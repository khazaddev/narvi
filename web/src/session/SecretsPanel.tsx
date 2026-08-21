// SecretsPanel.tsx -- Settings -> Secrets (§27.1/§25.1): the two
// secret-shaped tables (sandbox_secrets, provider_credentials), each
// partitioned repo/environment/global.
//
// # This Step's own defining risk, concretely, in this file
//
// Both management APIs are write-only by design (§27.1: "values never
// returned, never logged"). SandboxSecret/ProviderCredential's own
// generated shape PROVES this at the type level -- neither carries a
// `value` field at all, only `maskedValue: string` (a fixed, non-secret
// placeholder). This file never introduces a second field to hold a
// value, never logs a create/update mutation's own request body, and
// never echoes CreateSandboxSecretRequest.value/CreateProviderCredentialRequest.value
// back into any rendered element after the request that sent it -- the
// `value`/`newValue` local state below is cleared to '' the instant its
// own mutation succeeds (onSuccess), so it cannot linger in a re-render.
// If a GET here ever returned a real value, that would be a backend
// defect to report, not something to display -- see this Step's own
// task instructions.
//
// # Resolution order is shown, the WINNER is not computed here
//
// mockups.html's own Secrets table shows a "Resolved for payroll-stack"
// column -- a claim about which single row wins for one specific target,
// per the automation -> environment -> repo -> global order §27.1
// documents. That resolution is computed server-side, at boot, by
// sandbox-agent's own delivery-endpoint round trip (providercredential.
// Resolve / the sandbox_secrets equivalent) -- correctly reproducing it
// here would mean re-deriving which Environment a given repo's sessions
// actually use, information this Settings screen has no way to know
// (an Environment carries no back-reference to "the repo it is for").
// Rather than guess, this panel renders every configured row's own real
// scope chip (exactly what the mockup also shows) and states plainly, in
// prose, what the resolution order is and where it is actually computed
// -- an honest partial view, not a wrong one.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { ProviderCredential, SandboxSecret } from '@narvi/contracts/rest-dtos'

import { createProviderCredential, createSandboxSecret, deleteProviderCredential, deleteSandboxSecret, listEnvironments, listProviderCredentials, listSandboxSecrets, type SecretScope } from '../api/endpoints'
import { ApiError } from '../api/http'
import { environmentQueryKeys, providerCredentialQueryKeys, sandboxSecretQueryKeys } from '../api/queryKeys'
import { meQueryOptions } from '../auth/session'
import { environmentSummaryLine, secretScopeLabel, secretScopeTone } from './settingsFormat'
import { truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 500

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

function isMaintainerPlus(role: string | undefined): boolean {
  return role === 'admin' || role === 'maintainer'
}

/**
 * canManageScope mirrors the server's own split, which is NOT uniform across
 * the scope selector on this screen:
 *   - global scope  -> authz.ActionManageGlobalSecrets, admin only
 *   - repo / environment scope -> ActionManageRepoSecrets, admin + maintainer
 *
 * This panel used to apply maintainer+ to every scope, so a maintainer landing
 * on the default (global) tab was shown a live "Add secret" form directly above
 * "You're not authorized to view secrets at this scope" -- an affordance the
 * role model says can never succeed. The server fails closed either way; what
 * was wrong was the offer.
 */
function canManageScope(role: string | undefined, scope: SecretScope): boolean {
  if (scope.kind === 'global') return role === 'admin'
  return isMaintainerPlus(role)
}

/**
 * createFailureMessage keeps an authorization refusal from being reported as a
 * data problem. The fixed copy on these forms names a reserved prefix or a
 * duplicate at this scope; a 403 is neither, and telling an operator to "rotate
 * via delete + re-create" for rows they cannot even list sends them after a
 * remedy that cannot work.
 */
function createFailureMessage(error: unknown, conflictHint: string): string {
  if (error instanceof ApiError && error.status === 403) {
    return "Your role can't manage secrets at this scope."
  }
  return conflictHint
}

function ScopePicker({ scope, onChange }: { scope: SecretScope; onChange: (s: SecretScope) => void }) {
  const [owner, setOwner] = useState('')
  const [repo, setRepo] = useState('')
  const [environmentId, setEnvironmentId] = useState('')

  // The environment scope used to be a free-text id box. Nothing rejected a
  // wrong one: the create endpoint stores whatever target it is handed, so a
  // typo produced a secret attached to an environment that does not exist --
  // invisible at every real scope, delivered to nothing, and reported as
  // success. Anyone allowed to manage secrets is also allowed to list
  // environments (both are maintainer+), so the real set is always available
  // here; offering it as a choice is what makes the wrong value unreachable
  // rather than merely discouraged.
  const environmentsQuery = useQuery({
    queryKey: environmentQueryKeys.list(),
    queryFn: ({ signal }) => listEnvironments(signal),
    retry: false,
  })
  const environments = environmentsQuery.data?.environments

  return (
    <div className="formrow">
      <select
        className="sel-select"
        value={scope.kind}
        onChange={(e) => {
          const kind = e.target.value
          if (kind === 'global') onChange({ kind: 'global' })
          else if (kind === 'environment') onChange({ kind: 'environment', environmentId })
          else onChange({ kind: 'repo', owner, repo })
        }}
      >
        <option value="global">global</option>
        <option value="environment">environment</option>
        <option value="repo">repo</option>
      </select>
      {scope.kind === 'environment' &&
        (environments ? (
          <select
            className="sel-select"
            value={environmentId}
            onChange={(e) => {
              setEnvironmentId(e.target.value)
              onChange({ kind: 'environment', environmentId: e.target.value })
            }}
          >
            <option value="">select an environment…</option>
            {environments.map((env) => (
              <option key={env.id} value={env.id}>
                {environmentSummaryLine(env)}
              </option>
            ))}
          </select>
        ) : (
          // Only when the list genuinely could not be read -- never as the
          // default. Typing an id here can still miss, so say so rather than
          // letting it look equivalent to picking one.
          <input
            placeholder="environment id"
            title="The environment list could not be loaded, so this id is not checked against it."
            value={environmentId}
            onChange={(e) => {
              setEnvironmentId(e.target.value)
              onChange({ kind: 'environment', environmentId: e.target.value })
            }}
          />
        ))}
      {scope.kind === 'repo' && (
        <>
          <input
            placeholder="owner"
            value={owner}
            onChange={(e) => {
              setOwner(e.target.value)
              onChange({ kind: 'repo', owner: e.target.value, repo })
            }}
          />
          <input
            placeholder="repo"
            value={repo}
            onChange={(e) => {
              setRepo(e.target.value)
              onChange({ kind: 'repo', owner, repo: e.target.value })
            }}
          />
        </>
      )}
    </div>
  )
}

function scopeReady(scope: SecretScope): boolean {
  if (scope.kind === 'global') return true
  if (scope.kind === 'environment') return scope.environmentId.trim().length > 0
  return scope.owner.trim().length > 0 && scope.repo.trim().length > 0
}

/** SandboxSecretRow renders one sandbox secret's own table row -- exported for direct render-safety testing (mirrors AutomationsView.tsx's own AutomationRow precedent): s.name (a caller-chosen env-var name, free text validated server-side but never HTML-safety-validated) must render as plain text only, and s.maskedValue -- the ONLY value-shaped field this row ever touches -- must be the fixed, non-secret placeholder the server sent, never anything derived from an actual secret value (this component receives no `value` prop at all; SandboxSecret's own generated type has no such field to accidentally wire up). */
export function SandboxSecretRow({ secret, canManage, deleting, onDelete }: { secret: SandboxSecret; canManage: boolean; deleting: boolean; onDelete: () => void }) {
  return (
    <tr>
      <td>
        <T text={secret.name} />
      </td>
      <td>
        <span className={`chip ${secretScopeTone(secret.scope)}`}>
          <span className="dot" />
          <T text={secretScopeLabel(secret.scope, secret.scopeTarget)} />
        </span>
      </td>
      <td className="masked">{secret.maskedValue}</td>
      {canManage && (
        <td style={{ textAlign: 'right' }}>
          <button type="button" className="btn danger" style={{ padding: '2px 9px', fontSize: 11 }} disabled={deleting} onClick={onDelete}>
            Delete
          </button>
        </td>
      )}
    </tr>
  )
}

function SandboxSecretsTable({ scope, canManage }: { scope: SecretScope; canManage: boolean }) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const ready = scopeReady(scope)

  const query = useQuery({
    queryKey: sandboxSecretQueryKeys.list(scope),
    queryFn: ({ signal }) => listSandboxSecrets(scope, signal),
    enabled: ready,
    retry: false,
  })
  const createMutation = useMutation({
    mutationFn: () => createSandboxSecret(scope, { name, value }),
    onSuccess: () => {
      setName('')
      setValue('') // never linger past the request that sent it
      void queryClient.invalidateQueries({ queryKey: sandboxSecretQueryKeys.list(scope) })
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (secretId: string) => deleteSandboxSecret(scope, secretId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: sandboxSecretQueryKeys.list(scope) }),
  })

  if (!ready) return <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>Fill in the scope above to view/manage sandbox secrets at that target.</p>
  const forbidden = query.isError && query.error instanceof ApiError && query.error.status === 403

  return (
    <div>
      {canManage && (
        <div className="formrow">
          <input placeholder="NAME (env var)" value={name} onChange={(e) => setName(e.target.value)} />
          <input placeholder="value" type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} />
          <button type="button" className="btn primary" disabled={name.trim().length === 0 || value.length === 0 || createMutation.isPending} onClick={() => createMutation.mutate()}>
            {createMutation.isPending ? 'Saving…' : 'Add secret'}
          </button>
        </div>
      )}
      {createMutation.isError && (
        <p className="sidebar-notice">
          {createFailureMessage(
            createMutation.error,
            "Couldn't save (a reserved NARVI_*/OPENCODE_* name, a name providercredential already owns, or a duplicate at this scope -- rotate via delete + re-create instead).",
          )}
        </p>
      )}
      {forbidden && <p className="notavailable">You're not authorized to view secrets at this scope.</p>}
      {!forbidden && query.isPending && <p className="rail-empty">Loading…</p>}
      {!forbidden && query.isError && <p className="rail-empty">Couldn't load sandbox secrets.</p>}
      {!forbidden && query.isSuccess && (
        <table className="sectable">
          <thead>
            <tr>
              <th>Name</th>
              <th>Scope</th>
              <th>Value</th>
              {canManage && <th />}
            </tr>
          </thead>
          <tbody>
            {query.data.sandboxSecrets.length === 0 && (
              <tr>
                <td colSpan={4} style={{ color: 'var(--faint)' }}>
                  None configured.
                </td>
              </tr>
            )}
            {query.data.sandboxSecrets.map((s: SandboxSecret) => (
              <SandboxSecretRow key={s.id} secret={s} canManage={canManage} deleting={deleteMutation.isPending} onDelete={() => deleteMutation.mutate(s.id)} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

/** ProviderCredentialRow renders one provider credential's own table row -- exported for direct render-safety testing, mirroring SandboxSecretRow above. provider is a closed 3-value Postgres enum ('google'|'anthropic'|'openai'), never free text, so it renders unescaped as a literal; maskedValue is the ONLY value-shaped field, always the fixed non-secret placeholder -- this component receives no `value` prop (ProviderCredential's own generated type has none). */
export function ProviderCredentialRow({ credential, canManage, deleting, onDelete }: { credential: ProviderCredential; canManage: boolean; deleting: boolean; onDelete: () => void }) {
  return (
    <tr>
      <td>{credential.provider}</td>
      <td>
        <span className={`chip ${secretScopeTone(credential.scope)}`}>
          <span className="dot" />
          <T text={secretScopeLabel(credential.scope, credential.scopeTarget)} />
        </span>
      </td>
      <td className="masked">{credential.maskedValue}</td>
      {canManage && (
        <td style={{ textAlign: 'right' }}>
          <button type="button" className="btn danger" style={{ padding: '2px 9px', fontSize: 11 }} disabled={deleting} onClick={onDelete}>
            Delete
          </button>
        </td>
      )}
    </tr>
  )
}

function ProviderCredentialsTable({ scope, canManage }: { scope: SecretScope; canManage: boolean }) {
  const queryClient = useQueryClient()
  const [provider, setProvider] = useState<'anthropic' | 'openai' | 'google'>('anthropic')
  const [value, setValue] = useState('')
  const ready = scopeReady(scope)

  const query = useQuery({
    queryKey: providerCredentialQueryKeys.list(scope),
    queryFn: ({ signal }) => listProviderCredentials(scope, signal),
    enabled: ready,
    retry: false,
  })
  const createMutation = useMutation({
    mutationFn: () => createProviderCredential(scope, { provider, value }),
    onSuccess: () => {
      setValue('')
      void queryClient.invalidateQueries({ queryKey: providerCredentialQueryKeys.list(scope) })
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (credentialId: string) => deleteProviderCredential(scope, credentialId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: providerCredentialQueryKeys.list(scope) }),
  })

  if (!ready) return <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>Fill in the scope above to view/manage provider credentials at that target.</p>
  const forbidden = query.isError && query.error instanceof ApiError && query.error.status === 403

  return (
    <div>
      {canManage && (
        <div className="formrow">
          <select className="sel-select" value={provider} onChange={(e) => setProvider(e.target.value as typeof provider)}>
            <option value="anthropic">anthropic</option>
            <option value="openai">openai</option>
            <option value="google">google</option>
          </select>
          <input placeholder="value" type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} />
          <button type="button" className="btn primary" disabled={value.length === 0 || createMutation.isPending} onClick={() => createMutation.mutate()}>
            {createMutation.isPending ? 'Saving…' : 'Add credential'}
          </button>
        </div>
      )}
      {createMutation.isError && (
        <p className="sidebar-notice">{createFailureMessage(createMutation.error, "Couldn't save (a duplicate provider at this scope -- rotate via delete + re-create instead).")}</p>
      )}
      {forbidden && <p className="notavailable">You're not authorized to view credentials at this scope.</p>}
      {!forbidden && query.isPending && <p className="rail-empty">Loading…</p>}
      {!forbidden && query.isError && <p className="rail-empty">Couldn't load provider credentials.</p>}
      {!forbidden && query.isSuccess && (
        <table className="sectable">
          <thead>
            <tr>
              <th>Provider</th>
              <th>Scope</th>
              <th>Value</th>
              {canManage && <th />}
            </tr>
          </thead>
          <tbody>
            {query.data.providerCredentials.length === 0 && (
              <tr>
                <td colSpan={4} style={{ color: 'var(--faint)' }}>
                  None configured.
                </td>
              </tr>
            )}
            {query.data.providerCredentials.map((c: ProviderCredential) => (
              <ProviderCredentialRow key={c.id} credential={c} canManage={canManage} deleting={deleteMutation.isPending} onDelete={() => deleteMutation.mutate(c.id)} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

export function SecretsPanel() {
  const meQuery = useQuery(meQueryOptions)
  const [scope, setScope] = useState<SecretScope>({ kind: 'global' })
  // Scope-aware, not role-alone: global secrets are admin-only while
  // repo/environment secrets are maintainer+ (canManageScope's own comment).
  const canManage = canManageScope(meQuery.data?.role, scope)

  return (
    <div className="panel">
      <h4>Secrets</h4>
      <p className="ph">
        A sandbox resolves secrets most-specific-first: automation, then environment, then repository, then global. This table lists every configured row at the selected scope, not the one that would win for a
        particular sandbox. Global secrets are admin-only; repository and environment secrets can also be managed by maintainers.
      </p>
      <ScopePicker scope={scope} onChange={setScope} />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, marginTop: 8 }}>
        <div>
          <b style={{ fontSize: 'var(--text-sm)' }}>Sandbox secrets</b>
          <SandboxSecretsTable scope={scope} canManage={canManage} />
        </div>
        <div>
          <b style={{ fontSize: 'var(--text-sm)' }}>Provider credentials</b>
          <ProviderCredentialsTable scope={scope} canManage={canManage} />
        </div>
      </div>
    </div>
  )
}

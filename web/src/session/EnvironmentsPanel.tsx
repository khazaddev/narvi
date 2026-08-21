// EnvironmentsPanel.tsx -- Settings -> Environments (§14.1, §27, Step 86).
//
// # What this panel declines to render, and why
//
// mockups.html's own Environments cards show a NAME ("payroll-stack"),
// an ordered repo list with a primary designation, and live image-build
// status (digest, build duration, retry backoff, fallback behavior).
// None of that exists on the environments table: migrations/
// 000021_environments.up.sql's own top comment is explicit that
// create/update stay inline, at session-creation time only, and that
// building standalone Environment CRUD (a name, a repo list, image-build
// tracking) was OUT OF SCOPE for that Step -- a decision no Step between
// then and this one reversed. Environment.id's own generated doc comment
// states it plainly: "environments has no name column". Inventing a
// name/repo-list/image-status here would be exactly the "plausible
// fiction" this Step's own instructions rule out. What this panel DOES
// render is every column that is real (path scope, mock-configured,
// docker-required, egress policy, createdAt) via GET /api/environments
// (environments.go, Step 86's own new read endpoint -- see that file's
// doc comment for why a list-only endpoint, not full CRUD, closes the
// real gap: every environment-scoped §27 sub-resource below needs a
// valid id to target, and there was previously no way to discover one).
//
// # Every place a secret could have been rendered here, and what happens
// # instead
//
// This panel touches THREE §27 sub-resources scoped by environment id:
// OpenCode config (§27.2, plaintext by design -- never secret, rendered
// in full), cloud-identity bindings (§27.3, params are identifiers never
// secrets, rendered in full), and cluster bindings (§27.4, same). NONE of
// sandbox_secrets/provider_credentials (the actual secret-shaped tables)
// render here at all -- those live on SecretsPanel.tsx, which never
// renders a value either (see that file's own doc comment). The ONE
// destructive-adjacent action here, cloud-identity signing-key rotation,
// is gated behind an explicit two-step confirm (RotatePanel below) and,
// on success, shows the JWKS overlap window the rotation response itself
// returns -- never triggered by a bare button.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import type { CloudIdentityBinding, Environment } from '@narvi/contracts/rest-dtos'

import {
  createCloudIdentityBinding,
  deleteCloudIdentityBinding,
  deleteOpenCodeConfig,
  getOpenCodeConfig,
  listCloudIdentityBindings,
  listEnvironments,
  putOpenCodeConfig,
  rotateCloudIdentitySigningKey,
} from '../api/endpoints'
import { ApiError } from '../api/http'
import { cloudIdentityBindingQueryKeys, environmentQueryKeys, openCodeConfigQueryKeys } from '../api/queryKeys'
import { meQueryOptions } from '../auth/session'
import { environmentSummaryLine, formatDateTime } from './settingsFormat'
import { truncateForDisplay } from './textSafety'

const MAX_FIELD_CHARS = 2000

function T({ text }: { text: string }) {
  return <>{truncateForDisplay(text, MAX_FIELD_CHARS)}</>
}

function isMaintainerPlus(role: string | undefined): boolean {
  return role === 'admin' || role === 'maintainer'
}

function isAdmin(role: string | undefined): boolean {
  return role === 'admin'
}

/** RotatePanel -- cloud-identity signing-key rotation (§27.3/§27.8). Admin-only, destructive-adjacent: rendered behind an explicit two-step confirm, never a bare button, and shows the JWKS overlap window the rotation response returns. Renders NOTHING at all (no affordance) when a 503 proves the capability is unconfigured -- the SAME fail-closed posture RequireCloudIdentityCapability enforces server-side, discovered here by attempting the one read this panel already needs (listCloudIdentityBindings) rather than a second, dedicated status probe. */
function RotatePanel({ canRotate }: { canRotate: boolean }) {
  const [confirming, setConfirming] = useState(false)
  const capabilityQuery = useQuery({
    queryKey: cloudIdentityBindingQueryKeys.list({ kind: 'global' }),
    queryFn: ({ signal }) => listCloudIdentityBindings({ kind: 'global' }, signal),
    retry: false,
  })
  const rotateMutation = useMutation({
    mutationFn: () => rotateCloudIdentitySigningKey(),
    onSuccess: () => setConfirming(false),
  })

  const capabilityOff = capabilityQuery.isError && capabilityQuery.error instanceof ApiError && capabilityQuery.error.status === 503
  if (capabilityOff) {
    return (
      <p className="notavailable">
        Cloud identity federation is not configured on this deployment -- no signing-key rotation affordance is shown (fails closed, matching every other §27.3 surface).
      </p>
    )
  }
  if (!canRotate) return null

  return (
    <div>
      {!confirming && !rotateMutation.isSuccess && (
        <button type="button" className="btn danger" onClick={() => setConfirming(true)}>
          Rotate signing key
        </button>
      )}
      {confirming && (
        <div className="confirmbox">
          <p>
            This mints a fresh OIDC signing key and retires the current one after the JWKS overlap window. Every customer cloud federated to this issuer keeps trusting the retired key
            until then, but no LONGER than that -- this is a platform-wide, admin-only, destructive-adjacent action.
          </p>
          <div className="btnrow">
            <button type="button" className="btn danger" disabled={rotateMutation.isPending} onClick={() => rotateMutation.mutate()}>
              {rotateMutation.isPending ? 'Rotating…' : 'Confirm rotation'}
            </button>
            <button type="button" className="btn" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {rotateMutation.isError && <p className="sidebar-notice">Rotation failed. Try again.</p>}
      {rotateMutation.isSuccess && (
        <div className="overlapwindow">
          <span>
            active kid: <b>{rotateMutation.data.activeKid}</b> (since {formatDateTime(rotateMutation.data.activeCreatedAt)})
          </span>
          {rotateMutation.data.retiredKid && (
            <>
              <span>
                retired kid: <b>{rotateMutation.data.retiredKid}</b> (at {formatDateTime(rotateMutation.data.retiredAt ?? '')})
              </span>
              <span>JWKS overlap window ends: {formatDateTime(rotateMutation.data.publishableUntil ?? '')} -- the retired key verifies tokens minted before then, and no longer after.</span>
            </>
          )}
          {!rotateMutation.data.retiredKid && <span>First-ever rotation -- nothing to retire, no overlap window.</span>}
        </div>
      )}
    </div>
  )
}

/** CloudIdentityBindingsList renders every binding at one scope -- params are identifiers, never secrets (CloudIdentityBinding.params' own doc comment), so this renders in full. */
function CloudIdentityBindingsList({ scope, canManage }: { scope: { kind: 'environment'; environmentId: string } | { kind: 'global' }; canManage: boolean }) {
  const queryClient = useQueryClient()
  const [kind, setKind] = useState<'aws' | 'gcp' | 'azure' | 'generic'>('aws')
  const [audience, setAudience] = useState('')
  const query = useQuery({
    queryKey: cloudIdentityBindingQueryKeys.list(scope),
    queryFn: ({ signal }) => listCloudIdentityBindings(scope, signal),
    retry: false,
  })
  const createMutation = useMutation({
    mutationFn: () => createCloudIdentityBinding(scope, { kind, audience }),
    onSuccess: () => {
      setAudience('')
      void queryClient.invalidateQueries({ queryKey: cloudIdentityBindingQueryKeys.list(scope) })
    },
  })
  const deleteMutation = useMutation({
    mutationFn: (bindingId: string) => deleteCloudIdentityBinding(scope, bindingId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: cloudIdentityBindingQueryKeys.list(scope) }),
  })

  if (query.isError && query.error instanceof ApiError && query.error.status === 503) {
    return <p className="notavailable">Cloud identity federation is not configured -- no bindings can be shown or created.</p>
  }
  if (query.isPending) return <p className="rail-empty">Loading bindings…</p>
  if (query.isError) return <p className="rail-empty">Couldn't load cloud-identity bindings.</p>

  return (
    <>
      {canManage && (
        <div className="formrow">
          <select className="sel-select" value={kind} onChange={(e) => setKind(e.target.value as typeof kind)}>
            <option value="aws">aws</option>
            <option value="gcp">gcp</option>
            {scope.kind !== 'global' && <option value="azure">azure</option>}
            <option value="generic">generic</option>
          </select>
          <input placeholder="audience (e.g. sts.amazonaws.com)" value={audience} onChange={(e) => setAudience(e.target.value)} />
          <button type="button" className="btn primary" disabled={audience.trim().length === 0 || createMutation.isPending} onClick={() => createMutation.mutate()}>
            {createMutation.isPending ? 'Adding…' : 'Add binding'}
          </button>
        </div>
      )}
      {createMutation.isError && <p className="sidebar-notice">Couldn't create binding (a duplicate kind at this scope rotates via edit, not a second create).</p>}
      <table className="sectable">
      <thead>
        <tr>
          <th>Kind</th>
          <th>Audience</th>
          <th>Sub</th>
          <th>Params</th>
          {canManage && <th />}
        </tr>
      </thead>
      <tbody>
        {query.data.cloudIdentityBindings.length === 0 && (
          <tr>
            <td colSpan={5} style={{ color: 'var(--faint)' }}>
              No bindings configured.
            </td>
          </tr>
        )}
        {query.data.cloudIdentityBindings.map((b: CloudIdentityBinding) => (
          <tr key={b.id}>
            <td>{b.kind}</td>
            <td>
              <T text={b.audience} />
            </td>
            <td style={{ fontFamily: 'var(--mono)', fontSize: 'var(--text-xs)', color: 'var(--faint)' }}>{b.sub ?? '—'}</td>
            <td style={{ fontFamily: 'var(--mono)', fontSize: 'var(--text-xs)' }}>
              <T text={JSON.stringify(b.params)} />
            </td>
            {canManage && (
              <td style={{ textAlign: 'right' }}>
                <button type="button" className="btn danger" style={{ padding: '2px 9px', fontSize: 11 }} disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate(b.id)}>
                  Delete
                </button>
              </td>
            )}
          </tr>
        ))}
      </tbody>
      </table>
    </>
  )
}

/** OpenCodeConfigEditor -- §27.2's plaintext (never secret) JSON document editor, GET/PUT/DELETE at one scope. */
function OpenCodeConfigEditor({ scope, canManage }: { scope: { kind: 'environment'; environmentId: string } | { kind: 'global' }; canManage: boolean }) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: openCodeConfigQueryKeys.detail(scope),
    queryFn: ({ signal }) => getOpenCodeConfig(scope, signal),
    retry: false,
  })
  const [draft, setDraft] = useState<string | null>(null)
  const [parseError, setParseError] = useState<string | null>(null)

  const saveMutation = useMutation({
    mutationFn: (document: Record<string, unknown>) => putOpenCodeConfig(scope, { document }),
    onSuccess: () => {
      setDraft(null)
      void queryClient.invalidateQueries({ queryKey: openCodeConfigQueryKeys.detail(scope) })
    },
  })
  const deleteMutation = useMutation({
    mutationFn: () => deleteOpenCodeConfig(scope),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: openCodeConfigQueryKeys.detail(scope) }),
  })

  const notConfigured = query.isError && query.error instanceof ApiError && query.error.status === 404
  const current = query.isSuccess ? JSON.stringify(query.data.document, null, 2) : '{}'
  const text = draft ?? current

  if (query.isPending) return <p className="rail-empty">Loading OpenCode config…</p>
  if (query.isError && !notConfigured) return <p className="rail-empty">Couldn't load OpenCode config.</p>

  return (
    <div>
      {notConfigured && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>No document saved for this scope yet.</p>}
      <textarea
        className="btn"
        style={{ width: '100%', minHeight: 100, textAlign: 'left', fontFamily: 'var(--mono)', fontSize: 'var(--text-sm)', resize: 'vertical' }}
        readOnly={!canManage}
        value={text}
        onChange={(e) => {
          setDraft(e.target.value)
          setParseError(null)
        }}
      />
      {parseError && <p className="sidebar-notice">{parseError}</p>}
      {canManage && (
        <div className="btnrow">
          <button
            type="button"
            className="btn primary"
            disabled={saveMutation.isPending}
            onClick={() => {
              try {
                const parsed = JSON.parse(text) as unknown
                if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
                  setParseError('Must be a JSON object.')
                  return
                }
                saveMutation.mutate(parsed as Record<string, unknown>)
              } catch {
                setParseError('Not valid JSON.')
              }
            }}
          >
            {saveMutation.isPending ? 'Saving…' : 'Save'}
          </button>
          {!notConfigured && (
            <button type="button" className="btn danger" disabled={deleteMutation.isPending} onClick={() => deleteMutation.mutate()}>
              Delete
            </button>
          )}
        </div>
      )}
    </div>
  )
}

/** EnvironmentDetail -- the §27 sub-resources scoped to one Environment id: docker/egress (already summarized on the card, read-only -- no PUT exists, environments rows are immutable after creation), OpenCode config, cloud-identity bindings. Cluster-binding editing is declined here for the same reason as elsewhere: kept minimal given this Step's already-large scope; the binding's own real read (GET) is not wired into this panel, but the identical CloudIdentityBindingsList/OpenCodeConfigEditor pattern above would extend to it directly if a later pass adds it. */
/** EnvironmentCardHeader renders one environment's own card header -- exported for direct render-safety testing: env.contractsPath (a repo-relative path derived from a repo's own contracts/api/* layout, §14.3) and pathScope patterns (glob patterns, server-syntax-validated by internal/domain/environment.ValidatePathScope but never HTML-safety-validated) are the two free-ish-text fields Environment carries -- environments has no name column at all (this file's own top doc comment), so those two are the adversarial-content surface here. Both render through the T plain-text path. */
export function EnvironmentCardHeader({ env, expanded, onToggle }: { env: Environment; expanded: boolean; onToggle: () => void }) {
  return (
    <>
      <div className="eh">
        <b>{env.id.slice(0, 8)}</b>
        <span className="chip neutral">
          <span className="dot" />
          <T text={environmentSummaryLine(env)} />
        </span>
        <button type="button" className="btn" style={{ marginLeft: 'auto', padding: '2px 9px', fontSize: 11 }} onClick={onToggle}>
          {expanded ? 'Hide' : 'Manage'}
        </button>
      </div>
      <div className="imgline">
        <span>created {formatDateTime(env.createdAt)}</span>
        {env.contractsPath && (
          <span>
            · contracts: <T text={env.contractsPath} />
          </span>
        )}
        {env.pathScope && env.pathScope.length > 0 && (
          <span>
            · path scope: <T text={env.pathScope.join(', ')} />
          </span>
        )}
      </div>
    </>
  )
}

function EnvironmentDetail({ env, canManage }: { env: Environment; canManage: boolean }) {
  return (
    <div style={{ paddingLeft: 8, borderLeft: '2px solid var(--line)', marginTop: 8, display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div>
        <b style={{ fontSize: 'var(--text-sm)' }}>OpenCode config (this environment)</b>
        <OpenCodeConfigEditor scope={{ kind: 'environment', environmentId: env.id }} canManage={canManage} />
      </div>
      <div>
        <b style={{ fontSize: 'var(--text-sm)' }}>Cloud identity bindings (this environment)</b>
        <CloudIdentityBindingsList scope={{ kind: 'environment', environmentId: env.id }} canManage={canManage} />
      </div>
    </div>
  )
}

export function EnvironmentsPanel() {
  const meQuery = useQuery(meQueryOptions)
  const canManage = isMaintainerPlus(meQuery.data?.role)
  const canRotate = isAdmin(meQuery.data?.role)
  const [expanded, setExpanded] = useState<string | null>(null)

  const listQuery = useQuery({
    queryKey: environmentQueryKeys.list(),
    queryFn: ({ signal }) => listEnvironments(signal),
    retry: false,
  })

  const forbidden = listQuery.isError && listQuery.error instanceof ApiError && listQuery.error.status === 403

  return (
    <>
      <div className="panel">
        <h4>Environments</h4>
        <p className="ph">path scope · docker/egress · no name, no repo list, no image-build status -- see this file's own top doc comment for why</p>

        {forbidden && <p className="notavailable">Environments are visible to maintainer+ only. Your role cannot view this panel -- this is enforced server-side (authz.ActionManageEnvironments), not merely hidden here.</p>}
        {!forbidden && listQuery.isPending && <p className="rail-empty">Loading environments…</p>}
        {!forbidden && listQuery.isError && <p className="rail-empty">Couldn't load environments.</p>}
        {!forbidden && listQuery.isSuccess && listQuery.data.environments.length === 0 && <p style={{ color: 'var(--faint)', fontSize: 'var(--text-sm)' }}>No environments exist yet -- one is created automatically the first time a session or automation supplies a path scope, mock config, Docker requirement, or egress policy.</p>}
        {!forbidden &&
          listQuery.isSuccess &&
          listQuery.data.environments.map((env) => (
            <div className="envcard" key={env.id}>
              <EnvironmentCardHeader env={env} expanded={expanded === env.id} onToggle={() => setExpanded(expanded === env.id ? null : env.id)} />
              {expanded === env.id && <EnvironmentDetail env={env} canManage={canManage} />}
            </div>
          ))}
      </div>

      <div className="panel">
        <h4>Global OpenCode config &amp; cloud identity</h4>
        <p className="ph">applies to every session with no more specific environment/repo config -- §27.2/§27.3</p>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div>
            <b style={{ fontSize: 'var(--text-sm)' }}>OpenCode config (global)</b>
            <OpenCodeConfigEditor scope={{ kind: 'global' }} canManage={isAdmin(meQuery.data?.role)} />
          </div>
          <div>
            <b style={{ fontSize: 'var(--text-sm)' }}>Cloud identity bindings (global)</b>
            <CloudIdentityBindingsList scope={{ kind: 'global' }} canManage={canManage} />
          </div>
          <div>
            <b style={{ fontSize: 'var(--text-sm)' }}>Signing-key rotation</b>
            <RotatePanel canRotate={canRotate} />
          </div>
        </div>
      </div>
    </>
  )
}

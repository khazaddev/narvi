// settingsFormat.ts -- pure, testable formatting/derivation helpers for
// the Settings + Analytics views (§12.2 item 5, Step 86). Mirrors this
// codebase's own established split (automationFormat.ts, planFormat.ts,
// reviewFormat.ts): render logic that does not need React lives here, so
// it can be unit-tested without rendering anything.
import type { CloudIdentityBinding, Environment, Member, ProviderCredential, SandboxSecret } from '@narvi/contracts/rest-dtos'

/** roleTone maps a §13.3 role to a chip tone class, matching mockups.html's own chip vocabulary (ok/warn/crit/neutral/run). */
export function roleTone(role: Member['role']): string {
  switch (role) {
    case 'admin':
      return 'run'
    case 'maintainer':
      return 'ok'
    case 'member':
      return 'neutral'
    case 'viewer':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/** identityProviderLabel renders a stable, non-model-authored label for a §13.2 identity provider -- these 4 values are a closed Postgres enum (identity_provider), never third-party text, so this is safe to hard-code (unlike environment/secret/repo names, which always go through the T/truncateForDisplay plain-text path instead). */
export function identityProviderLabel(provider: string): string {
  switch (provider) {
    case 'github':
      return 'github'
    case 'slack':
      return 'slack'
    case 'linear':
      return 'linear'
    case 'google':
      return 'google'
    default:
      return provider
  }
}

/** secretScopeLabel renders SandboxSecret/ProviderCredential.scope + scopeTarget as the mockup's own "environment" / "repo · payroll-api" / "global" chip text -- scopeTarget is either a repo_full_name (human-authored, but a STRUCTURED identifier the caller already typed to reach this scope, not free text pulled from elsewhere) or an environments.id (opaque UUID, truncated for readability). */
export function secretScopeLabel(scope: SandboxSecret['scope'] | ProviderCredential['scope'], scopeTarget: string | null): string {
  if (scope === 'global') return 'global'
  if (scope === 'repo') return scopeTarget ? `repo · ${scopeTarget}` : 'repo'
  if (scope === 'environment') return scopeTarget ? `environment · ${scopeTarget.slice(0, 8)}` : 'environment'
  return scope
}

/** secretScopeTone maps a secret's own scope to a chip tone -- 'environment' gets the mockup's own distinct "run" (accent) tone since it is the most-specific, most-commonly-relevant scope in the resolution order; repo/global stay neutral. */
export function secretScopeTone(scope: SandboxSecret['scope'] | ProviderCredential['scope']): string {
  return scope === 'environment' ? 'run' : 'neutral'
}

/** environmentSummaryLine renders an Environment's own real, non-fabricated fields as one compact line -- deliberately never a name (environments carries none, Environment.id's own generated doc comment) and never a repo list/image-build status (no such data exists on this row; see EnvironmentsPanel.tsx's own top doc comment for what was declined and why). */
export function environmentSummaryLine(env: Environment): string {
  const parts: string[] = []
  parts.push(env.pathScope && env.pathScope.length > 0 ? `path-scoped (${env.pathScope.length} pattern${env.pathScope.length === 1 ? '' : 's'})` : 'full repo access')
  if (env.mockConfigured) parts.push('mock configured')
  if (env.dockerRequired) parts.push('docker required')
  if (env.egressPolicyMode) parts.push(`egress: ${env.egressPolicyMode}`)
  return parts.join(' · ')
}

/** cloudIdentityBindingSummary renders a CloudIdentityBinding's own real fields -- kind + audience, params rendered separately (a raw-JSON passthrough, never assumed to have any particular key). */
export function cloudIdentityBindingSummary(binding: CloudIdentityBinding): string {
  return `${binding.kind} · aud ${binding.audience}`
}

/** formatDateTime renders an ISO timestamp as a stable, locale-independent absolute string (unlike relativeTime.ts's own decorative elapsed-time text) -- used for audit-log entries and secret createdAt/updatedAt, where an admin auditing a security-relevant surface needs the real instant, not "2 min ago". */
export function formatDateTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z')
}

/** lookbackDaysLabel renders RepoDigestScope.lookbackDays as the mockup-style "last N days" window label. */
export function lookbackDaysLabel(days: number): string {
  return `last ${days} day${days === 1 ? '' : 's'}`
}

/**
 * identityLinkProof describes how a §13.2 identity came to be attached to its
 * member, in the terms the screen has to answer: was this link PROVEN by the
 * person, or asserted on their behalf?
 *
 * §13.2 gives three linked_via values and only two answers:
 *   - auto_email: matched against exactly one VERIFIED email address.
 *   - prompt:     the person followed the short-lived magic link sent to that
 *                 provider account, which proves they control it.
 *   - admin:      an admin force-linked it (§13.2 step 5). Nothing was proven
 *                 by the person; someone with the power to do so asserted it.
 *
 * The first two are proof by different mechanisms and read the same. The third
 * must NOT: rendering it with the same check mark as a verified link erases the
 * one distinction this column exists to show. linked_via is a closed Postgres
 * enum, so an unknown value can only mean the enum grew -- report it as
 * unproven rather than quietly granting it a check mark.
 */
export function identityLinkProof(linkedVia: string): { tone: 'ok' | 'pend'; mark: string; title: string } {
  switch (linkedVia) {
    case 'auto_email':
      return { tone: 'ok', mark: '✓', title: 'Verified: matched a verified email address' }
    case 'prompt':
      return { tone: 'ok', mark: '✓', title: 'Verified: the member followed the link sent to this account' }
    case 'admin':
      return { tone: 'pend', mark: '!', title: 'Force-linked by an admin — not verified by the member' }
    default:
      return { tone: 'pend', mark: '!', title: `Unrecognised link method (${linkedVia}) — treated as unverified` }
  }
}

/**
 * cloudIdentityParamFields lists the params a cloud-identity binding of each
 * kind MUST carry to be usable, mirroring internal/domain/cloudidentity's own
 * ValidateParams (§27.3) key for key.
 *
 * These are not optional extras. sandbox-agent runs ValidateParams before it
 * trusts a delivered binding, and a binding that fails it is skipped with a
 * log line and nothing else — no error reaches the control plane, and none
 * reaches this screen. The create form used to send no params at all, so
 * every binding it produced was accepted with 201, listed as configured, and
 * then silently ignored at every boot. Collecting the required keys here is
 * what makes the row that appears in the table the row that actually works.
 *
 * params themselves are identifiers, never secrets (§27.3), which is why they
 * are plain text inputs and render in full in the table.
 */
export const cloudIdentityParamFields: Record<string, { key: string; label: string; placeholder: string }[]> = {
  aws: [{ key: 'roleArn', label: 'Role ARN', placeholder: 'arn:aws:iam::123456789012:role/narvi' }],
  gcp: [{ key: 'workloadIdentityProvider', label: 'Workload identity provider', placeholder: 'projects/1/locations/global/workloadIdentityPools/p/providers/v' }],
  azure: [
    { key: 'clientId', label: 'Client id', placeholder: '00000000-0000-0000-0000-000000000000' },
    { key: 'tenantId', label: 'Tenant id', placeholder: '00000000-0000-0000-0000-000000000000' },
  ],
  generic: [{ key: 'envVar', label: 'Env var', placeholder: 'CLOUD_IDENTITY_TOKEN_FILE' }],
}

/** cloudIdentityParamsComplete reports whether every field cloudIdentityParamFields requires for kind has a non-blank value -- the same condition ValidateParams applies server-side, checked here so the form cannot submit a binding that would be skipped at boot. */
export function cloudIdentityParamsComplete(kind: string, params: Record<string, string>): boolean {
  const fields = cloudIdentityParamFields[kind]
  if (!fields) return false
  return fields.every((f) => (params[f.key] ?? '').trim().length > 0)
}

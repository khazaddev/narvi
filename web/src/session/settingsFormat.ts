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

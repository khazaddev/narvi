import { describe, expect, it } from 'vitest'

import { chatgptCardPresentation, chatgptLinkStatusPresentation, cloudIdentityBindingSummary, cloudIdentityParamsComplete, environmentSummaryLine, formatDateTime, identityLinkProof, identityProviderLabel, integrationOutboundTone, integrationSurfaceLabel, lookbackDaysLabel, roleTone, secretScopeLabel, secretScopeTone } from '../settingsFormat'

describe('roleTone', () => {
  it('maps every §13.3 role to a chip tone', () => {
    expect(roleTone('admin')).toBe('run')
    expect(roleTone('maintainer')).toBe('ok')
    expect(roleTone('member')).toBe('neutral')
    expect(roleTone('viewer')).toBe('neutral')
  })
})

describe('identityProviderLabel', () => {
  it('renders every closed identity_provider value', () => {
    expect(identityProviderLabel('github')).toBe('github')
    expect(identityProviderLabel('slack')).toBe('slack')
    expect(identityProviderLabel('linear')).toBe('linear')
    expect(identityProviderLabel('google')).toBe('google')
  })
})

describe('secretScopeLabel', () => {
  it('renders global with no target', () => {
    expect(secretScopeLabel('global', null)).toBe('global')
  })
  it('renders repo with its target', () => {
    expect(secretScopeLabel('repo', 'acme/widgets')).toBe('repo · acme/widgets')
  })
  it('renders environment, truncating the id for readability', () => {
    expect(secretScopeLabel('environment', 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee')).toBe('environment · aaaaaaaa')
  })
})

describe('secretScopeTone', () => {
  it('gives environment scope the distinct accent tone, everything else neutral', () => {
    expect(secretScopeTone('environment')).toBe('run')
    expect(secretScopeTone('repo')).toBe('neutral')
    expect(secretScopeTone('global')).toBe('neutral')
  })
})

describe('environmentSummaryLine', () => {
  it('never fabricates a name -- renders only real columns', () => {
    const line = environmentSummaryLine({
      id: 'e1',
      pathScope: ['web/**', 'contracts/api/**'],
      mockConfigured: true,
      contractsPath: 'contracts/api/openapi.yaml',
      dockerRequired: true,
      egressPolicyMode: 'allowlist',
      egressPolicyAllowlist: ['registry.example.invalid'],
      createdAt: '2026-08-20T00:00:00Z',
    })
    expect(line).toContain('path-scoped (2 patterns)')
    expect(line).toContain('mock configured')
    expect(line).toContain('docker required')
    expect(line).toContain('egress: allowlist')
  })

  it('renders "full repo access" when pathScope is null', () => {
    const line = environmentSummaryLine({
      id: 'e1',
      pathScope: null,
      mockConfigured: false,
      contractsPath: null,
      dockerRequired: false,
      egressPolicyMode: null,
      egressPolicyAllowlist: null,
      createdAt: '2026-08-20T00:00:00Z',
    })
    expect(line).toBe('full repo access')
  })
})

describe('cloudIdentityBindingSummary', () => {
  it('renders kind and audience', () => {
    const summary = cloudIdentityBindingSummary({
      id: 'b1',
      scope: 'global',
      scopeTarget: null,
      kind: 'aws',
      audience: 'sts.amazonaws.com',
      params: {},
      sub: null,
      createdAt: '2026-08-20T00:00:00Z',
      updatedAt: '2026-08-20T00:00:00Z',
    })
    expect(summary).toBe('aws · aud sts.amazonaws.com')
  })
})

describe('formatDateTime', () => {
  it('renders a stable absolute string, not a relative one', () => {
    expect(formatDateTime('2026-08-20T02:03:04Z')).toBe('2026-08-20 02:03:04Z')
  })
  it('returns the raw string for an unparseable value rather than throwing', () => {
    expect(formatDateTime('not-a-date')).toBe('not-a-date')
  })
})

describe('lookbackDaysLabel', () => {
  it('pluralizes correctly', () => {
    expect(lookbackDaysLabel(1)).toBe('last 1 day')
    expect(lookbackDaysLabel(30)).toBe('last 30 days')
  })
})

// identityLinkProof decides whether a member's linked identity is shown as
// verified. The generated DTO types linkedVia as a closed union, so an
// unrecognised value cannot be written through the component in TypeScript --
// but the type is a compile-time claim about a runtime wire value, and the
// enum growing server-side is exactly how one arrives. The default branch is
// therefore reachable in production and untestable through the component, so
// it is pinned here, at the helper, where the value can actually be supplied.
describe('identityLinkProof', () => {
  it('treats an email match as verified', () => {
    expect(identityLinkProof('auto_email').tone).toBe('ok')
    expect(identityLinkProof('auto_email').mark).toBe('✓')
  })

  it('treats a followed magic link as verified', () => {
    expect(identityLinkProof('prompt').tone).toBe('ok')
    expect(identityLinkProof('prompt').mark).toBe('✓')
  })

  it('does NOT treat an admin force-link as verified', () => {
    expect(identityLinkProof('admin').tone).toBe('pend')
    expect(identityLinkProof('admin').mark).not.toBe('✓')
  })

  it('fails closed on an unrecognised link method', () => {
    expect(identityLinkProof('some_future_mechanism').tone).toBe('pend')
    expect(identityLinkProof('some_future_mechanism').mark).not.toBe('✓')
  })
})

// cloudIdentityParamsComplete gates the create button on exactly what
// domain/cloudidentity.ValidateParams requires, so a binding this form
// produces cannot be one sandbox-agent silently skips at boot.
describe('cloudIdentityParamsComplete', () => {
  it('requires roleArn for aws', () => {
    expect(cloudIdentityParamsComplete('aws', {})).toBe(false)
    expect(cloudIdentityParamsComplete('aws', { roleArn: '   ' })).toBe(false)
    expect(cloudIdentityParamsComplete('aws', { roleArn: 'arn:aws:iam::1:role/r' })).toBe(true)
  })

  it('requires workloadIdentityProvider for gcp', () => {
    expect(cloudIdentityParamsComplete('gcp', {})).toBe(false)
    expect(cloudIdentityParamsComplete('gcp', { workloadIdentityProvider: 'projects/1/x' })).toBe(true)
  })

  it('requires BOTH clientId and tenantId for azure', () => {
    expect(cloudIdentityParamsComplete('azure', { clientId: 'c' })).toBe(false)
    expect(cloudIdentityParamsComplete('azure', { tenantId: 't' })).toBe(false)
    expect(cloudIdentityParamsComplete('azure', { clientId: 'c', tenantId: 't' })).toBe(true)
  })

  it('requires envVar for generic', () => {
    expect(cloudIdentityParamsComplete('generic', {})).toBe(false)
    expect(cloudIdentityParamsComplete('generic', { envVar: 'TOKEN_FILE' })).toBe(true)
  })

  it('refuses an unknown kind rather than defaulting to complete', () => {
    expect(cloudIdentityParamsComplete('nonesuch', { anything: 'x' })).toBe(false)
  })
})

describe('integrationSurfaceLabel', () => {
  it('renders every closed Integration.surface value', () => {
    expect(integrationSurfaceLabel('slack')).toBe('Slack')
    expect(integrationSurfaceLabel('linear')).toBe('Linear')
    expect(integrationSurfaceLabel('github')).toBe('GitHub')
  })
})

// integrationOutboundTone must never read a failed/unknown outbound attempt
// as the SAME tone as a successful one -- §12.5's own "never a health
// verdict" rule still means the three real outcomes must be visually
// distinct from each other, just not collapsed into an implied verdict.
describe('integrationOutboundTone', () => {
  it('gives delivered the ok tone', () => {
    expect(integrationOutboundTone('delivered')).toBe('ok')
  })
  it('gives pending the warn tone', () => {
    expect(integrationOutboundTone('pending')).toBe('warn')
  })
  it('gives dead_letter the crit tone', () => {
    expect(integrationOutboundTone('dead_letter')).toBe('crit')
  })
  it('falls back to neutral for null or an unrecognised value, never a guessed-favorable tone', () => {
    expect(integrationOutboundTone(null)).toBe('neutral')
    expect(integrationOutboundTone('some_future_status')).toBe('neutral')
  })
})

// chatgptLinkStatusPresentation's whole job is keeping needs_relink visually
// DISTINCT from pending: one is "hasn't been confirmed yet" (expected,
// transient), the other is "the refresh pump gave up and this credential is
// no longer served to any sandbox" (a silent degradation with no other
// signal). Collapsing them to the same tone would erase that distinction --
// the terminal refresh-pump failure §29.5 defines would read as an ordinary
// mid-flow wait.
// The case that matters is a failed poll DURING a live device-flow attempt.
// Keying the card on isSuccess meant one transient failure unmounted it,
// taking the verification URL and user code away mid-flow. These four cases
// are the whole contract, and the third is the one a regression would break.
describe('chatgptCardPresentation', () => {
  it('shows only the spinner before anything has loaded', () => {
    expect(chatgptCardPresentation({ isPending: true, isError: false, hasData: false })).toEqual({ card: false, staleNotice: false, loading: true, error: false })
  })

  it('keeps the card up when a poll fails but a status is already known, and says it is stale', () => {
    expect(chatgptCardPresentation({ isPending: false, isError: true, hasData: true })).toEqual({ card: true, staleNotice: true, loading: false, error: false })
  })

  it('shows a bare error only when nothing ever loaded', () => {
    expect(chatgptCardPresentation({ isPending: false, isError: true, hasData: false })).toEqual({ card: false, staleNotice: false, loading: false, error: true })
  })

  it('shows the card, with no stale notice, on a healthy read', () => {
    expect(chatgptCardPresentation({ isPending: false, isError: false, hasData: true })).toEqual({ card: true, staleNotice: false, loading: false, error: false })
  })
})

describe('chatgptLinkStatusPresentation', () => {
  it('marks unlinked neutral', () => {
    expect(chatgptLinkStatusPresentation('unlinked')).toEqual({ tone: 'neutral', label: 'not connected' })
  })
  it('marks pending warn, distinct from needs_relink', () => {
    expect(chatgptLinkStatusPresentation('pending')).toEqual({ tone: 'warn', label: 'verifying' })
  })
  it('marks linked ok', () => {
    expect(chatgptLinkStatusPresentation('linked')).toEqual({ tone: 'ok', label: 'connected' })
  })
  it('marks needs_relink crit -- the refresh pump\'s own terminal-failure signal, never folded into pending\'s warn tone', () => {
    const result = chatgptLinkStatusPresentation('needs_relink')
    expect(result.tone).toBe('crit')
    expect(result.tone).not.toBe(chatgptLinkStatusPresentation('pending').tone)
  })
})

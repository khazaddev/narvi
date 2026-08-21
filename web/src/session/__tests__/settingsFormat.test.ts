import { describe, expect, it } from 'vitest'

import { cloudIdentityBindingSummary, environmentSummaryLine, formatDateTime, identityProviderLabel, lookbackDaysLabel, roleTone, secretScopeLabel, secretScopeTone } from '../settingsFormat'

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

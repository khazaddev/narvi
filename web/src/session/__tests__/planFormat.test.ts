import { describe, expect, it } from 'vitest'

import { canActOnPlan, modelLabel, planStatusLabel, planStatusTone } from '../planFormat'

describe('planStatusTone', () => {
  it('maps every plan_status value to a tone', () => {
    expect(planStatusTone('awaiting_approval')).toBe('warn')
    expect(planStatusTone('approved')).toBe('ok')
    expect(planStatusTone('rejected')).toBe('crit')
    expect(planStatusTone('superseded')).toBe('neutral')
  })
})

describe('planStatusLabel', () => {
  it('maps every plan_status value to a human label', () => {
    expect(planStatusLabel('awaiting_approval')).toBe('awaiting approval')
    expect(planStatusLabel('approved')).toBe('approved')
    expect(planStatusLabel('rejected')).toBe('rejected')
    expect(planStatusLabel('superseded')).toBe('superseded')
  })
})

describe('modelLabel', () => {
  it('renders the real model id when set', () => {
    expect(modelLabel('anthropic/claude-opus-4-8')).toBe('anthropic/claude-opus-4-8')
  })
  it('renders "default" for null, never blank or fabricated', () => {
    expect(modelLabel(null)).toBe('default')
  })
})

// canActOnPlan is this view's own client-side approximation of §13.3's
// real matrix -- the AUTHORIZATION TEST the task brief requires is at the
// component level (planFormat.authorization.test.tsx / the httpapi
// integration suite already proves the real server-side gate); these
// cases prove the pure predicate itself matches the matrix's own stated
// rule exactly, including its own documented conservative-under-approx
// for "joined" (untestable here since it is a client-side data gap, not a
// logic branch this function has).
describe('canActOnPlan', () => {
  it('admin may act on any plan, regardless of ownership', () => {
    expect(canActOnPlan('admin', 'user-a', 'user-b')).toBe(true)
    expect(canActOnPlan('admin', 'user-a', null)).toBe(true)
  })
  it('maintainer may act on any plan, regardless of ownership', () => {
    expect(canActOnPlan('maintainer', 'user-a', 'user-b')).toBe(true)
  })
  it('member may act on a plan they created', () => {
    expect(canActOnPlan('member', 'user-a', 'user-a')).toBe(true)
  })
  it('member may NOT act on a plan created by someone else (the conservative under-approximation -- server also checks "joined", this predicate cannot)', () => {
    expect(canActOnPlan('member', 'user-a', 'user-b')).toBe(false)
  })
  it('member may not act when the session has no direct human creator (bot/automation-created)', () => {
    expect(canActOnPlan('member', 'user-a', null)).toBe(false)
  })
  it('viewer never, even on their own session', () => {
    expect(canActOnPlan('viewer', 'user-a', 'user-a')).toBe(false)
  })
  it('an unknown/undefined role never', () => {
    expect(canActOnPlan(undefined, 'user-a', 'user-a')).toBe(false)
  })
  it('an unresolved caller id (meId undefined -- still loading) never claims ownership', () => {
    expect(canActOnPlan('member', undefined, 'user-a')).toBe(false)
  })
})

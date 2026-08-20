import { describe, expect, it } from 'vitest'

import { formatRelativeTime } from '../relativeTime'

describe('formatRelativeTime', () => {
  const now = new Date('2026-08-20T12:00:00Z')

  it('renders "just now" for under a minute', () => {
    expect(formatRelativeTime('2026-08-20T11:59:30Z', now)).toBe('just now')
  })

  it('renders whole minutes', () => {
    expect(formatRelativeTime('2026-08-20T11:58:00Z', now)).toBe('2 min')
  })

  it('renders whole hours once past 60 minutes', () => {
    expect(formatRelativeTime('2026-08-20T10:00:00Z', now)).toBe('2 h')
  })

  it('renders whole days once past 24 hours', () => {
    expect(formatRelativeTime('2026-08-18T12:00:00Z', now)).toBe('2 d')
  })

  it('never goes negative for a clock-skewed future timestamp', () => {
    expect(formatRelativeTime('2026-08-20T13:00:00Z', now)).toBe('just now')
  })

  it('returns an empty string for an unparseable timestamp rather than "NaN min"', () => {
    expect(formatRelativeTime('not-a-date', now)).toBe('')
  })
})

import { describe, expect, it } from 'vitest'

import { deriveBootProgress, deriveStatusChip, isStillBooting } from '../sessionStatus'

describe('deriveStatusChip', () => {
  it('renders cancelled as neutral (decision 1: "no more Failed badge on a session that was merely stopped")', () => {
    expect(deriveStatusChip({ status: 'cancelled', failureReason: null })).toEqual({ tone: 'neutral', label: 'cancelled' })
  })

  it('renders failed with its persisted reason, never a bare "Failed"', () => {
    expect(deriveStatusChip({ status: 'failed', failureReason: 'timeout' })).toEqual({ tone: 'crit', label: 'failed · timeout' })
  })

  it('renders completed as ok', () => {
    expect(deriveStatusChip({ status: 'completed', failureReason: null })).toEqual({ tone: 'ok', label: 'completed' })
  })

  it('renders an active session with a booting sandboxStatus as "booting n/m"', () => {
    expect(deriveStatusChip({ status: 'active', failureReason: null }, 'booting')).toEqual({ tone: 'warn', label: 'booting 3/4' })
  })

  it('renders an active session with a ready sandboxStatus as "running"', () => {
    expect(deriveStatusChip({ status: 'active', failureReason: null }, 'ready')).toEqual({ tone: 'run', label: 'running' })
  })

  it('renders an active session with sandboxStatus omitted (GetSession never populates it) as the honest, unspecific "active"', () => {
    expect(deriveStatusChip({ status: 'active', failureReason: null })).toEqual({ tone: 'run', label: 'active' })
  })

  it('never crashes on an unrecognized status value', () => {
    expect(() => deriveStatusChip({ status: 'some_future_status' as never, failureReason: null })).not.toThrow()
  })
})

describe('deriveBootProgress', () => {
  it('maps pending/spawning to 1/4 (folded together -- neither is one of the mockup\'s own 4 named nodes on its own), connecting to 2/4, booting to 3/4', () => {
    expect(deriveBootProgress('pending')).toEqual({ index: 1, total: 4 })
    expect(deriveBootProgress('spawning')).toEqual({ index: 1, total: 4 })
    expect(deriveBootProgress('connecting')).toEqual({ index: 2, total: 4 })
    expect(deriveBootProgress('booting')).toEqual({ index: 3, total: 4 })
  })

  it('returns null once ready (boot progress is over)', () => {
    expect(deriveBootProgress('ready')).toBeNull()
  })

  it('returns null for null (no sandbox row yet)', () => {
    expect(deriveBootProgress(null)).toBeNull()
  })

  it('returns null for a non-boot status (snapshotting/suspect/stopped/failed)', () => {
    expect(deriveBootProgress('snapshotting')).toBeNull()
    expect(deriveBootProgress('suspect')).toBeNull()
    expect(deriveBootProgress('stopped')).toBeNull()
    expect(deriveBootProgress('failed')).toBeNull()
  })
})

describe('isStillBooting', () => {
  // Named regression: caught live during this Step's own browser
  // verification pass -- a seeded 'completed' session with zero events
  // rendered "Sandbox is booting…" before this function existed.
  it('is false for a completed/cancelled/failed session with no events -- there is no boot in progress to report', () => {
    expect(isStillBooting('completed', false)).toBe(false)
    expect(isStillBooting('cancelled', false)).toBe(false)
    expect(isStillBooting('failed', false)).toBe(false)
  })

  it('is true for a created/active session that has not seen a ready event yet', () => {
    expect(isStillBooting('created', false)).toBe(true)
    expect(isStillBooting('active', false)).toBe(true)
  })

  it('is false once a ready event has been seen, even for an active session', () => {
    expect(isStillBooting('active', true)).toBe(false)
  })
})

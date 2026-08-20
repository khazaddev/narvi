import { describe, expect, it } from 'vitest'

import { parseSandboxSnapshot } from '../sandboxSnapshot'

describe('parseSandboxSnapshot', () => {
  it('parses a well-formed snapshot (client.go\'s own sandboxWireMap shape)', () => {
    const raw = { id: 'sb-1', gen: 3, status: 'ready', lastSeenAt: '2026-08-20T10:00:02Z', createdAt: '2026-08-20T09:58:00Z', updatedAt: '2026-08-20T10:00:02Z' }
    expect(parseSandboxSnapshot(raw)).toEqual(raw)
  })

  it('accepts a null lastSeenAt (never yet reported a heartbeat)', () => {
    const raw = { id: 'sb-1', gen: 0, status: 'pending', lastSeenAt: null, createdAt: 'x', updatedAt: 'x' }
    expect(parseSandboxSnapshot(raw)).toEqual(raw)
  })

  it('returns null for a missing sandbox (no sandbox row yet)', () => {
    expect(parseSandboxSnapshot(null)).toBeNull()
    expect(parseSandboxSnapshot(undefined)).toBeNull()
  })

  it('returns null for a non-object', () => {
    expect(parseSandboxSnapshot('not an object')).toBeNull()
    expect(parseSandboxSnapshot(42)).toBeNull()
    expect(parseSandboxSnapshot([])).toBeNull()
  })

  it('returns null when a required field has the wrong type -- never coerces', () => {
    expect(parseSandboxSnapshot({ id: 'sb-1', gen: '3', status: 'ready', lastSeenAt: null, createdAt: 'x', updatedAt: 'x' })).toBeNull()
    expect(parseSandboxSnapshot({ id: 'sb-1', gen: 3, status: 42, lastSeenAt: null, createdAt: 'x', updatedAt: 'x' })).toBeNull()
    expect(parseSandboxSnapshot({ id: 'sb-1', gen: 3, status: 'ready', lastSeenAt: 42, createdAt: 'x', updatedAt: 'x' })).toBeNull()
  })

  it('returns null when a required field is missing entirely', () => {
    expect(parseSandboxSnapshot({ gen: 3, status: 'ready', lastSeenAt: null, createdAt: 'x', updatedAt: 'x' })).toBeNull()
  })
})

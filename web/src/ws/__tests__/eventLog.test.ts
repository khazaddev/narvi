import { describe, expect, it } from 'vitest'

import { EventLog } from '../eventLog'
import type { EventEnvelope } from '../types'

function event(id: number, type = 'tool_call'): EventEnvelope {
  return { id, type, payload: { id }, createdAt: `2026-01-01T00:00:${String(id).padStart(2, '0')}Z` }
}

describe('EventLog', () => {
  it('append returns true for a fresh id and false for a repeat -- the dedup guard sessionStream.ts relies on', () => {
    const log = new EventLog()
    expect(log.append(event(1))).toBe(true)
    expect(log.append(event(1))).toBe(false)
    expect(log.size).toBe(1)
  })

  it('appendMany returns only the subset that was newly inserted, e.g. a resent replay overlapping already-known events', () => {
    const log = new EventLog()
    log.appendMany([event(1), event(2)])
    const inserted = log.appendMany([event(1), event(2), event(3)])
    expect(inserted.map((e) => e.id)).toEqual([3])
    expect(log.size).toBe(3)
  })

  it('keeps entries() id-sorted regardless of insertion order (a backfill page can insert behind the current high-water mark)', () => {
    const log = new EventLog()
    log.append(event(5))
    log.append(event(1))
    log.append(event(3))
    log.append(event(2))
    log.append(event(4))
    expect(log.entries().map((e) => e.id)).toEqual([1, 2, 3, 4, 5])
  })

  it('highestId reflects the greatest id seen, independent of insertion order, and is null when empty', () => {
    const log = new EventLog()
    expect(log.highestId()).toBeNull()
    log.append(event(5))
    log.append(event(2))
    expect(log.highestId()).toBe(5)
  })

  it('has() reports membership by id', () => {
    const log = new EventLog()
    log.append(event(7))
    expect(log.has(7)).toBe(true)
    expect(log.has(8)).toBe(false)
  })

  it('reset clears the log entirely', () => {
    const log = new EventLog()
    log.appendMany([event(1), event(2)])
    log.reset()
    expect(log.size).toBe(0)
    expect(log.entries()).toEqual([])
    expect(log.highestId()).toBeNull()
  })

  it('entries() returns a fresh array each call -- mutating the returned array must not corrupt the log', () => {
    const log = new EventLog()
    log.append(event(1))
    const first = log.entries()
    first.push(event(99))
    expect(log.entries().map((e) => e.id)).toEqual([1])
  })
})

import { describe, expect, it } from 'vitest'

import { initialSessionActivityState, reduceEvent, reduceLog } from '../reducer'
import type { EventEnvelope } from '../types'

function event(id: number, type: string, createdAt: string): EventEnvelope {
  return { id, type, payload: {}, createdAt }
}

describe('reduceEvent', () => {
  it('folds one event: increments eventCount, bumps its own type, advances lastEventId/lastEventAt', () => {
    const next = reduceEvent(initialSessionActivityState, event(1, 'tool_call', '2026-01-01T00:00:01Z'))
    expect(next).toEqual({
      eventCount: 1,
      countsByType: { tool_call: 1 },
      lastEventId: 1,
      lastEventAt: '2026-01-01T00:00:01Z',
    })
  })
})

describe('reduceLog', () => {
  it('folds an id-ordered array into per-type counts and a running total', () => {
    const state = reduceLog([
      event(1, 'tool_call', '2026-01-01T00:00:01Z'),
      event(2, 'tool_call', '2026-01-01T00:00:02Z'),
      event(3, 'execution_complete', '2026-01-01T00:00:03Z'),
    ])
    expect(state.eventCount).toBe(3)
    expect(state.countsByType).toEqual({ tool_call: 2, execution_complete: 1 })
    expect(state.lastEventId).toBe(3)
    expect(state.lastEventAt).toBe('2026-01-01T00:00:03Z')
  })

  it('assumes id order is causal order: folding by id order gives a different (correct) result than folding by an arbitrary arrival order would', () => {
    // Event 2 is CAUSALLY later than event 1 (higher id) but, in this
    // fixture, has an EARLIER wall-clock createdAt than a naive
    // "assume the timestamp orders things" reducer might expect --
    // deliberately, so this test cannot pass by accident via timestamp
    // ordering happening to agree with id ordering. reduceLog must still
    // treat id 2 as the causally-latest event because EventLog (eventLog.ts)
    // guarantees its entries() array is always id-sorted, and reduceLog
    // trusts exactly that ordering, never arrival order -- see reducer.ts's
    // own top comment.
    const idOrdered: EventEnvelope[] = [event(1, 'tool_call', '2026-01-01T00:00:09Z'), event(2, 'execution_complete', '2026-01-01T00:00:01Z')]
    const state = reduceLog(idOrdered)
    expect(state.lastEventId).toBe(2)
    expect(state.lastEventAt).toBe('2026-01-01T00:00:01Z')

    // Feeding the SAME two events in the opposite (non-id, "arrival")
    // order produces a DIFFERENT final state -- proof reduceLog is order-
    // sensitive, not merely order-independent-by-luck on this fixture. The
    // pipeline's own correctness therefore rests entirely on EventLog
    // always handing reduceLog its id-sorted view (sessionStream.test.ts's
    // own out-of-order-arrival test pins that half of the guarantee).
    const arrivalOrdered: EventEnvelope[] = [idOrdered[1], idOrdered[0]]
    const reversedState = reduceLog(arrivalOrdered)
    expect(reversedState.lastEventId).toBe(1)
    expect(reversedState).not.toEqual(state)
  })

  it('empty log folds to the initial state', () => {
    expect(reduceLog([])).toEqual(initialSessionActivityState)
  })
})

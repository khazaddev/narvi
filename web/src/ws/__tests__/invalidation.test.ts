import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'

import { planQueryKeys, reviewQueryKeys, sessionQueryKeys } from '../../api/queryKeys'
import { invalidateForEvents, queryKeysForEvent } from '../invalidation'
import type { EventEnvelope } from '../types'

function event(id: number, type: string): EventEnvelope {
  return { id, type, payload: {}, createdAt: '2026-01-01T00:00:00Z' }
}

describe('queryKeysForEvent', () => {
  it('maps a known event type (tool_call) to the session events key', () => {
    expect(queryKeysForEvent('s1', event(1, 'tool_call'))).toEqual([sessionQueryKeys.events('s1')])
  })

  it('maps execution_complete to the session detail/events keys plus the review readout/release-manifest/plan-list keys (a verdict or new plan version may have posted mid-turn)', () => {
    expect(queryKeysForEvent('s1', event(1, 'execution_complete'))).toEqual([
      sessionQueryKeys.detail('s1'),
      sessionQueryKeys.events('s1'),
      reviewQueryKeys.readout('s1'),
      reviewQueryKeys.releaseManifest('s1'),
      planQueryKeys.list('s1'),
    ])
  })

  it('falls back to the events key for an unrecognized type -- never "invalidate nothing"', () => {
    expect(queryKeysForEvent('s1', event(1, 'some_future_event_type'))).toEqual([sessionQueryKeys.events('s1')])
  })
})

describe('invalidateForEvents', () => {
  it('invalidates the query client with the de-duplicated union of keys for the given events -- the mechanism the pipeline (sessionStream.ts) relies on to turn a newly-folded event into a stale query', () => {
    const queryClient = new QueryClient()
    const spy = vi.spyOn(queryClient, 'invalidateQueries')

    invalidateForEvents(queryClient, 's1', [event(1, 'tool_call'), event(2, 'tool_call'), event(3, 'execution_complete')])

    const invalidatedKeys = spy.mock.calls.map((call) => call[0]?.queryKey)
    expect(invalidatedKeys).toContainEqual(sessionQueryKeys.events('s1'))
    expect(invalidatedKeys).toContainEqual(sessionQueryKeys.detail('s1'))
    expect(invalidatedKeys).toContainEqual(reviewQueryKeys.readout('s1'))
    expect(invalidatedKeys).toContainEqual(reviewQueryKeys.releaseManifest('s1'))
    expect(invalidatedKeys).toContainEqual(planQueryKeys.list('s1'))
    // tool_call's own key (events) is only requested ONCE across both
    // tool_call events, even though two separate events asked for it --
    // de-duplicated, not one invalidateQueries call per event. The union
    // across tool_call (events) and execution_complete (detail, events,
    // readout, releaseManifest, plan list) is 5 unique keys.
    expect(spy).toHaveBeenCalledTimes(5)
  })

  it('does nothing for an empty event list', () => {
    const queryClient = new QueryClient()
    const spy = vi.spyOn(queryClient, 'invalidateQueries')
    invalidateForEvents(queryClient, 's1', [])
    expect(spy).not.toHaveBeenCalled()
  })
})

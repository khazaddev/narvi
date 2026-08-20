import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { installUnauthorizedHandler } from '../session'
import { requireAuth } from '../requireAuth'

// These tests use a REAL QueryClient on purpose.
//
// The defect they pin shipped precisely because the existing guard tests stub
// the client (`{ ensureQueryData } as unknown as QueryClient`), so real cache
// semantics were never exercised. A stub cannot express the thing that was
// wrong: ensureQueryData short-circuits on any cached data and consults
// neither staleness nor invalidation, so a signed-out visitor kept passing the
// guard on the retained Member. Stubbing the very object whose behaviour is
// the subject is how a guard gets a green test and no protection.

afterEach(() => {
  vi.unstubAllGlobals()
})

const meResponse = {
  id: 'user-1',
  email: 'ada.example@example.test',
  displayName: 'Ada Example',
  role: 'member',
  disabled: false,
  createdAt: '2026-01-01T00:00:00Z',
  identities: [],
}

/** Answers the first call with a signed-in Member, every later call with 401. */
function fetchSignedInThenRevoked(): { calls: () => number } {
  let n = 0
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => {
      n += 1
      return n === 1
        ? new Response(JSON.stringify(meResponse), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
        : new Response(JSON.stringify({ error: 'unauthorized' }), {
            status: 401,
            headers: { 'content-type': 'application/json' },
          })
    }),
  )
  return { calls: () => n }
}

function guardArgs(queryClient: QueryClient) {
  return { context: { queryClient }, location: { pathname: '/' } }
}

describe('requireAuth against a real query cache', () => {
  it('redirects once the session is revoked, instead of passing on the retained profile', async () => {
    const queryClient = new QueryClient()
    installUnauthorizedHandler(queryClient)
    const fetches = fetchSignedInThenRevoked()

    // First navigation: a real session, the guard passes and the Member lands
    // in the cache.
    await expect(requireAuth(guardArgs(queryClient))).resolves.toBeUndefined()
    expect(queryClient.getQueryData(['auth', 'me'])).toBeDefined()

    // The session is revoked server-side. Something reads /api/me, gets a 401,
    // and the unauthorized hook invalidates the entry.
    await queryClient.invalidateQueries({ queryKey: ['auth', 'me'], refetchType: 'none' })

    // The next guarded navigation must NOT resolve on the cached Member.
    // TanStack redirects are thrown, so any throw is the guard doing its job;
    // resolving is the defect.
    await expect(requireAuth(guardArgs(queryClient))).rejects.toBeDefined()
    expect(fetches.calls()).toBeGreaterThan(1)
  })

  it('still serves a genuinely fresh entry from cache, so rapid navigation costs no extra request', async () => {
    const queryClient = new QueryClient()
    const fetches = fetchSignedInThenRevoked()

    await expect(requireAuth(guardArgs(queryClient))).resolves.toBeUndefined()
    // No invalidation between the two: inside staleTime the guard must reuse
    // the cached answer rather than re-asking on every navigation.
    await expect(requireAuth(guardArgs(queryClient))).resolves.toBeUndefined()

    expect(fetches.calls()).toBe(1)
  })
})

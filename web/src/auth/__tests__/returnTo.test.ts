// Proves auth/returnTo.ts's own isSafeReturnTo -- the client-side half of
// Step 81's "redirect handling" (defense in depth; the backend's
// internal/adapters/inbound/auth/login.go isSafeRedirectNext is the real
// authority and is proved separately by that package's own Go tests).
import { describe, expect, it } from 'vitest'

import { isSafeReturnTo, KNOWN_RETURN_TO_ROUTES } from '../returnTo'

describe('isSafeReturnTo', () => {
  it('accepts every route in the known allowlist', () => {
    for (const route of KNOWN_RETURN_TO_ROUTES) {
      expect(isSafeReturnTo(route)).toBe(true)
    }
  })

  it('rejects an off-origin absolute URL', () => {
    expect(isSafeReturnTo('https://evil.example.test/')).toBe(false)
  })

  it('rejects a scheme-relative URL (the classic "//evil.example.test" open-redirect vector)', () => {
    expect(isSafeReturnTo('//evil.example.test/')).toBe(false)
  })

  it('rejects a backslash-prefixed variant some browsers still treat as scheme-relative', () => {
    expect(isSafeReturnTo('/\\evil.example.test/')).toBe(false)
  })

  it('rejects an empty string', () => {
    expect(isSafeReturnTo('')).toBe(false)
  })

  // This is THE mutation-test proof named in the Step's own verification
  // list: isSafeReturnTo must be an ALLOWLIST-OF-ROUTES check, never a
  // "starts with one slash" substring/prefix check -- a same-origin path
  // requirement alone still accepts any string merely SHAPED like a path,
  // real route or not. A path that satisfies that weaker, prefix-only
  // property but names no route this app actually has must still be
  // rejected here.
  it('rejects a same-origin-shaped path that is not a real, known route (proves this is a route allowlist, not a "starts with /" substring check)', () => {
    expect(isSafeReturnTo('/not-a-real-route')).toBe(false)
    expect(isSafeReturnTo('/sign-in/../../etc/passwd')).toBe(false)
    // A known route as a PREFIX of a longer path is still not an exact
    // match -- "/sign-in-evil" starts with "/sign-in" as a substring, but
    // is not "/sign-in" itself.
    expect(isSafeReturnTo('/sign-in-evil')).toBe(false)
  })
})

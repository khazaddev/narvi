// requireAuth.ts (Step 81, "an unauthenticated visitor hitting a deep
// link should land somewhere sensible rather than a blank shell") is a
// `beforeLoad` guard any route can attach to require a valid session --
// see routes/index.tsx for this codebase's first real use of it (gating
// the boot-screen placeholder route "/", the one deep link that exists
// today).
//
// Deliberately does NOT gate on a client-side role check (there is no
// role check here at all, only "is there a valid session"): §13.3's own
// rule is that role/permission enforcement is a server-side-only
// authority (domain/authz.Authorize, every state-changing REST handler)
// -- a route guard hiding a button or a screen from a role that
// shouldn't see it is a UX nicety this file does not even attempt yet
// (no route built so far needs one); it would never be a substitute for
// the real check regardless.
import { redirect } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'

import { meQueryOptions, isSignedOut } from './session'
import { isSafeReturnTo } from './returnTo'

export interface RequireAuthArgs {
  context: { queryClient: QueryClient }
  location: { pathname: string }
}

/**
 * requireAuth ensures a valid session exists before a route's own
 * loader/component runs, via queryClient.ensureQueryData (so a route
 * guard's own check and the sign-in view's own useQuery(meQueryOptions)
 * share ONE cache entry/request, never two independent round-trips).
 *
 * - No session (a 401 ApiError, isSignedOut) -> redirect to /sign-in,
 *   carrying the CURRENT location as `next` ONLY when isSafeReturnTo
 *   accepts it (every route this guard is ever attached to already IS a
 *   known route by construction, so this should always pass in
 *   practice -- still checked here, never assumed, exactly like
 *   sign-in.tsx's own two call sites for the same function).
 * - A genuine failure (network error, 500) -> rethrown, letting
 *   TanStack Router's own error boundary handle it as a loader error --
 *   never silently treated as "not signed in" (that would be a
 *   confusing failure mode: a visitor with a perfectly valid session
 *   bounced to the sign-in page by a transient backend hiccup).
 * - A valid session -> resolves, the guarded route proceeds normally.
 */
export async function requireAuth({ context, location }: RequireAuthArgs): Promise<void> {
  try {
    await context.queryClient.ensureQueryData(meQueryOptions)
  } catch (err) {
    if (!isSignedOut(err)) {
      throw err
    }
    throw redirect({
      to: '/sign-in',
      search: isSafeReturnTo(location.pathname) ? { next: location.pathname } : {},
    })
  }
}

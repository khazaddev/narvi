// returnTo.ts (Step 81, "redirect handling") validates a post-sign-in
// destination BEFORE it is ever placed in a URL this app constructs (a
// <Link>/redirect() target, or the `next` query param handed to
// GET /auth/github/login). This is client-side defense in depth, not the
// authority: internal/adapters/inbound/auth/login.go's own
// isSafeRedirectNext already re-validates server-side (and
// callback.go re-checks it AGAIN before actually redirecting), so a
// forged/tampered value reaching the backend directly is still refused
// there regardless of what this file does. What this file is FOR is
// picking which client-side navigation target this app itself ever
// offers a visitor in the first place (the `next` search param a route
// guard attaches when it redirects an unauthenticated visitor to
// /sign-in) -- it must never construct one from attacker-influenced
// input without checking it here first.
//
// Deliberately an ALLOWLIST OF KNOWN ROUTES, not a "starts with one slash,
// not two" format check (which is exactly what login.go's own
// isSafeRedirectNext already does, server-side): a same-origin PATH
// requirement alone still accepts any string shaped like a path, real
// route or not, which is a weaker property than "is this route this
// app actually has and would sensibly land a visitor back on." A
// substring/prefix check (`path.startsWith('/')`) is exactly the
// weakened form this file's own tests prove insufficient -- see
// __tests__/returnTo.test.ts's own mutation-test block.
const KNOWN_RETURN_TO_ROUTES: readonly string[] = [
  '/',
  '/sign-in',
]

/**
 * isSafeReturnTo reports whether path is one of this app's own known,
 * top-level route paths -- an EXACT match against KNOWN_RETURN_TO_ROUTES
 * above, never a prefix/substring test. A future Step that adds a real
 * route (e.g. a session workspace at "/sessions/$sessionId") extends this
 * list (or, for a genuinely parameterized route, adds a narrow,
 * anchored-regex entry alongside it) -- silently falling through to "deny"
 * for anything not yet listed is the safe default in the meantime, not a
 * bug to route around with a looser check.
 */
export function isSafeReturnTo(path: string): boolean {
  return KNOWN_RETURN_TO_ROUTES.includes(path)
}

export { KNOWN_RETURN_TO_ROUTES }

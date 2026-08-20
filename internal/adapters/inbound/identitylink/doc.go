// Package identitylink is the inbound HTTP adapter backing §13.2 step 4's
// own magic link ("connect your account") -- §13.2's ("identities +
// full RBAC") second half. One route, mounted OUTSIDE auth.Middleware
// entirely (cmd/control-plane/main.go):
//
//	GET /auth/identity-link/{nonce}
//
// This CANNOT sit behind auth.Middleware the way the /api/sessions group
// does: that middleware's own contract is "authenticated or a bare 401
// JSON body" (§13.3: "HTTP middleware handles the coarse route-level
// gate"), but a visitor clicking a magic link from Slack/Linear is very
// likely NOT signed in yet at all -- the right response to that is a
// REDIRECT into the existing GitHub OAuth web-login flow (internal/
// adapters/inbound/auth), not a 401 a browser can't recover from on its
// own. So this handler calls auth.Authenticate directly (the same 4-step
// check Middleware itself performs, extracted for exactly this second
// caller -- see that function's own doc comment) and branches on the
// result itself:
//
//   - Not authenticated: 302 to /auth/github/login?next=<this same URL>
//     (auth.NewLoginHandler's own ?next= addition, §13.2) -- once the
//     visitor completes that REAL GitHub OAuth round trip,
//     NewCallbackHandler lands them right back on this exact URL,
//     narvi_auth_session cookie now set, and this handler runs again,
//     this time authenticated.
//   - Authenticated: internal/app/identitylink.Consume validates the
//     nonce (exists, not expired) and links the identity (linked_via=
//     "prompt", user_id = the now-authenticated visitor), inside one
//     transaction alongside its own audit-log entry -- see that
//     function's own doc comment for the complete behavior, including
//     ErrLinkPromptNotFound/ErrLinkPromptExpired/ErrIdentityAlreadyLinked,
//     each rendered as its own distinct, honest outcome page below (never
//     collapsed into one generic error the way auth.Middleware's own
//     rejection responses deliberately are -- there is no CSRF-adjacent
//     enumeration risk here worth hiding behind a generic body, and a
//     real person just clicked a real link and deserves to know why it
//     didn't work).
//
// Response bodies are minimal, dependency-free HTML (no template engine,
// no external asset, matching this codebase's own "no UI framework
// exists yet" reality outside the Phase 6/7 mockups) -- just enough for a
// human who just clicked a link in Slack/Linear to read a clear outcome
// in their browser tab. This is NOT the Phase 7 Settings -> Members UI
// (still out of scope, mocked only in docs/design/mockups.html); it is
// the smallest real page this one, narrow, one-shot flow needs.
package identitylink

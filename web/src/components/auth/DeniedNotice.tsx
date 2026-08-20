// DeniedNotice (Step 81, "honest allowlist/denial states") renders the
// ONE thing an allowlist-rejected visitor is told: they are not
// permitted, full stop. It takes NO props at all, by design -- the
// classic failure mode this component exists to make structurally
// impossible is a well-meaning future edit threading a backend-supplied
// "detail"/"reason" string through to display ("closer to what actually
// happened", the reasoning always sounds like) -- which is exactly
// internal/adapters/inbound/auth/callback.go's own OutcomeFirstTimeDenied
// branch's point: "does not say WHICH of the 3 mechanisms almost
// matched -- that's enumeration information an attacker could use to
// probe the allowlist's own configuration" (that file's own comment,
// verbatim). A component with no input channel for that string cannot
// leak it no matter what later touches its call site; see
// __tests__/deniedNotice.test.tsx's own mutation-test block for the
// proof this is checked, not just asserted here.
//
// # Why this is reachable today only as inert groundwork
//
// The real GitHub-OAuth denial path (callback.go's OutcomeFirstTimeDenied/
// OutcomeNoVerifiedEmail) is a FULL top-level browser navigation straight
// to /auth/github/callback (GitHub itself redirects the tab there) that
// responds with net/http.Error's own plain-text 403 body -- this never
// routes through the SPA/React bundle at all, so this component cannot
// intercept that real rejection today. It is wired into the sign-in
// route (routes/sign-in.tsx) behind a `denied` search param anyway, both
// because §12.2 item 7 names "allowlist errors" as this Step's own
// content to build and because a concrete, tested rendering path is more
// honest groundwork than an unbuilt one -- see sign-in.tsx's own doc
// comment for the explicit "nothing sets this today" note repeated at
// its actual call site.
export function DeniedNotice() {
  return (
    <div className="signin-notice signin-notice-denied" role="alert">
      <p>Your account is not permitted to sign in.</p>
      <p className="signin-notice-sub">
        Access is limited to allowed email domains and GitHub organizations. If you believe this is a mistake, ask
        your workspace admin to add you.
      </p>
    </div>
  )
}

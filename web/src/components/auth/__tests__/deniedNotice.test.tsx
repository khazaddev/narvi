// Proves DeniedNotice renders the ONE fixed, honest, non-leaking message
// -- "a denied response renders the honest message and does not echo
// back allowlist contents" -- §13.1's own requirement. Uses
// react-dom/server's renderToStaticMarkup, not a jsdom-based renderer:
// this is a static string-rendering proof, no interaction/hooks needed,
// and adds no new dependency (react-dom is already this app's own
// dependency).
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import { DeniedNotice } from '../DeniedNotice'

describe('DeniedNotice', () => {
  it('renders the fixed, generic denial message', () => {
    const html = renderToStaticMarkup(<DeniedNotice />)
    expect(html).toContain('Your account is not permitted to sign in.')
  })

  it('never contains a concrete leaked value (a specific domain, org name, or email address)', () => {
    const html = renderToStaticMarkup(<DeniedNotice />)
    // The fixed copy below describes the MECHANISM categories generically
    // ("allowed email domains", "GitHub organizations") -- the same way
    // the mockup's own static .allow note does for every visitor,
    // signed-in or not -- but never a concrete value: no email address
    // (no "@"), and no domain-like token (word.tld). internal/adapters/
    // inbound/auth's own allowlist.go (EmailDomains/GitHubOrgs/Emails)
    // is never read by this component -- it takes no props carrying that
    // data at all, so there is nothing here TO leak by construction; this
    // assertion is the proof, not just the design.
    expect(html).not.toContain('@')
    // Any dot-joined token with no whitespace around the dot (domain- or
    // hostname-shaped, e.g. "narvi.example" or "not-a-real-org.io") --
    // broader than a fixed TLD allowlist so a fixture using a DIFFERENT
    // reserved/example TLD still trips this check.
    expect(html).not.toMatch(/\b[a-z0-9-]+\.[a-z]{2,}\b/i)
  })

  // Named mutation-test target (§13.1's own requirement): make the
  // denial message echo the backend's raw detail -> this test must
  // fail. Performed manually during that verification pass
  // (temporarily threading a fake "detail" prop through and
  // interpolating it here, confirming THIS test goes red, then
  // reverting byte-identical) -- not encoded as a permanent toggle in
  // this file, since DeniedNotice deliberately accepts no such prop at
  // all in its real, shipped form.
  it('renders identically regardless of what a caller might try to pass (component takes no props)', () => {
    const html = renderToStaticMarkup(<DeniedNotice />)
    expect(html).toBe(
      '<div class="signin-notice signin-notice-denied" role="alert"><p>Your account is not permitted to sign in.</p><p class="signin-notice-sub">Access is limited to allowed email domains and GitHub organizations. If you believe this is a mistake, ask your workspace admin to add you.</p></div>',
    )
  })
})

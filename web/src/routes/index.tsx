import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'

// Placeholder boot screen -- proves the SPA actually boots (routing, theme,
// embed) with none of the nine real views (§12.2) built yet. Replaced by
// the decision-inbox home view later, not extended in place.
//
// beforeLoad: requireAuth (Step 81) -- "/" is this app's only real deep
// link today, and Step 87's own decision-inbox home view that eventually
// replaces this placeholder is unambiguously an authenticated-only
// screen, so gating it now (rather than leaving it open until that Step
// remembers to) is the safe default: an unauthenticated visitor hitting
// "/" lands on /sign-in?next=%2F instead of this shell, and a signed-in
// one sees the placeholder exactly as before.
export const Route = createFileRoute('/')({
  beforeLoad: requireAuth,
  component: BootScreen,
})

function BootScreen() {
  return (
    <div className="boot-screen">
      <p className="eyebrow">ui bootstrap</p>
      <h1>Narvi control plane</h1>
      <p className="body">
        This is the application shell: Vite + React + TanStack Query/Router, built as a static
        bundle and served by the control-plane binary on the same port as its API and WebSocket
        traffic. The full set of views ships next.
      </p>
    </div>
  )
}

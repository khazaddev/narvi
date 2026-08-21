import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { DecisionInboxView } from '../session/DecisionInboxView'
import '../styles/session.css'

// The decision-inbox home view (§16, decisions 32-34) -- the placeholder
// BootScreen this file used to render is replaced outright, per that
// placeholder's own doc comment ("Replaced by the decision-inbox home
// view later, not extended in place"), rather than grown into this. The
// sessions list -- previously reachable only via a session's own sidebar
// -- moves to its own top-level route (routes/sessions.tsx,
// docs/IMPLEMENTATION_PLAN.md row 87).
//
// beforeLoad: requireAuth (§13.1) -- unchanged from the placeholder: "/"
// is an authenticated-only screen (the inbox is inherently "YOUR pending
// decisions", §16.1), so an unauthenticated visitor still lands on
// /sign-in?next=%2F instead of this view.
export const Route = createFileRoute('/')({
  beforeLoad: requireAuth,
  component: DecisionInboxView,
})

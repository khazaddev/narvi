// The sessions list view (docs/IMPLEMENTATION_PLAN.md row 87: "the
// sessions list moves to a second tab") -- /sessions, a top-level route
// mirroring automations.tsx/settings.tsx's own precedent (a screen owned
// by no single session).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { SessionsListView } from '../session/SessionsListView'
import '../styles/session.css'

export const Route = createFileRoute('/sessions')({
  beforeLoad: requireAuth,
  component: SessionsListView,
})

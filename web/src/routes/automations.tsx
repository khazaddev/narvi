// The automations view (§12.2 item 4) -- /automations, a top-level route
// (not session-scoped, unlike the session-workspace views shipped before
// it) since an automation is not owned by any one session.
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { AutomationsView } from '../session/AutomationsView'
import '../styles/session.css'

export const Route = createFileRoute('/automations')({
  beforeLoad: requireAuth,
  component: AutomationsView,
})

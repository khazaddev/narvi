// The plan-mode view (§12.2 item 3) -- a standalone view at
// /session/$sessionId/plan, sibling to (not nested inside) the ordinary
// session workspace ($sessionId.tsx), mirroring $sessionId_.review.tsx's
// own routing precedent (requireAuth guard, singular "/session/..." path).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../../auth/requireAuth'
import { PlanModeView } from '../../session/PlanModeView'
import '../../styles/session.css'

export const Route = createFileRoute('/session/$sessionId_/plan')({
  beforeLoad: requireAuth,
  component: RouteComponent,
})

function RouteComponent() {
  const { sessionId } = Route.useParams()
  return <PlanModeView sessionId={sessionId} />
}

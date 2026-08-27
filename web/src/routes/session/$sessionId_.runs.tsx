// The workflow run view + human decision gate (§25.9/§25.10) -- a
// standalone view at /session/$sessionId/runs, sibling to (not nested
// inside) the ordinary session workspace ($sessionId.tsx), mirroring
// $sessionId_.plan.tsx's own routing precedent exactly (requireAuth guard,
// singular "/session/..." path).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../../auth/requireAuth'
import { WorkflowRunsView } from '../../session/WorkflowRunsView'
import '../../styles/session.css'

export const Route = createFileRoute('/session/$sessionId_/runs')({
  beforeLoad: requireAuth,
  component: RouteComponent,
})

function RouteComponent() {
  const { sessionId } = Route.useParams()
  return <WorkflowRunsView sessionId={sessionId} />
}

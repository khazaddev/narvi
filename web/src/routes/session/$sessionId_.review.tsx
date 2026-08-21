// The code-review view (§26.1's merge readout, §12.2 item 2) -- a
// standalone view at /session/$sessionId/review, sibling to (not nested
// inside) the ordinary session workspace ($sessionId.tsx), mirroring that
// file's own routing precedent (requireAuth guard, singular
// "/session/..." path).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../../auth/requireAuth'
import { CodeReviewView } from '../../session/CodeReviewView'
import '../../styles/session.css'

export const Route = createFileRoute('/session/$sessionId_/review')({
  beforeLoad: requireAuth,
  component: RouteComponent,
})

function RouteComponent() {
  const { sessionId } = Route.useParams()
  return <CodeReviewView sessionId={sessionId} />
}

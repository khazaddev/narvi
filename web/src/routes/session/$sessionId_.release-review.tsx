// The release-review view (§15, §12.2 item 9) -- a standalone view at
// /session/$sessionId/release-review, mirroring $sessionId.review.tsx's
// own identical routing precedent.
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../../auth/requireAuth'
import { ReleaseReviewView } from '../../session/ReleaseReviewView'
import '../../styles/session.css'

export const Route = createFileRoute('/session/$sessionId_/release-review')({
  beforeLoad: requireAuth,
  component: RouteComponent,
})

function RouteComponent() {
  const { sessionId } = Route.useParams()
  return <ReleaseReviewView sessionId={sessionId} />
}

// The Analytics view (§12.2 items 5-6) -- /analytics, a top-level route
// (mirrors automations.tsx's own precedent: analytics belong to no
// single session).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { AnalyticsView } from '../session/AnalyticsView'
import '../styles/session.css'

export const Route = createFileRoute('/analytics')({
  beforeLoad: requireAuth,
  component: AnalyticsView,
})

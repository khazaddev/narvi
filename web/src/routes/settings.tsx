// The Settings view (§12.2 item 5) -- /settings, a top-level route
// (mirrors automations.tsx's own precedent: settings belong to no single
// session).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { SettingsView } from '../session/SettingsView'
import '../styles/session.css'

export const Route = createFileRoute('/settings')({
  beforeLoad: requireAuth,
  component: SettingsView,
})

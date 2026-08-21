// The per-repository settings view (§21, §26.7, §26.8, §4.1.2) --
// /repo-settings, a top-level route (mirrors settings.tsx/analytics.tsx's
// own precedent: this screen is scoped to whichever repository the
// operator types in, never to a single session).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { RepoSettingsView } from '../session/RepoSettingsView'
import '../styles/session.css'

export const Route = createFileRoute('/repo-settings')({
  beforeLoad: requireAuth,
  component: RepoSettingsView,
})

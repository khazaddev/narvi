// The workflow canvas editor (§25.12) -- /workflows, a top-level route
// (mirrors automations.tsx/settings.tsx's own precedent: a workflow
// definition belongs to a lane/repo, never to a single session).
import { createFileRoute } from '@tanstack/react-router'

import { requireAuth } from '../auth/requireAuth'
import { WorkflowEditorView } from '../session/WorkflowEditorView'
import '../styles/session.css'

export const Route = createFileRoute('/workflows')({
  beforeLoad: requireAuth,
  component: WorkflowEditorView,
})

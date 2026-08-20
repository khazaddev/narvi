import { createFileRoute } from '@tanstack/react-router'

// Placeholder boot screen -- proves the SPA actually boots (routing, theme,
// embed) with none of the nine real views (§12.2) built yet. Replaced by
// the decision-inbox home view later, not extended in place.
export const Route = createFileRoute('/')({
  component: BootScreen,
})

function BootScreen() {
  return (
    <div className="boot-screen">
      <p className="eyebrow">ui bootstrap</p>
      <h1>Narvi control plane</h1>
      <p className="body">
        This is the application shell: Vite + React + TanStack Query/Router, built as a static
        bundle and served by the control-plane binary on the same port as its API and WebSocket
        traffic. The full set of views ships next.
      </p>
    </div>
  )
}

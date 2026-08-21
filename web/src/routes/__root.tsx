import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Link, Outlet } from '@tanstack/react-router'

import { ThemeToggle } from '../components/ThemeToggle'

// RouterContext (§13.1) -- queryClient is the one thing every
// route's own beforeLoad guard needs to share the SAME TanStack Query
// cache main.tsx's QueryClientProvider already renders with (auth/
// requireAuth.ts's own queryClient.ensureQueryData(meQueryOptions) call),
// rather than each guard constructing its own client or reaching for a
// module-level singleton. Nothing else lives in router context yet.
export interface RouterContext {
  queryClient: QueryClient
}

// Root layout (§12.1): the one header every real view is expected to
// share. TanStack Router renders this once and swaps <Outlet/>'s contents
// per route -- the index and sign-in routes are the only children that
// exist yet.
export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
})

function RootLayout() {
  return (
    <div className="app-shell">
      <header className="app-header">
        <span className="wordmark">Narvi</span>
        {/* The real top nav (§16): Home is the decision-inbox queue
            (routes/index.tsx); Sessions is its own second tab
            (routes/sessions.tsx, §16.3 -- "sessions list moves to the
            second tab"), no longer reachable
            only from inside an already-open session's own sidebar. */}
        <Link to="/" className="header-nav-link">
          Home
        </Link>
        <Link to="/sessions" className="header-nav-link">
          Sessions
        </Link>
        <Link to="/automations" className="header-nav-link">
          Automations
        </Link>
        <Link to="/workflows" className="header-nav-link">
          Workflows
        </Link>
        <Link to="/settings" className="header-nav-link">
          Settings
        </Link>
        <Link to="/repo-settings" className="header-nav-link">
          Repo settings
        </Link>
        <Link to="/analytics" className="header-nav-link">
          Analytics
        </Link>
        <ThemeToggle />
      </header>
      <main className="app-main">
        <Outlet />
      </main>
    </div>
  )
}

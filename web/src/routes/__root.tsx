import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'

import { ThemeToggle } from '../components/ThemeToggle'

// RouterContext (Step 81, §13.1) -- queryClient is the one thing every
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
        <ThemeToggle />
      </header>
      <main className="app-main">
        <Outlet />
      </main>
    </div>
  )
}

import { createRootRoute, Outlet } from '@tanstack/react-router'

import { ThemeToggle } from '../components/ThemeToggle'

// Root layout (§12.1): the one header every real view is expected to
// share. TanStack Router renders this once and swaps <Outlet/>'s contents
// per route -- the index route below is the only child that exists yet.
export const Route = createRootRoute({
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

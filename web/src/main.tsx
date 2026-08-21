import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createRouter } from '@tanstack/react-router'

import { initTheme } from './lib/theme'
import { installUnauthorizedHandler } from './auth/session'
import { RouteErrorFallback, RouteNotFoundFallback } from './components/shell/RouteFallbacks'
import { routeTree } from './routeTree.gen'
import './styles/tokens.css'
import './styles/base.css'

// Re-applies the SAME preference index.html's own inline pre-paint script
// already set on <html> -- see theme.ts's own doc comment for why this is
// harmless-but-correct redundancy rather than dead code.
initTheme()

// §12.1's data-layer client pattern ("WS transport -> event log -> reducer
// -> query invalidation") starts here with the query cache; the WS
// transport and reducer land with the real data layer, not this
// bootstrap -- no queries are defined yet.
const queryClient = new QueryClient()

// §13.1: wires http.ts's generic 401 hook to THIS app's real
// consequence (invalidate the cached "who am I" query) -- see
// installUnauthorizedHandler's own doc comment for why this belongs here,
// once, rather than per-component.
installUnauthorizedHandler(queryClient)

// defaultErrorComponent/defaultNotFoundComponent are wired here rather than
// per-route: they are the shell's own terminal states, and leaving them unset
// is what left the app rendering TanStack's bare defaults -- an unstyled
// exception dump on any backend hiccup, and a corner-of-the-page "Not Found".
// See components/shell/RouteFallbacks.tsx for why the error one never renders
// the underlying message.
const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultErrorComponent: RouteErrorFallback,
  defaultNotFoundComponent: RouteNotFoundFallback,
})

// Registers `router`'s own route/param types with TanStack Router's global
// type registry, exactly as its own setup docs require -- without this,
// every <Link to="..."> across the app loses type-checked route paths.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('main.tsx: #root element missing from index.html')
}

createRoot(rootElement).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)

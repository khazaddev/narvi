// RouteFallbacks are the app's two terminal states: a route that failed to
// load, and a route that does not exist.
//
// Both were TanStack Router's bare defaults until now — an unstyled
// "Something went wrong!" beside a raw exception message, and a bare
// "Not Found" string in the top-left corner. Neither is a shell concern
// any one view owns, which is exactly why both survived several Steps: the
// error page is what an operator actually sees whenever the control plane
// is briefly unreachable, and §12.4's own screenshot review would have met
// it on any backend hiccup.
//
// Deliberately plain: these use the mockups' own tokens and the existing
// .boot-screen/.eyebrow/.pill-button vocabulary rather than introducing a
// component system for two screens.
//
// The error state does NOT render the underlying exception message. A loader
// error can carry a backend error string, and that string is not written for
// an end user — it can name internal routes, table names or configuration.
// The operator-facing detail belongs in the console, where it already is.

interface RouteErrorProps {
  /** TanStack passes the thrown value; used only to log, never to render. */
  error?: unknown
  /** TanStack passes a reset callback on its own error component. */
  reset?: () => void
}

export function RouteErrorFallback({ error, reset }: RouteErrorProps) {
  if (error !== undefined) {
    // Console, not the page: see this file's own note on why the message is
    // not rendered.
    console.error('narvi: route failed to load', error)
  }

  return (
    <div className="boot-screen" role="alert">
      <p className="eyebrow">Something went wrong</p>
      <h1>Can&rsquo;t reach the control plane</h1>
      <p>
        This view could not load. That usually means the control plane is
        restarting or briefly unreachable &mdash; your session is unaffected.
      </p>
      <p>
        <button
          type="button"
          className="pill-button"
          onClick={() => {
            if (reset) {
              reset()
              return
            }
            window.location.reload()
          }}
        >
          Try again
        </button>
      </p>
    </div>
  )
}

export function RouteNotFoundFallback() {
  return (
    <div className="boot-screen">
      <p className="eyebrow">Not found</p>
      <h1>No such page</h1>
      <p>
        The address you followed doesn&rsquo;t match anything in this control
        plane. It may have been renamed, or the link may be stale.
      </p>
      <p>
        <a className="pill-button" href="/">
          Back to sessions
        </a>
      </p>
    </div>
  )
}

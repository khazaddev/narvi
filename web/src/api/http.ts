// http.ts is the low-level half of §12.1's typed API client: one generic,
// hand-written `request<T>` function that every typed endpoint in
// endpoints.ts calls with a RESPONSE TYPE PARAMETER imported from
// contracts/gen/ts/rest-dtos.ts (via the "@narvi/contracts" alias -- see
// vite.config.ts's own comment) -- never a hand-written interface. See
// web/README.md's own "Typed API client: generated vs. derived" section
// for the full justification of this "thin typed wrapper" shape over
// extending contracts/'s own codegen; the short version: contracts/rest/
// v1/dtos.schema.json models DATA SHAPES only (Session, CreateTurnRequest,
// ...), with no path/method/operationId metadata anywhere in /contracts
// for a generator to turn into route bindings -- extending the generator
// to also emit routes would mean inventing a whole new contracts schema
// surface this Step has no mandate to design. What IS generated
// (rest-dtos.ts) is exactly what this file's own generic signature
// consumes; nothing here re-derives a shape rest-dtos.ts already owns.
//
// ApiError is deliberately NOT a "response type" in §12.1's sense -- it
// describes a CLIENT-SIDE failure (this request didn't succeed), never a
// server response shape; contracts/rest/v1 has no schema for it because
// it isn't a wire shape at all, matching httpapi/helpers.go's own
// writeError, which every REST handler in this codebase uses for its
// error path: `{"error": "<message>"}`, nothing else.
export class ApiError extends Error {
  readonly status: number
  readonly body: unknown

  constructor(status: number, message: string, body: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  /** AbortSignal for caller-driven cancellation (e.g. TanStack Query's own queryFn signal). */
  signal?: AbortSignal
}

// onUnauthorized (Step 81, §13.1's own "expired session" state) -- a
// module-level hook, default no-op, so THIS generic layer can announce
// "a request just came back 401" without importing TanStack Query (or
// anything else app-specific) into it. web/src/auth/session.ts's own
// installUnauthorizedHandler wires the real handler exactly once, from
// main.tsx, to invalidate the cached GET /api/me query -- so a session
// that expires or is revoked mid-use (not just one caught by a route's own
// beforeLoad guard on the NEXT navigation) is noticed the moment ANY
// in-flight request surfaces it, from wherever in the app that request
// happened to originate. Deliberately fire-and-forget (never awaited, and
// request<T> below still throws its own ApiError to the original caller
// either way) -- this is a side-channel notification, not a substitute
// for the caller's own error handling.
let unauthorizedHandler: () => void = () => {}

/** setUnauthorizedHandler installs handler as the one function every 401 response calls (see onUnauthorized's own doc comment above). Test-only escape hatch: real app code calls this exactly once, from main.tsx. */
export function setUnauthorizedHandler(handler: () => void): void {
  unauthorizedHandler = handler
}

/** request performs one JSON REST call against path (e.g. "/api/sessions/abc/events?limit=50") and decodes the response as T -- T is always a generated contracts/gen/ts/rest-dtos.ts export at every real call site (endpoints.ts), never invented here. */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: options.method ?? 'GET',
    headers:
      options.body === undefined
        ? undefined
        : {
            'Content-Type': 'application/json',
          },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    // §12.1: "one binary... on one port" -- the SPA and the REST API it
    // calls always share an origin, so 'same-origin' (never 'include',
    // which would also attach these cookies to a cross-origin request)
    // is both sufficient and the more conservative choice.
    credentials: 'same-origin',
    signal: options.signal,
  }

  const response = await fetch(path, init)

  if (!response.ok) {
    let body: unknown
    try {
      body = await response.json()
    } catch {
      body = undefined
    }
    const message =
      body !== undefined && typeof body === 'object' && body !== null && 'error' in body && typeof (body as { error: unknown }).error === 'string'
        ? (body as { error: string }).error
        : `request failed: ${response.status} ${response.statusText}`
    if (response.status === 401) {
      unauthorizedHandler()
    }
    throw new ApiError(response.status, message, body)
  }

  if (response.status === 204) {
    return undefined as T
  }

  return (await response.json()) as T
}

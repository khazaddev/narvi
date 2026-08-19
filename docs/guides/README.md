# Per-surface user guides

One file per ingress surface — [web](web.md), [Slack](slack.md),
[Linear](linear.md), [GitHub](github.md) — each documenting what that
surface **accepts**, and, just as importantly, its **honest negatives**:
what it silently ignores, what it refuses, and what it does not support at
all. Shipped behavior only. §10-P6's own framing is the reason this
directory exists at all: a hand-maintained prose guide is just a copy of
the code's behavior with no mechanism keeping it in sync, which drifts
into aspirational text the instant the code moves and nobody remembers to
update the prose. `internal/ops`'s `TestNoGuideDrift` (`go test
./internal/ops/...`, part of `make test`) is that mechanism.

## The mechanism

Every accepted command documented in a per-surface guide is followed by a
machine-readable ```` ```json narvi-command ```` fenced block, e.g.:

````
```json narvi-command
{"name": "Create a session", "route": "POST /api/sessions"}
```
````

or, for a command whose real implementation is a §18.4 classifier
routing-decision outcome rather than a literal HTTP route:

````
```json narvi-command
{"name": "PR mention is always routed as a review", "classifier": {"surface": "github", "target": "review"}}
```
````

`internal/ops.LoadGuides` parses every block in every guide file (except
this README, which is prose only); `internal/ops.CheckGuideDrift` checks
each one against:

- **`route`** — a real, currently-registered chi route, scanned directly
  out of `cmd/control-plane/main.go`'s own router wiring via
  `internal/ops.ScanRegisteredRoutes` (a `go/ast` walk, the exact same
  mechanism `ScanRegisteredInstruments` already uses to keep
  `deploy/observability/{dashboards,alerts}` honest — see
  `internal/ops/doc.go`). Written as `"METHOD /path"`, exactly matching
  the joined path a nested `router.Route("/prefix", func(r chi.Router) {
  r.Get("/sub", ...) })` group actually serves.
- **`classifier`** — a real §18.4 routing-decision shape: `surface` must
  be a genuine `sessions.spawn_source` value, and `target`/`mode`/`source`
  (each optional) must, when present, be genuine values
  `internal/domain/intent`'s own exported constants actually define —
  scanned via `internal/ops.ScanIntentVocabulary`.

`internal/ops.TestNoGuideDrift` wires this into `go test ./...` against
the real repo tree — no separate CI job, no way to merge a guide that
documents a route or a routing outcome the code doesn't actually have.

A malformed file — an unterminated code fence, invalid JSON inside one, a
command naming neither or both of `route`/`classifier`, a file with no
top-level heading or zero command blocks — **fails the check**, the same
"never silently skipped" discipline `LoadDashboards`/`LoadAlerts` already
apply to `deploy/observability/*.json` (`internal/ops/schema.go`).

## What this check cannot catch

Stated plainly, mirroring `internal/ops/doc.go`'s own identical caveat for
the dashboards/alerts twin of this mechanism: **it validates names, not
semantics.**

- A route that still exists but no longer behaves the way the guide's
  surrounding prose describes (a handler rewritten to reject a field it
  used to accept, say) passes the check untouched — only a *renamed or
  removed* route is caught.
- A `classifier` block's `surface`/`target`/`mode`/`source` values are
  each checked for existing INDEPENDENTLY, never as a combination that the
  code actually produces together for a specific input. Nothing stops a
  guide from claiming a combination the real code path never reaches (the
  web surface never calls the LLM classifier at all, for instance — see
  [web.md](web.md)'s own "Plan mode" section) as long as each individual
  field name is real.
- Nothing checks that the *prose* around a command block accurately
  describes what the route actually does with its request body, its
  response shape, or its authorization rules — only that the route
  exists. A reviewer still has to read the diff.
- Two other real ingress-shaped endpoints are **deliberately outside every
  guide's scope**, not an oversight: `POST /webhooks/automations/{automationID}`
  (a per-automation, admin-configured inbound trigger — not one of the
  four canonical `sessions.spawn_source` surfaces this Step covers), and
  the sandbox-agent-to-control-plane bearer-authenticated routes under
  `/sessions/{sessionID}/...` (`scm-credentials`, `provider-credentials`,
  `sandbox-secrets`, `opencode-config`, `cloud-identity-token`,
  `cloud-identity-config`, `snapshot`, `review/verdict`,
  `workflow/step-outcome`, `turn/epistemic-outcome`, the bearer variant of
  `uploads`, and the live-stream `ws` endpoint) — machine-to-machine
  wiring a human never calls directly, not a "surface" in this guide's
  sense. Neither is a gap in the *check* — both are real, working routes —
  they are simply not what "what does this surface accept" is asking
  about.

## Mutation-testing this check

Verified by hand as part of Step 78 (§10-P6), reverted byte-identical
afterward — see `internal/ops/guidedrift_test.go`'s own
`TestNoGuideDrift` doc comment and the Step's own PR description for the
exact mutations and test names:

1. Documenting a command whose `route` matches no real endpoint makes
   `TestNoGuideDrift` fail.
2. Renaming a real route in `cmd/control-plane/main.go` without updating
   the guide that documents it makes `TestNoGuideDrift` fail identically.
3. Malforming a guide file (breaking its embedded JSON, or removing a
   closing fence) makes `internal/ops.LoadGuides` itself fail — the test
   errors out rather than silently treating that file as empty.

# contracts

Versioned JSON Schemas for the wire contracts this system must preserve
byte-for-byte (technical plan §6): the sandbox WS protocol (commands +
events), the client WS protocol, `SESSION_CONFIG`, and REST DTOs. Go types
are generated from these schemas for the control plane and the sandbox
agent; TS types are generated for the frontend. `/contracts` is the single
source of wire truth — no hand-written response types anywhere else in the
codebase (§6.3).

## Layout

```
contracts/
  sandbox-ws/v1/commands.schema.json             CP -> sandbox-agent commands (§6.1)
  sandbox-ws/v1/events.schema.json                sandbox-agent -> CP events (§6.1)
  client-ws/v1/protocol.schema.json               browser <-> CP protocol (§6.2)
  session-config/v1/session-config.schema.json    SESSION_CONFIG document (§4.1)
  rest/v1/dtos.schema.json                        REST DTOs (§6.3 — see scope note below)
  embed.go                                        go:embed of the *.schema.json files above
  gen/go/{sandboxws,clientws,sessionconfig,restdtos}/   generated Go types
  gen/ts/*.ts                                     generated TS types
  contractstest/                                  Go round-trip contract tests (§9.2)
  scripts/generate-ts.mjs                         TS codegen entry point
  typecheck/                                       hand-written TS type-level fixtures
  package.json / tsconfig.json                    scoped npm project for codegen + typecheck only
```

`contracts/` is a scoped npm project (`@narvi/contracts`) used only for
codegen and typechecking — it does not make the repo root an npm project.
Phase 6 (the web UI) brings its own frontend package and imports the
generated types under `gen/ts/`.

## Regenerating

```
make contracts-generate   # regenerate contracts/gen/{go,ts} from the schemas
make contracts-check      # regenerate + fail on drift + tsc --noEmit
```

`contracts-generate` runs `go-jsonschema` (pinned via go.mod's `tool`
directive, so no separately-installed binary is required) for the Go side,
and `contracts/scripts/generate-ts.mjs` (`json-schema-to-typescript`) for
the TS side — the latter requires `cd contracts && npm ci` beforehand.
Never hand-edit anything under `gen/go/` or `gen/ts/`; edit the source
`.schema.json` file and regenerate instead. CI runs `make contracts-check`
on every push/PR (`.github/workflows/ci.yml`).

## Testing

- `go test ./contracts/...` (`contractstest/`): for every schema, the
  sequence technical plan §9.2 requires — Go marshal -> JSON Schema
  validate (via `santhosh-tekuri/jsonschema`, against the schema text
  embedded by `embed.go`, format assertions turned on) -> Go unmarshal ->
  compare against the original value. Includes the dedicated regression
  test for §6.1's explicit warning that `step_finish.cost.tokens` is an
  **object** (`{input, output, cached?}`), never a bare number — a
  number-shaped payload is asserted to fail both JSON Schema validation and
  Go unmarshal.
- `typecheck/step-finish-tokens.ts`: the same `tokens`-shape regression
  pinned on the TypeScript side, via a `@ts-expect-error` fixture asserted
  by `npm run typecheck` (part of `make contracts-check`).
- `make contracts-check` is the CI gate for the phase-0 exit criterion
  (§10: "contracts round-trip green"): it fails if regenerating would
  change anything under `gen/`, then runs `tsc --noEmit`.

## Scope note: REST DTOs (§6.3)

§6.3 names the full BFF-facing REST route surface — sessions, events,
artifacts, secrets, environments, automations, uploads, ws-token. As of
PR-05, only **sessions** and **ws-token** were specified in enough
field-level detail anywhere in the technical plan to schema honestly, so
`rest/v1/dtos.schema.json` originally modeled exactly three shapes:
`Session`, `CreateSessionRequest`, and `WSTokenResponse`.

Step 19 ("wshub: clients + session REST") added the **events** and
**artifacts** read endpoints, and with them two more shapes: `EventsResponse`
and `ArtifactsResponse`. Both deliberately reuse the same
`additionalProperties: true` looseness as `contracts/client-ws/v1/
protocol.schema.json`'s own `SubscribedPayload`/`FetchHistoryResponse` (the
technical plan itself leaves the full event/artifact read-model shape to
"later PRs" — REST and the client WS protocol intentionally do not diverge
on this).

**Secrets, environments, automations, and uploads DTOs are still
deliberately not modeled here** — this remains a scope decision, not an
oversight. They belong to the PRs that actually define those features and
can schema them honestly: environments (PR-10/26/27), automations (PR-46/47),
uploads (PR-49), and so on. Do not invent field shapes for those ahead of
the PRs that own them; extend `rest/v1/dtos.schema.json` (or add a new
versioned sibling) when that PR lands instead.

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

## Members/audit-log DTOs (§13.2/§13.3)

An audit finding (wire-contract, `internal/adapters/inbound/httpapi/
members.go`) surfaced that the members API's own 8 response/request shapes
had been hand-written directly in that file, entirely outside this
pipeline — contradicting the "no hand-written response types anywhere else
in the codebase" rule above. These routes are §13.2/§13.3's own members
API, unrelated to §6.3's REST scope note above; they were promoted into
`rest/v1/dtos.schema.json` as a pure migration (no wire-shape change):
`Identity`, `Member`, `PendingLinkPrompt`, `ListMembersResponse`,
`AuditLogEntry`, `ListAuditLogResponse`, `UpdateMemberRoleRequest`, and
`LinkMemberIdentityRequest`.

`UpdateMemberRoleRequest.role` and `LinkMemberIdentityRequest.provider` are
deliberately modeled as unconstrained strings, not enums matching the
closed `user_role`/`identity_provider` sets those fields actually draw
from — `members.go`'s own `validRoles`/`validProviders` maps still own that
validation at the application layer, so an unrecognized value keeps
surfacing that handler's own specific 400 message instead of a generic
schema-decode error. Every enum that is purely an OUTPUT concern (`Member.
role`, `Identity.provider`/`.linkedVia`, `PendingLinkPrompt.provider`) is
modeled as a closed enum, matching this schema's existing `Session`
precedent, since encoding a closed Go enum type is byte-identical to
encoding a plain string.

## Plan DTOs (§8.1/§12.2 item 3, §16.1)

An audit-fix batch closed three related findings that all touch
`internal/adapters/inbound/httpapi/planapprove.go`:

- **M3 (completeness)**: Step 37 ("plan mode, web") shipped plan mode
  write-only — `POST .../plans/:planId/approve|reject` — with no way for a
  web client to ever discover a `planId` to approve in the first place.
  `GET /api/sessions/:id/plans` (`ListPlans`, `internal/adapters/inbound/
  httpapi/plans.go`) closes that gap: every plan VERSION for the session,
  ordered by version, no extra RBAC beyond "session exists" (mirroring
  `ListArtifacts`/`ListEvents`'s own precedent exactly — a plan action
  changes state and needs `canActOnPlan`; a read of the list does not).
- **L14 (wire-contract, plan-endpoint portion) / L16**: `planapprove.go`'s
  own `planActionResponse` struct was hand-written outside this codegen
  pipeline, with an explicit doc comment noting it had no frontend consumer
  yet. Now that `GET .../plans` gives this same area a real DTO-consuming
  sibling endpoint, that reasoning no longer held — it was promoted in the
  same pass.

Three new shapes: `Plan` (one plan-mode version's own wire shape — id,
sessionId, version, status, planModelId, createdAt, decidedAt, decidedBy;
deliberately omits `turnId` and `slack_channel_id`/`slack_message_ts`,
present on the underlying row but internal plumbing not meant for a REST
client, see `Plan`'s own schema doc comment), `ListPlansResponse` (the
`{"plans": Plan[]}` wrapper, following `ListMembersResponse`/
`ListAuditLogResponse`'s own exact "wrapper object with one array field"
shape), and `PlanActionResponse` (the promoted `planActionResponse`
replacement — planId, status, turnId nullable).

`status` on both `Plan` and `PlanActionResponse` is modeled as the full
`plan_status` enum (`awaiting_approval`/`approved`/`rejected`/`superseded`,
matching Postgres `plan_status` exactly, `migrations/000034_plan_mode.up.
sql`) rather than a narrower literal set, for forward-compatibility —
matching `CreateTurnResponse.status`'s own identical precedent above.

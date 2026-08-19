# Web guide

The web surface is Narvi's own browser-facing REST API (`spawnSource:
"web"`) — every route below is under `/api/...` except the three
GitHub-OAuth sign-in routes and the live session WebSocket, both listed in
their own sections. Every request (except sign-in itself) is authenticated
by the `narvi_auth_session` cookie `POST /auth/github/callback` mints; a
request with no valid cookie gets `401`.

Two things this guide deliberately does **not** cover — see
[README.md](README.md#what-this-check-cannot-catch) for why: the
per-automation inbound-webhook trigger
(`POST /webhooks/automations/{automationID}`), and the sandbox-agent-only
bearer-authenticated routes under `/sessions/{sessionID}/...` — those are
machine-to-machine plumbing, not something a human using the web app ever
calls.

## Sign-in

```json narvi-command
{"name": "Sign in with GitHub", "route": "GET /auth/github/login"}
```

```json narvi-command
{"name": "GitHub OAuth callback (completes sign-in)", "route": "GET /auth/github/callback"}
```

```json narvi-command
{"name": "Sign out", "route": "POST /auth/logout"}
```

**Negatives.** GitHub is the *only* sign-in method — there is no
password, no magic-link login, no other OAuth provider. Signing in
requires the GitHub account's own primary/verified email (or, org
membership — see below) to match this deployment's own allowlist
(`NARVI_ALLOWED_EMAIL_DOMAINS`/`NARVI_ALLOWED_GITHUB_ORGS`/
`NARVI_ALLOWED_EMAILS`); an account that matches none of the three is
refused sign-in outright, with no self-service way to request access —
an admin has to widen the allowlist or add the person to
`NARVI_INITIAL_ADMIN_EMAILS`. A brand-new user's role always starts at
**viewer** (§13.3's own lowest role) unless they're on the admin allowlist
— nobody self-elevates.

```json narvi-command
{"name": "Consume an identity-link magic link (posted privately in a Slack/Linear reply, never a real sign-in route)", "route": "GET /auth/identity-link/{nonce}"}
```

This route is **not** a second way to sign in — it is what a Slack/Linear
identity-linking notice's own private link points at, one-time-use,
short-lived (`platform.Timeouts.IdentityLinkPromptTTL`, 24h), and it only
ever *links* a Slack/Linear identity onto an already-signed-in-or-signing-
in GitHub account.

## Sessions

```json narvi-command
{"name": "Create a session", "route": "POST /api/sessions"}
```

Requires **member role or above** (§13.3: viewer can view but never
create). The request body's `repos` field is always a list — never a
single repo, even for a one-repo session (`docs/TECHNICAL_PLAN.md`'s own
"repos are always a list" invariant). An optional `planMode` boolean
picks plan-vs-build for the session's very first turn — **this is the
one and only way the web surface picks Mode**: unlike Slack/Linear/GitHub,
a web-created session's routing decision is recorded with `source:
"explicit"`, never `"classifier"` — the LLM intent classifier is never
invoked for a REST-created session at all, and `req.spawnSource` in the
request body is ignored/overwritten server-side (this endpoint is
structurally only ever reachable as the real web surface — a client
claiming a different `spawnSource` in its own JSON body gets no different
treatment).

```json narvi-command
{"name": "Get a session", "route": "GET /api/sessions/{sessionID}"}
```

```json narvi-command
{"name": "List a session's events", "route": "GET /api/sessions/{sessionID}/events"}
```

```json narvi-command
{"name": "List a session's artifacts", "route": "GET /api/sessions/{sessionID}/artifacts"}
```

```json narvi-command
{"name": "Mint a live-session WebSocket token", "route": "POST /api/sessions/{sessionID}/ws-token"}
```

```json narvi-command
{"name": "Open the live session WebSocket", "route": "GET /sessions/{sessionID}/ws"}
```

**Negatives.** The WS connection must send its own `subscribe{token,
clientId}` frame within `platform.Timeouts.ClientSubscribeTimeout` (30s)
of connecting, or the server closes it (code `4001`, "re-auth required").
The minted token expires after `platform.Timeouts.WSTokenTTL` (24h) — a
tab left open across that boundary gets closed (code `4002`, "token
expired") and needs a fresh mint. The server pings every
`platform.Timeouts.ClientWSPingInterval` (30s); an unanswered ping closes
the connection (code `4003`, "idle timeout"). A `fetch_history` frame
sent more than once per `platform.Timeouts.ClientFetchHistoryMinInterval`
(250ms) on the same connection is logged and dropped — the connection
stays open, but that one request is silently ignored.

## Turns (prompting an existing session)

```json narvi-command
{"name": "Add a turn (send a prompt) to a session", "route": "POST /api/sessions/{sessionID}/turns"}
```

**Negative — the busy behavior is REST-specific.** If the session already
has a non-terminal turn (`Pending`/`Dispatched`/`Processing`), this
endpoint refuses with **`409 Conflict`** (`CreateTurnPolicy.RejectIfOpen`,
`internal/adapters/inbound/httpapi/turn.go`) — it never silently queues a
second turn behind the first, and it never silently drops your message
either. This is deliberately different from every other surface: Slack
and Linear *drop* a message sent to a busy session (an honest in-thread
"still working" reply, nothing queued — see [slack.md](slack.md) /
[linear.md](linear.md)), and GitHub *always* queues a backlog turn behind
the current one, never refuses (see [github.md](github.md)). A web client
that gets a `409` here has to retry once the in-flight turn actually
finishes — there is no built-in queuing to fall back on.

## Plan mode

```json narvi-command
{"name": "List a session's plans", "route": "GET /api/sessions/{sessionID}/plans"}
```

```json narvi-command
{"name": "Approve a plan", "route": "POST /api/sessions/{sessionID}/plans/{planId}/approve"}
```

```json narvi-command
{"name": "Reject a plan", "route": "POST /api/sessions/{sessionID}/plans/{planId}/reject"}
```

Approving/rejecting requires §13.3's `approve_plan` action — own/joined
sessions for member and above, ANY session for maintainer/admin; viewer
never. This is the exact same `httpapi.DecidePlan` core every surface's
own approve/reject path (Slack buttons and typed keywords, Linear typed
keywords, the "Request changes" modal) calls — a plan decided first on
one surface is decided everywhere; a later attempt on another surface
sees (and is told) the outcome the first attempt already produced, never
a second, conflicting decision.

## Review

```json narvi-command
{"name": "Re-trigger a review", "route": "POST /api/sessions/{sessionID}/review/retrigger"}
```

```json narvi-command
{"name": "Rebut a review finding", "route": "POST /api/sessions/{sessionID}/review/findings/{identityHash}/rebut"}
```

```json narvi-command
{"name": "Apply a review finding's suggested fix", "route": "POST /api/sessions/{sessionID}/review/findings/{identityHash}/apply-suggestion"}
```

**Negative.** All three require maintainer role or above (§13.3: "Edit
review verdicts; re-trigger reviews ... admin/maintainer" only) — a
member or viewer gets `403`, even on a session they created or joined.
This is stricter than plan approval on purpose: an ordinary member can
approve their own plan, but cannot re-open or rebut a review verdict.

## Decision inbox

```json narvi-command
{"name": "List the decision inbox", "route": "GET /api/decision-inbox"}
```

```json narvi-command
{"name": "Merge a pull request from the decision inbox", "route": "POST /api/decision-inbox/merge"}
```

## Uploads

```json narvi-command
{"name": "Mint an upload URL", "route": "POST /api/sessions/{sessionID}/uploads"}
```

```json narvi-command
{"name": "Confirm an upload completed", "route": "POST /api/sessions/{sessionID}/uploads/{uploadID}/complete"}
```

```json narvi-command
{"name": "Fetch uploaded content", "route": "GET /api/sessions/{sessionID}/uploads/{uploadID}/content"}
```

**Negatives.** Uploads are **feature-flagged off entirely** unless this
deployment configured object storage (`NARVI_OBJECT_STORE_ENDPOINT` and
friends) — every route above 404s/refuses cleanly on a deployment that
never set it up, rather than half-working. A minted upload URL expires
after `platform.Timeouts.UploadPresignPutTTL` (15 min); a mint that is
never confirmed within `platform.Timeouts.UploadPendingSweepAfter` (24h)
is swept away automatically (`platform.Timeouts.
UploadAbandonmentSweepInterval`, every 15 min) — there is no way to
resurrect an abandoned upload, only to mint a new one.

## Models

```json narvi-command
{"name": "Get the model catalog", "route": "GET /api/models"}
```

## Members, identities, and audit log

```json narvi-command
{"name": "List members", "route": "GET /api/members"}
```

```json narvi-command
{"name": "Change a member's role", "route": "PATCH /api/members/{userID}/role"}
```

```json narvi-command
{"name": "Link an identity to a member", "route": "POST /api/members/{userID}/identities"}
```

```json narvi-command
{"name": "Unlink an identity from a member", "route": "DELETE /api/members/{userID}/identities/{identityID}"}
```

```json narvi-command
{"name": "List the audit log", "route": "GET /api/audit-log"}
```

**Negative.** Changing roles/linking/unlinking identities is admin-only
(§13.3's own last row: "members & roles ... admin" only) — maintainer and
below get `403`. The audit log records completed state changes only; a
denied/refused request (a `403`, a rollout refusal, an authz denial) is
never itself an audit-log row (see [github.md](github.md)'s own "silent
refusal" section for the sharpest example of this).

## ChatGPT account link

```json narvi-command
{"name": "Start a ChatGPT account link", "route": "POST /api/me/chatgpt-link"}
```

```json narvi-command
{"name": "Get ChatGPT link status", "route": "GET /api/me/chatgpt-link"}
```

```json narvi-command
{"name": "Remove a ChatGPT account link", "route": "DELETE /api/me/chatgpt-link"}
```

## Administration & configuration

Everything below is settings/configuration surface, not day-to-day
session use — grouped here rather than given the same prose treatment as
the sections above because each one is a straightforward CRUD endpoint
over one settings table. Every write endpoint in this section requires
maintainer role or above at minimum; several (global secrets/credentials,
integrations, prompt-template activation, per-repo auto-merge and
sentinel-auto-fix toggles) are admin-only (§13.3's own last two rows) —
a maintainer gets `403` on those specifically, not a degraded response.

**Automations**

```json narvi-command
{"name": "Create an automation", "route": "POST /api/automations"}
```

```json narvi-command
{"name": "List automations", "route": "GET /api/automations"}
```

```json narvi-command
{"name": "Get an automation", "route": "GET /api/automations/{automationID}"}
```

```json narvi-command
{"name": "Pause an automation", "route": "POST /api/automations/{automationID}/pause"}
```

```json narvi-command
{"name": "Resume an automation", "route": "POST /api/automations/{automationID}/resume"}
```

```json narvi-command
{"name": "Rotate an automation's inbound webhook token", "route": "POST /api/automations/{automationID}/webhook-token"}
```

```json narvi-command
{"name": "Revoke an automation's inbound webhook token", "route": "DELETE /api/automations/{automationID}/webhook-token"}
```

**Intent classifier templates**

```json narvi-command
{"name": "Preview an intent-classifier prompt template", "route": "POST /api/intent-templates/preview"}
```

```json narvi-command
{"name": "Upsert an intent-classifier prompt template", "route": "POST /api/intent-templates"}
```

**Per-repo settings**

```json narvi-command
{"name": "Get a repo's settings", "route": "GET /api/repos/{owner}/{repo}/settings"}
```

```json narvi-command
{"name": "Update a repo's settings", "route": "PUT /api/repos/{owner}/{repo}/settings"}
```

```json narvi-command
{"name": "List a repo's false-positive review patterns", "route": "GET /api/repos/{owner}/{repo}/false-positive-patterns"}
```

```json narvi-command
{"name": "Retire a false-positive review pattern", "route": "POST /api/repos/{owner}/{repo}/false-positive-patterns/{patternID}/retire"}
```

```json narvi-command
{"name": "Update a repo's auto-approval eligibility settings", "route": "PUT /api/repos/{owner}/{repo}/auto-approval-settings"}
```

```json narvi-command
{"name": "Toggle a repo's auto-merge", "route": "PUT /api/repos/{owner}/{repo}/auto-merge"}
```

```json narvi-command
{"name": "Toggle a repo's automatic re-review", "route": "PUT /api/repos/{owner}/{repo}/auto-retrigger-review"}
```

```json narvi-command
{"name": "Toggle a repo's description auto-fix", "route": "PUT /api/repos/{owner}/{repo}/description-autofix"}
```

```json narvi-command
{"name": "Update a repo's review-depth config", "route": "PUT /api/repos/{owner}/{repo}/review-depth"}
```

```json narvi-command
{"name": "Update a repo's review cost budget", "route": "PUT /api/repos/{owner}/{repo}/review-cost-budget"}
```

```json narvi-command
{"name": "Get a repo's review analytics", "route": "GET /api/repos/{owner}/{repo}/review-analytics"}
```

**Per-repo, per-environment, and global provider credentials / sandbox
secrets** — the SAME four-verb CRUD shape at three different scopes
(§27.1/§27.2); a value set at a narrower scope always wins over a wider
one at resolution time, never the other way around.

```json narvi-command
{"name": "Create a repo provider credential", "route": "POST /api/repos/{owner}/{repo}/provider-credentials"}
```

```json narvi-command
{"name": "List a repo's provider credentials", "route": "GET /api/repos/{owner}/{repo}/provider-credentials"}
```

```json narvi-command
{"name": "Update a repo provider credential", "route": "PUT /api/repos/{owner}/{repo}/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Delete a repo provider credential", "route": "DELETE /api/repos/{owner}/{repo}/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Create an environment provider credential", "route": "POST /api/environments/{environmentID}/provider-credentials"}
```

```json narvi-command
{"name": "List an environment's provider credentials", "route": "GET /api/environments/{environmentID}/provider-credentials"}
```

```json narvi-command
{"name": "Update an environment provider credential", "route": "PUT /api/environments/{environmentID}/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Delete an environment provider credential", "route": "DELETE /api/environments/{environmentID}/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Create a global provider credential", "route": "POST /api/provider-credentials"}
```

```json narvi-command
{"name": "List global provider credentials", "route": "GET /api/provider-credentials"}
```

```json narvi-command
{"name": "Update a global provider credential", "route": "PUT /api/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Delete a global provider credential", "route": "DELETE /api/provider-credentials/{credentialID}"}
```

```json narvi-command
{"name": "Create a repo sandbox secret", "route": "POST /api/repos/{owner}/{repo}/sandbox-secrets"}
```

```json narvi-command
{"name": "List a repo's sandbox secrets", "route": "GET /api/repos/{owner}/{repo}/sandbox-secrets"}
```

```json narvi-command
{"name": "Update a repo sandbox secret", "route": "PUT /api/repos/{owner}/{repo}/sandbox-secrets/{secretID}"}
```

```json narvi-command
{"name": "Delete a repo sandbox secret", "route": "DELETE /api/repos/{owner}/{repo}/sandbox-secrets/{secretID}"}
```

```json narvi-command
{"name": "Create an environment sandbox secret", "route": "POST /api/environments/{environmentID}/sandbox-secrets"}
```

```json narvi-command
{"name": "List an environment's sandbox secrets", "route": "GET /api/environments/{environmentID}/sandbox-secrets"}
```

```json narvi-command
{"name": "Update an environment sandbox secret", "route": "PUT /api/environments/{environmentID}/sandbox-secrets/{secretID}"}
```

```json narvi-command
{"name": "Delete an environment sandbox secret", "route": "DELETE /api/environments/{environmentID}/sandbox-secrets/{secretID}"}
```

```json narvi-command
{"name": "Create a global sandbox secret", "route": "POST /api/sandbox-secrets"}
```

```json narvi-command
{"name": "List global sandbox secrets", "route": "GET /api/sandbox-secrets"}
```

```json narvi-command
{"name": "Update a global sandbox secret", "route": "PUT /api/sandbox-secrets/{secretID}"}
```

```json narvi-command
{"name": "Delete a global sandbox secret", "route": "DELETE /api/sandbox-secrets/{secretID}"}
```

**Negative.** A provider credential's or a sandbox secret's own value is
**write-only** once saved — every GET above returns a masked placeholder
proving a value is configured, never a partial or full reveal of the
real secret. There is no "show value" endpoint anywhere in this surface;
recovering a forgotten value means rotating it (setting a new one), never
reading the old one back.

**Environment cloud-identity, cluster binding, and OpenCode config**

```json narvi-command
{"name": "Create an environment cloud-identity binding", "route": "POST /api/environments/{environmentID}/cloud-identity-bindings"}
```

```json narvi-command
{"name": "List an environment's cloud-identity bindings", "route": "GET /api/environments/{environmentID}/cloud-identity-bindings"}
```

```json narvi-command
{"name": "Update an environment cloud-identity binding", "route": "PUT /api/environments/{environmentID}/cloud-identity-bindings/{bindingID}"}
```

```json narvi-command
{"name": "Delete an environment cloud-identity binding", "route": "DELETE /api/environments/{environmentID}/cloud-identity-bindings/{bindingID}"}
```

```json narvi-command
{"name": "Create a global cloud-identity binding", "route": "POST /api/cloud-identity-bindings"}
```

```json narvi-command
{"name": "List global cloud-identity bindings", "route": "GET /api/cloud-identity-bindings"}
```

```json narvi-command
{"name": "Update a global cloud-identity binding", "route": "PUT /api/cloud-identity-bindings/{bindingID}"}
```

```json narvi-command
{"name": "Delete a global cloud-identity binding", "route": "DELETE /api/cloud-identity-bindings/{bindingID}"}
```

```json narvi-command
{"name": "Rotate the cloud-identity OIDC signing key", "route": "POST /api/cloud-identity/signing-keys/rotate"}
```

```json narvi-command
{"name": "Get an environment's cluster binding", "route": "GET /api/environments/{environmentID}/cluster-binding"}
```

```json narvi-command
{"name": "Set an environment's cluster binding", "route": "PUT /api/environments/{environmentID}/cluster-binding"}
```

```json narvi-command
{"name": "Delete an environment's cluster binding", "route": "DELETE /api/environments/{environmentID}/cluster-binding"}
```

```json narvi-command
{"name": "Get an environment's OpenCode config", "route": "GET /api/environments/{environmentID}/opencode-config"}
```

```json narvi-command
{"name": "Set an environment's OpenCode config", "route": "PUT /api/environments/{environmentID}/opencode-config"}
```

```json narvi-command
{"name": "Delete an environment's OpenCode config", "route": "DELETE /api/environments/{environmentID}/opencode-config"}
```

```json narvi-command
{"name": "Get the global OpenCode config", "route": "GET /api/opencode-config"}
```

```json narvi-command
{"name": "Set the global OpenCode config", "route": "PUT /api/opencode-config"}
```

```json narvi-command
{"name": "Delete the global OpenCode config", "route": "DELETE /api/opencode-config"}
```

**Workflow runs**

```json narvi-command
{"name": "Decide a workflow HITL step", "route": "POST /api/workflow-runs/{runId}/steps/{stepRunId}/decide"}
```

**Diagnostics**

```json narvi-command
{"name": "View the shadow-mode classifier comparison report", "route": "GET /api/admin/shadow-compare"}
```

This one is read-only diagnostics for the intent classifier's own §18.5
shadow mode — see [github.md](github.md)'s own "shadow classification"
section for what shadow mode actually means (and does not yet do).

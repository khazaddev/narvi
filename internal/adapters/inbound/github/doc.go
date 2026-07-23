// Package github implements the GitHub webhook ingress adapter (Step 32,
// "GitHub ingress", §8.2's own phrase "atomic claim coalescing of
// concurrent @mentions"). It is the INGRESS layer only: detect a PR
// @mention of this deploy's own bot handle, atomically coalesce
// concurrent mentions on the SAME pull request onto one shared review
// session (reusing it via a new turn rather than spawning a duplicate),
// and get a session/turn genuinely created and dispatched -- laying the
// groundwork Phase 4's actual code-review logic (verdict posting,
// risk-map, sentinels -- §8.2's fuller feature list, Steps 40+) builds on
// later. This Step deliberately does NOT implement any of that later
// logic, and does NOT touch internal/domain/automation (confirmed an
// empty stub, Step 46/47's own job, Phase 4) -- the plan row's own
// "events -> automations" phrase is aspirational/forward-looking (where
// this ingress eventually plugs in once a later Step builds the
// automations engine), not a concrete Step-32 deliverable.
//
// # Route
//
// POST /webhooks/github (cmd/control-plane/main.go), mounted OUTSIDE
// internal/adapters/inbound/auth.Middleware entirely -- this is a
// GitHub-authenticated route (HMAC signature over the raw body), not a
// cookie-authenticated browser one, mirroring internal/adapters/inbound/
// httpapi's own scm-credentials/snapshot-mint routes' identical
// reasoning for being mounted outside that gate.
//
// # Request handling (handler.go)
//
//  1. Read the raw body (capped via http.MaxBytesReader, mirroring
//     httpapi's own maxRequestBodyBytes precedent) -- the signature below
//     is computed over these EXACT bytes, so nothing may decode/re-encode
//     the body first.
//  2. Verify "X-Hub-Signature-256: sha256=<hex>" via
//     platform.VerifyWebhookSignature (internal/platform/webhooksig.go),
//     using Config.WebhookSecret -- GitHub's own real webhook secret,
//     DISTINCT from platform.Config.HMACWebhookSecret (see that field's
//     own doc comment, internal/platform/config.go). Any failure (missing
//     header, malformed hex, mismatch) is rejected 401, fail-closed.
//  3. Claim (provider="github", deliveryID=the "X-GitHub-Delivery"
//     header) via postgres.WebhookDeliveryStore.Claim (§5.1's atomic
//     INSERT ... ON CONFLICT dedupe, Step 31) -- a redelivery of an
//     already-claimed delivery id is acknowledged 200 without being
//     processed again.
//  4. Parse "X-GitHub-Event": only "issue_comment" (a comment on an issue
//     OR a pull request -- GitHub's own API models a PR as a kind of
//     issue; only the PR case is acted on) and
//     "pull_request_review_comment" (a comment on a specific diff line)
//     can carry a PR @mention; anything else is acknowledged 200,
//     untouched (payload.go).
//  5. Detect whether the comment body actually mentions Config.BotHandle
//     (a simple, case-insensitive "@handle" match with a word-boundary
//     guard -- see payload.go's own mentionPattern doc comment for its
//     known limitations). No mention -> acknowledged 200, no session
//     created.
//  6. Normalize into a restdtos.CreateSessionRequest (spawnSource:
//     "github", prompt: the mention comment's own body, repos: derived
//     from the PR's own head repo/branch when the event type carries it
//     directly -- see payload.go's own KNOWN LIMITATION note for
//     "issue_comment", which does not) and hand off to coalesce.go's
//     SessionCoalescer.CreateOrJoin.
//
// # Per-PR coalescing design (coalesce.go) -- the real design work of
// this Step
//
// migrations/000028_github_pr_sessions.up.sql adds a small mapping table,
// github_pr_sessions, keyed on the natural (repo_full_name, pr_number)
// identity -- mirroring webhook_deliveries' own minimal-columns,
// composite-key shape (Step 31) rather than inventing a different claim
// shape, per §5.1's own "INSERT ... ON CONFLICT atomic claims" house
// style. The claim is a TWO-STEP sequence inside one Postgres
// transaction, composing two idioms this codebase ALREADY uses elsewhere
// for analogous problems (not a third, novel pattern):
//
//  1. `INSERT ... ON CONFLICT (repo_full_name, pr_number) DO NOTHING`
//     ensures a claim row exists (session_id NULL on a fresh insert) --
//     the SAME atomic-claim idiom ClaimWebhookDelivery already
//     establishes (Step 31).
//  2. `SELECT session_id ... FOR UPDATE` locks that row for the rest of
//     the transaction -- the SAME session-row-locking precedent
//     internal/adapters/inbound/httpapi/turn.go's own CreateTurn already
//     uses (GetActorEpochForUpdate) to close an identical check-then-act
//     race. Any concurrent caller's own step 2, for the SAME PR, blocks
//     here until the first caller commits or rolls back.
//
// Whichever caller observes session_id still NULL under that lock is the
// genuine first mention on this PR (the WINNER): it creates the session
// AND its initial turn INLINE, on the exact SAME transaction/connection
// that already holds the claim row lock (coalesce.go's own
// sessionAndTurnOnTx -- a small, deliberately trimmed subset of
// createSessionCore's own logic, NOT a call to httpapi.
// CreateSessionForBot -- see CreateOrJoin's own "connection-pool safety"
// doc comment in coalesce.go for exactly why a second, simultaneous pool
// connection there would be a genuine deadlock risk under enough
// concurrent same-PR mentions), then fills the claim row's session_id in
// and commits, releasing the lock. Every other (concurrent or later)
// caller observes a non-NULL session_id under that SAME lock, commits
// immediately (releasing its own connection), and only THEN -- with no
// transaction of its own left open -- enqueues a new turn on the EXISTING
// session via httpapi.CreateTurnForBot. Reuse, never a duplicate session,
// and never more than one live connection per request in either branch.
//
// A LOSING caller therefore never creates a session row at all (zero
// wasted Postgres writes, zero wasted actor-spawn/dispatch side effects).
// A crash on the WINNING path is likewise not a source of duplicate
// sessions: sessionAndTurnOnTx's own session+turn inserts, SetSessionID,
// and the final commit are all part of the exact SAME transaction, so
// either all of it lands atomically or none of it does -- a crash before
// commit rolls everything back cleanly, including the claim row itself,
// and a later mention simply becomes the new, legitimate first winner.
// The one place a genuine "vanishingly rare" gap remains is the ordinary
// check-then-act window turn.go's own CreateTurn doc comment already
// accepts for its identical session-row lock pattern (a concurrent
// session DELETE between an earlier existence check and the lock) -- not
// a new kind of risk this Step introduces.
//
// # Turns created via bot-ingress coalescing are a Pending BACKLOG, not
// single-turn-at-a-time
//
// httpapi.CreateTurnForBot deliberately does NOT apply CreateTurn's own
// REST-specific hasOpenTurn 409 gate (see that function's own doc
// comment, httpapi/bot.go) -- domain/turn.NextToDispatch already supports
// an arbitrary Pending backlog on one session, dispatching the oldest one
// only once nothing is Dispatched/Processing. N concurrent @mentions on a
// PR that already has a review session therefore all succeed, producing
// exactly one session and N turns, dispatched one at a time by that
// session's own actor -- exactly what "session reuse" (§8.2) means.
package github

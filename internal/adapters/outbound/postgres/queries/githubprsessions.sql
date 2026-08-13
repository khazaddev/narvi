-- Queries backing GitHubPRSessionStore (§8.2's "atomic claim coalescing of
-- concurrent @mentions", Step 32 "GitHub ingress"). See
-- migrations/000028_github_pr_sessions.up.sql's own doc comment for the
-- full two-step claim design (ensure the row exists via ON CONFLICT, then
-- lock + branch via FOR UPDATE) these three queries implement together.

-- name: EnsureGitHubPRSessionRow :exec
-- Idempotently ensures a (repo_full_name, pr_number) claim row exists,
-- with session_id left NULL on a fresh insert -- the SAME
-- "INSERT ... ON CONFLICT" atomic-claim idiom ClaimWebhookDelivery (Step
-- 31) already establishes. DO NOTHING here since this step's only job is
-- "make sure the row is there to lock next" -- the following
-- LockGitHubPRSessionForUpdate call, in the SAME transaction, is what
-- actually determines who won.
INSERT INTO github_pr_sessions (repo_full_name, pr_number)
VALUES ($1, $2)
ON CONFLICT (repo_full_name, pr_number) DO NOTHING;

-- name: LockGitHubPRSessionForUpdate :one
-- Locks the (repo_full_name, pr_number) row for the remainder of the
-- caller's own transaction -- any concurrent caller's own call to this
-- SAME query for the SAME PR blocks here until the first caller commits
-- or rolls back, mirroring internal/adapters/inbound/httpapi/turn.go's
-- own GetActorEpochForUpdate row-lock precedent exactly. The caller must
-- have already run EnsureGitHubPRSessionRow (in the SAME transaction) so
-- this row is guaranteed to exist by the time this runs.
SELECT session_id FROM github_pr_sessions
WHERE repo_full_name = $1 AND pr_number = $2
FOR UPDATE;

-- name: SetGitHubPRSessionID :exec
-- Fills in session_id for a (repo_full_name, pr_number) claim row while
-- still holding LockGitHubPRSessionForUpdate's own row lock -- called
-- exactly once, by whichever caller observed session_id NULL under that
-- lock (the genuine first mention on this PR), immediately after it
-- creates the real session, still inside the SAME transaction as both the
-- lock and the session insert.
UPDATE github_pr_sessions
SET session_id = $3
WHERE repo_full_name = $1 AND pr_number = $2;

-- name: GetGitHubPRSessionBySessionID :one
-- The REVERSE lookup Step 35 ("outbox delivery") needs: given a
-- session_id, which (repo_full_name, pr_number) PR does it back? Backed
-- by migrations/000032_github_pr_sessions_session_id_idx.up.sql's own new
-- index (this table had no session_id index before that Step, since Step
-- 32's own ingress path only ever needed the FORWARD direction, its own
-- primary key). A pgx.ErrNoRows result means this session was never
-- created via a GitHub PR mention -- the caller skips enqueuing a GitHub
-- notification entirely rather than fabricating one.
SELECT * FROM github_pr_sessions
WHERE session_id = $1;

-- SetGitHubPRSessionHeadSHA (and pending_head_sha, migrations/000068) is
-- REMOVED as of migrations/000072_turns_review_head_sha.up.sql (§62
-- review finding C2, CRITICAL, fixed) -- superseded by turns.
-- review_head_sha, set once at turn-creation time and read back via
-- TurnStore.GetProcessingTurnForSession, never a shared per-(repo,PR)
-- column any later, unrelated turn's own context-fetch could overwrite.
-- See that migration's own doc comment for the full "why".

-- Queries backing Step 65's ("review: automatic re-review on new
-- commits", §24) trailing-edge debounce + per-PR budget -- see
-- migrations/000075_github_pr_sessions_retrigger.up.sql's own doc comment
-- for the three columns below, including why pending_retrigger_head_sha
-- is a deliberately DIFFERENT name from the retired pending_head_sha.

-- name: UpsertPendingRetriggerHeadSHA :one
-- The synchronize webhook handler's own direct, actor-bypassing write
-- (§24.1's 4th cost item, internal/adapters/inbound/github/
-- pullrequestsynchronize.go) -- called in the SAME transaction as
-- UpsertSessionTimer's own re-arm (session_timers.sql), never
-- independently committed. Guarded on session_id IS NOT NULL (CLAUDE.md/
-- §11's own "guarded UPDATE ... WHERE for cross-writer transitions" idiom)
-- so this is a genuine no-op -- pgx.ErrNoRows, never a fabricated row --
-- for exactly the two cases §24.1 says must be acknowledged and ignored:
-- no github_pr_sessions row at all for this PR, or a row whose session_id
-- is still NULL (nobody has ever mentioned the bot on this PR) -- "no
-- session to re-trigger", identical in kind to today's "no mention"
-- no-op for comment events. Overwrites (never appends) on every event,
-- per §24.2's own "upserted, not appended" rule.
UPDATE github_pr_sessions
SET pending_retrigger_head_sha = $3
WHERE repo_full_name = $1 AND pr_number = $2 AND session_id IS NOT NULL
RETURNING *;

-- name: ClearPendingRetriggerHeadSHA :one
-- The review_retrigger_debounce timer's own fire handler
-- (sessionactor.handleReviewRetriggerDebounceTimer) calls this after
-- EVERY firing that reaches a decision (§24.3 steps 3 and 4 alike: heads
-- already match, a turn was just enqueued, or the budget is exhausted) --
-- guarded on pending_retrigger_head_sha STILL equalling the exact value
-- ($3) this firing just read and acted on (a compare-and-swap, CLAUDE.md/
-- §11's own guarded-UPDATE idiom): a NEW synchronize event landing
-- between this firing's own read and this clear (a genuine, expected
-- race between the webhook handler and this timer's own claimed-but-
-- still-processing window) will already have overwritten this column
-- with a NEWER head sha and re-armed the timer fresh -- this guard
-- ensures THAT newer, still-unprocessed push is never silently clobbered
-- back to NULL by a decision made against a now-stale value. pgx.ErrNoRows
-- on a guard miss is the expected, harmless outcome in that race (nothing
-- to clear -- a fresher event already owns this row). Rereview fix
-- (finding 2, correcting this comment's own earlier false claim): the
-- caller must NOT then proceed to delete "its own claimed timer row" --
-- session_timers has UNIQUE(session_id, name), so there is exactly ONE
-- review_retrigger_debounce row for this session, and on a guard miss it
-- is already the SAME row the newer synchronize event's own re-arm just
-- updated. Deleting it here would strand that newer push with no timer
-- left to ever act on it -- the caller must skip its own delete on a
-- guard miss instead, trusting the newer event's own already-committed
-- re-arm to stand in for it (the same re-arm-or-delete contract every
-- named timer already follows, timerfired.go, satisfied by the newer
-- event's re-arm rather than by this firing's own delete).
UPDATE github_pr_sessions
SET pending_retrigger_head_sha = NULL
WHERE repo_full_name = $1 AND pr_number = $2 AND pending_retrigger_head_sha = $3
RETURNING *;

-- name: IncrementAutoRetriggerCount :one
-- §24.6's own budget counter -- incremented exactly once per PR each
-- time handleReviewRetriggerDebounceTimer actually enqueues an automatic
-- re-review turn (never for a manual label/button re-trigger, which is
-- never subject to this budget at all). A plain increment, not a guarded
-- one: only this PR's own session actor ever writes this column, so
-- there is no cross-writer race here to guard against (unlike
-- pending_retrigger_head_sha, which the webhook handler ALSO writes).
UPDATE github_pr_sessions
SET auto_retrigger_count = auto_retrigger_count + 1
WHERE repo_full_name = $1 AND pr_number = $2
RETURNING *;

-- name: MarkAutoRetriggerBudgetNoticeSent :one
-- §24.6's own "a one-time event, not repeated on every subsequent
-- firing" rule -- guarded on auto_retrigger_budget_notice_sent_at IS
-- NULL so a caller can tell (via pgx.ErrNoRows on a guard miss) "this PR
-- was already notified", the same guarded-UPDATE-as-claim idiom
-- RetireFalsePositivePattern already establishes
-- (reviewfalsepositivepatterns.sql) for a comparable single-writer,
-- once-only transition.
UPDATE github_pr_sessions
SET auto_retrigger_budget_notice_sent_at = now()
WHERE repo_full_name = $1 AND pr_number = $2 AND auto_retrigger_budget_notice_sent_at IS NULL
RETURNING *;

-- handleReviewRetriggerDebounceTimer's own read of pending_retrigger_head_
-- sha/auto_retrigger_count/auto_retrigger_budget_notice_sent_at reuses the
-- EXISTING GetGitHubPRSessionBySessionID above (a.sessionID is exactly
-- what a TimerFired command carries -- there is no separate (repo,
-- pr_number) identity to look this row up by at that point) -- no new
-- query needed for it.

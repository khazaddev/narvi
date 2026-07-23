-- github_pr_sessions: the atomic per-PR review-session coalescing claim
-- (Step 32, "GitHub ingress", §8.2's own phrase "atomic claim coalescing
-- of concurrent @mentions"; §5.1's "Dedupe/coalescing (webhook events,
-- concurrent PR @mentions) via INSERT ... ON CONFLICT atomic claims").
--
-- (repo_full_name, pr_number) is the natural key identifying "the one
-- review session for this PR" -- sessions itself has no such column
-- (§3.1's sessions table is provider-agnostic; spawn_source alone doesn't
-- carry a GitHub-specific PR identity), so this is a small, dedicated
-- mapping table, mirroring webhook_deliveries' own minimal-columns shape
-- (migrations/000027_webhook_deliveries.up.sql) rather than inventing a
-- different claim shape.
--
-- session_id is NULLABLE, not NOT NULL: the atomic claim happens in TWO
-- steps within the SAME transaction (see internal/adapters/inbound/
-- github/coalesce.go's own doc comment for the full sequencing) --
-- first, `INSERT ... ON CONFLICT DO NOTHING` ensures a row exists for
-- (repo_full_name, pr_number) [the SAME "INSERT ... ON CONFLICT" idiom
-- ClaimWebhookDelivery already establishes, Step 31]; then
-- `SELECT ... FOR UPDATE` locks that row, serializing any concurrent
-- claimant for the SAME PR behind it [the SAME session-row-locking
-- precedent internal/adapters/inbound/httpapi/turn.go's own CreateTurn
-- already uses via GetActorEpochForUpdate]. Whichever caller observes
-- session_id still NULL under that lock is the genuine first mention on
-- this PR -- it creates the real session, then fills this row's
-- session_id in (still holding the lock) before committing. Every other
-- concurrent (or later) caller observes a non-NULL session_id and reuses
-- it, enqueuing a new turn instead of a new session.
CREATE TABLE github_pr_sessions (
    repo_full_name TEXT NOT NULL,
    pr_number      INTEGER NOT NULL,
    session_id     UUID REFERENCES sessions(id) ON DELETE CASCADE,
    claimed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_full_name, pr_number)
);

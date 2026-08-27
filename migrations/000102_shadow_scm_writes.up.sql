-- shadow_scm_writes: the ledger §30.6 calls "the suppressed delta".
--
-- Shadow mode's product is the record. Most of "observe everything"
-- already exists and keeps working untouched -- review_verdicts,
-- review_findings, events, artifacts, auto_approval_outcomes are written
-- in shadow exactly as live. This table covers only what did NOT happen:
-- every customer-visible SCM write the gate intercepted, with enough of
-- its intention preserved that an operator can evaluate what the platform
-- would have done.
--
-- Append-only by construction: no UPDATE or DELETE query exists for it.
-- A suppressed effect is a historical fact, and editing the record of
-- something that never happened has no meaning.
--
-- session_id is ON DELETE SET NULL, not CASCADE, on review_verdicts' own
-- precedent (migrations/000067): the history outlives the session it came
-- from. Deleting a session must not erase the evidence that the platform
-- would have written to a customer's repository during it.
--
-- spec_json holds the write's decoded intention and NEVER a credential.
-- That exclusion is enforced in Go by the record types the writer accepts
-- (internal/app/shadowledger), which have no token field at all, so a
-- token cannot reach this column even by mistake -- §30.6: "excluding the
-- credential from the ledger is a compile error, not a redaction pass".
-- This comment is the reason the column can be trusted, not the mechanism;
-- the mechanism is that nothing can construct a record carrying one.
--
-- result_json holds the synthetic result handed back to the caller, so the
-- record shows both what would have gone out and what the platform told
-- itself came back. NULL where no result was synthesized at all -- the
-- MergePR case (§30.7), where a fabricated success would be a false-record
-- generator rather than a stand-in.
CREATE TABLE shadow_scm_writes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation      TEXT NOT NULL,
    repo_full_name TEXT NOT NULL,
    target         TEXT,
    spec_json      JSONB NOT NULL,
    result_json    JSONB,
    session_id     UUID REFERENCES sessions(id) ON DELETE SET NULL,
    correlation_id TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The operator surface reads a repository's suppressed writes newest
-- first; the session join is how a suppressed write is shown beside the
-- session that would have produced it.
CREATE INDEX shadow_scm_writes_repo_created_idx ON shadow_scm_writes (repo_full_name, created_at DESC);
CREATE INDEX shadow_scm_writes_session_idx ON shadow_scm_writes (session_id) WHERE session_id IS NOT NULL;

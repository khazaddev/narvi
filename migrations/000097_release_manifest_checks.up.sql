-- release_manifest_checks: durable persistence of the release manifest
-- check's own result ("release PR review", §15.2/§15.3), added for Step
-- 84's dedicated release-review screen (§12.2 item 9).
--
-- Before this table existed, internal/app/releasereview.Run computed the
-- manifest findings (§15.2) and the aggregate-review trigger decision
-- (§15.3) purely to RENDER them into one markdown comment
-- (reviewpost.RenderManifestComment), delivered via the outbox and never
-- persisted anywhere queryable -- exactly the "never re-parsing anything
-- out of posted comment text" invariant this codebase applies everywhere
-- else (review/doc.go) made this data structurally unavailable to a UI
-- read endpoint. This table closes that gap the same way review_verdicts
-- (§21.1) already closed it for the per-PR risk-map verdict: one
-- append-only row per check, written from the SAME already-computed
-- typed data Run() renders from, never re-derived from the comment text.
--
-- One row per release PR in practice (internal/app/releasereview's own
-- top doc comment: "a re-trigger on an already-tracked release PR does
-- not re-run this check in this Step" -- github_pr_sessions' own per-PR
-- atomic claim guarantees at most one winning session-creation ever
-- reaches Run for a given release PR). Modeled as append-only anyway,
-- mirroring review_verdicts' own GetLatest-over-DISTINCT-ON precedent,
-- so a future Step that DOES re-run this check on a later push needs no
-- schema change to start appending a second row.
--
-- findings/merged_prs/aggregate_review_trigger_reasons are plain JSON
-- arrays, not normalized child tables -- mirrors review_verdicts.
-- blast_radius/digest_arch_decisions' own identical "a small,
-- read-mostly, always-read-as-a-whole array never needs its own table"
-- precedent (internal/app/reviewverdict/convert.go's own
-- marshalTags/marshalArchDecisions). All three are NOT NULL DEFAULT
-- '[]'::jsonb, never a JSON null, for the same reason those two columns
-- are: a present, empty array and an absent value must never be
-- ambiguous to a reader.
CREATE TABLE release_manifest_checks (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,

    repo_full_name TEXT NOT NULL,
    pr_number      INTEGER NOT NULL,
    base_ref       TEXT NOT NULL,
    head_ref       TEXT NOT NULL,

    constituent_pr_count INTEGER NOT NULL,
    -- coverage_partial mirrors RenderManifestComment's own
    -- coveragePartial parameter (audit-fix should-fix #5): whether
    -- ports.SourceControl.ListMergedBetween's own truncated return was
    -- true for this check -- never a completeness guarantee this check
    -- did not actually have.
    coverage_partial BOOLEAN NOT NULL DEFAULT false,

    aggregate_review_triggered       BOOLEAN NOT NULL DEFAULT false,
    aggregate_review_trigger_reasons JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- findings: review.ManifestFinding[] (kind/prNumber/prTitle/detail),
    -- the SAME typed data RenderManifestComment already renders from.
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- merged_prs: the full constituent-PR list this check examined
    -- (number/title/hasApprovingReview/mergedViaAdminOverride/
    -- ciConclusionAtMergeSha/wasReverted/revertReviewState/
    -- revertedAfterMergeSeconds/hadManualConflictResolution/
    -- highRiskFlagged) -- the manifest table's own row source.
    merged_prs JSONB NOT NULL DEFAULT '[]'::jsonb,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs GetLatestReleaseManifestCheck's own per-(repo, pr) lookup, newest
-- first -- mirrors review_verdicts' own (repo_full_name, pr_number,
-- created_at) index precedent exactly.
CREATE INDEX release_manifest_checks_repo_pr_created_at_idx
    ON release_manifest_checks (repo_full_name, pr_number, created_at DESC);

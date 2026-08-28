-- §30.8's own promotion fence: "promotion additionally sets a fence --
-- only verdicts after the promotion timestamp are candidates". A second,
-- independent backstop alongside review_verdicts.suppressed_in_shadow's
-- own per-row stamp (migrations/000105_review_verdicts_shadow_epoch.up.
-- sql) -- deliberate redundancy in one direction, mirroring §30.2's own
-- transport-gate-plus-port-decorator precedent: a bug in the per-row
-- stamp alone must not be the only thing standing between a shadow-era
-- verdict and a real auto-merge.
--
-- Nullable, and cleared (not merely left stale) on demotion -- see
-- UpsertLiveEgressEnabled's own updated doc comment (queries/
-- reposettings.sql) for the full transition table. NULL means "never
-- promoted, or demoted since the last promotion" -- either way, the
-- fence must exclude every verdict on record for this repo, which the
-- candidate queries achieve by comparing against COALESCE(
-- live_egress_promoted_at, 'infinity'::timestamptz): a NULL fence
-- compares greater than any real created_at, so nothing on record can
-- ever be after it.
ALTER TABLE repo_settings ADD COLUMN live_egress_promoted_at TIMESTAMPTZ;

-- Backfill, because without it a repository that was ALREADY promoted
-- when this column arrived would read as never-promoted forever.
--
-- NULL means "never promoted, or demoted since", and the candidate
-- queries turn that into 'infinity' -- so a live repository carrying a
-- NULL fence is excluded from auto-merge candidacy permanently, while
-- every other part of the system correctly treats it as live. The upsert
-- cannot repair it either: a later true -> true flip takes the ELSE
-- branch and preserves the NULL.
--
-- now() is the conservative answer rather than an approximation of when
-- promotion actually happened, which nothing recorded. It fences out
-- every verdict that already exists for these repositories, which errs
-- toward re-reviewing rather than toward auto-merging on the strength of
-- a verdict whose egress mode nobody can now establish. That is the same
-- direction every other choice in this design leans.
UPDATE repo_settings
SET live_egress_promoted_at = now()
WHERE live_egress_enabled = true AND live_egress_promoted_at IS NULL;

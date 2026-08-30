-- §30.7's own calibration-read exclusion, which the phase shipped without.
--
-- §30.7 is explicit that a shadow-era verdict "must never arm auto-merge
-- after promotion", and that shadow outcomes are "excluded from the
-- calibration read model by the same query-level stamp §30.8 requires of
-- verdicts". review_verdicts got that stamp. auto_approval_outcomes --
-- the table the contradiction rate is actually computed FROM -- did not.
--
-- The consequence is specific and it runs the wrong way. In shadow, Narvi
-- still writes review_verdicts rows (§30.6 keeps recording), so it still
-- observes its own auto-approvals being contradicted by human reviewers
-- on the customer's real PRs. Those observations landed in this table
-- undifferentiated, and the contradiction rate is the instrument that
-- justifies arming auto-merge for real. An evaluation would therefore
-- move the number that decides whether the product is trusted to merge,
-- using verdicts nobody ever saw.
--
-- Default false, and that polarity is right for THIS column: every row
-- that predates it was recorded live, and a false value means "counted",
-- which is the pre-existing behaviour. A shadow row must be written with
-- an explicit true -- the exclusion is a positive act, never an omission.
ALTER TABLE auto_approval_outcomes
    ADD COLUMN suppressed_in_shadow BOOLEAN NOT NULL DEFAULT false;

-- The calibration query filters on this column, so it belongs in the
-- index that query already rides.
DROP INDEX IF EXISTS auto_approval_outcomes_repo_decided_idx;
CREATE INDEX auto_approval_outcomes_repo_decided_idx
    ON auto_approval_outcomes (repo_full_name, decided_at DESC)
    WHERE NOT suppressed_in_shadow;

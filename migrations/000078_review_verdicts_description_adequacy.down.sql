ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_description_adequacy;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_adequacy_explanation;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_proposed_body;

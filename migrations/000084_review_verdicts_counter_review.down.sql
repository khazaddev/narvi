ALTER TABLE review_verdicts DROP COLUMN IF EXISTS counter_review;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS fact_check;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS fact_check_killed;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_contested_points;

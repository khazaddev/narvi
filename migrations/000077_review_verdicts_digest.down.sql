ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_summary;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_arch_decisions;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_stack_risks;
ALTER TABLE review_verdicts DROP COLUMN IF EXISTS digest_unverified_limits;

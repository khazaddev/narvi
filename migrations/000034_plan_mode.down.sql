DROP INDEX IF EXISTS plans_one_awaiting_approval_per_session;
DROP TABLE IF EXISTS plans;
DROP TYPE IF EXISTS plan_status;
ALTER TABLE sessions DROP COLUMN IF EXISTS build_model_id;

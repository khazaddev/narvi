DROP TABLE IF EXISTS contract_drift_snapshots;
ALTER TABLE environments DROP COLUMN IF EXISTS contracts_path;

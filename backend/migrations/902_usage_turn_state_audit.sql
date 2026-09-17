-- Nullable: historical/unobserved is distinct from an observed empty header.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS turn_state_audit JSONB;

-- Fork-only audit fields for OpenAI cache-token billing reclassification.
-- Constant defaults are metadata-only on supported PostgreSQL versions and do
-- not rewrite historical usage partitions. Historical rows keep ratio=1.0 and
-- zero raw fields; newly written rows always include provider-reported values.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_input_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_billing_ratio NUMERIC(10, 6) NOT NULL DEFAULT 1.0,
    ADD COLUMN IF NOT EXISTS upstream_total_cost NUMERIC(20, 10) NOT NULL DEFAULT 0;

ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_cache_billing_ratio_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_cache_billing_ratio_check
    CHECK (cache_billing_ratio > 0 AND cache_billing_ratio <= 1) NOT VALID;

ALTER TABLE usage_logs
    VALIDATE CONSTRAINT usage_logs_cache_billing_ratio_check;

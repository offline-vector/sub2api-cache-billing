-- Preserve both customer-billable and provider-reported cache/cost buckets in
-- dashboard aggregates. The aggregate upstream_input_tokens column stores
-- uncached input (not the provider's total prompt count), matching input_tokens.
-- Existing rows predate the policy and therefore share the legacy values.

ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS upstream_input_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reclassified_cache_tokens BIGINT NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS upstream_input_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reclassified_cache_tokens BIGINT NOT NULL DEFAULT 0;

UPDATE usage_dashboard_hourly
SET upstream_input_tokens = input_tokens,
    upstream_cache_read_tokens = cache_read_tokens,
    upstream_cost = total_cost
WHERE upstream_input_tokens = 0
  AND upstream_cache_read_tokens = 0
  AND upstream_cost = 0
  AND reclassified_cache_tokens = 0;

UPDATE usage_dashboard_daily
SET upstream_input_tokens = input_tokens,
    upstream_cache_read_tokens = cache_read_tokens,
    upstream_cost = total_cost
WHERE upstream_input_tokens = 0
  AND upstream_cache_read_tokens = 0
  AND upstream_cost = 0
  AND reclassified_cache_tokens = 0;

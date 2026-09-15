package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICacheBillingAuditMigration(t *testing.T) {
	sqlBytes, err := FS.ReadFile("900_openai_cache_billing_audit.sql")
	require.NoError(t, err)
	sql := string(sqlBytes)

	require.Contains(t, sql, "upstream_input_tokens")
	require.Contains(t, sql, "upstream_cache_read_tokens")
	require.Contains(t, sql, "cache_billing_ratio")
	require.Contains(t, sql, "upstream_total_cost")
	require.Contains(t, sql, "CHECK (cache_billing_ratio > 0 AND cache_billing_ratio <= 1)")
}

func TestOpenAICacheBillingAggregateMigration(t *testing.T) {
	sqlBytes, err := FS.ReadFile("901_openai_cache_billing_aggregates.sql")
	require.NoError(t, err)
	sql := string(sqlBytes)

	for _, table := range []string{"usage_dashboard_hourly", "usage_dashboard_daily"} {
		require.Contains(t, sql, "ALTER TABLE "+table)
	}
	for _, column := range []string{"upstream_input_tokens", "upstream_cache_read_tokens", "upstream_cost", "reclassified_cache_tokens"} {
		require.Contains(t, sql, column)
	}
	require.Contains(t, sql, "SET upstream_input_tokens = input_tokens")
	require.Contains(t, sql, "upstream_cache_read_tokens = cache_read_tokens")
	require.Contains(t, sql, "upstream_cost = total_cost")
}

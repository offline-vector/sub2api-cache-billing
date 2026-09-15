package repository

import "fmt"

func usageLogColumn(alias, column string) string {
	if alias == "" {
		return column
	}
	return alias + "." + column
}

// The fork audit columns default to zero for historical rows. New rows can
// carry a distinct upstream view even when the configured ratio is 1 (for
// example ForceCacheBilling after an account switch), so audit presence must
// be detected from the raw fields rather than from cache_billing_ratio alone.
func hasUpstreamBillingAuditExpr(alias string) string {
	ratio := usageLogColumn(alias, "cache_billing_ratio")
	upstreamInput := usageLogColumn(alias, "upstream_input_tokens")
	upstreamCacheRead := usageLogColumn(alias, "upstream_cache_read_tokens")
	upstreamCost := usageLogColumn(alias, "upstream_total_cost")
	return fmt.Sprintf("(%s < 1 OR %s <> 0 OR %s <> 0 OR %s <> 0)", ratio, upstreamInput, upstreamCacheRead, upstreamCost)
}

func upstreamPromptTokensExpr(alias string) string {
	upstreamInput := usageLogColumn(alias, "upstream_input_tokens")
	input := usageLogColumn(alias, "input_tokens")
	cacheCreation := usageLogColumn(alias, "cache_creation_tokens")
	cacheRead := usageLogColumn(alias, "cache_read_tokens")
	return fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s + %s + %s END", hasUpstreamBillingAuditExpr(alias), upstreamInput, input, cacheCreation, cacheRead)
}

func upstreamCacheReadTokensExpr(alias string) string {
	upstreamCacheRead := usageLogColumn(alias, "upstream_cache_read_tokens")
	cacheRead := usageLogColumn(alias, "cache_read_tokens")
	return fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s END", hasUpstreamBillingAuditExpr(alias), upstreamCacheRead, cacheRead)
}

func upstreamUncachedInputTokensExpr(alias string) string {
	return fmt.Sprintf("GREATEST((%s) - %s - (%s), 0)",
		upstreamPromptTokensExpr(alias),
		usageLogColumn(alias, "cache_creation_tokens"),
		upstreamCacheReadTokensExpr(alias),
	)
}

func upstreamTotalCostExpr(alias string) string {
	upstreamCost := usageLogColumn(alias, "upstream_total_cost")
	totalCost := usageLogColumn(alias, "total_cost")
	return fmt.Sprintf("CASE WHEN %s THEN %s ELSE %s END", hasUpstreamBillingAuditExpr(alias), upstreamCost, totalCost)
}

func upstreamAccountCostExpr(alias string) string {
	accountStatsCost := usageLogColumn(alias, "account_stats_cost")
	accountRate := usageLogColumn(alias, "account_rate_multiplier")
	return fmt.Sprintf("COALESCE(%s, %s) * COALESCE(%s, 1)", accountStatsCost, upstreamTotalCostExpr(alias), accountRate)
}

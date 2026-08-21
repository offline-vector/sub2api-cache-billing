package repository

import "fmt"

func usageLogColumn(alias, column string) string {
	if alias == "" {
		return column
	}
	return alias + "." + column
}

// The fork audit columns default to zero for historical rows. Only rows with
// an applied policy ratio below one may use them; every other row falls back to
// the legacy customer columns, which were also the upstream source at the time.
func upstreamPromptTokensExpr(alias string) string {
	ratio := usageLogColumn(alias, "cache_billing_ratio")
	upstreamInput := usageLogColumn(alias, "upstream_input_tokens")
	input := usageLogColumn(alias, "input_tokens")
	cacheCreation := usageLogColumn(alias, "cache_creation_tokens")
	cacheRead := usageLogColumn(alias, "cache_read_tokens")
	return fmt.Sprintf("CASE WHEN %s < 1 THEN %s ELSE %s + %s + %s END", ratio, upstreamInput, input, cacheCreation, cacheRead)
}

func upstreamCacheReadTokensExpr(alias string) string {
	ratio := usageLogColumn(alias, "cache_billing_ratio")
	upstreamCacheRead := usageLogColumn(alias, "upstream_cache_read_tokens")
	cacheRead := usageLogColumn(alias, "cache_read_tokens")
	return fmt.Sprintf("CASE WHEN %s < 1 THEN %s ELSE %s END", ratio, upstreamCacheRead, cacheRead)
}

func upstreamUncachedInputTokensExpr(alias string) string {
	return fmt.Sprintf("GREATEST((%s) - %s - (%s), 0)",
		upstreamPromptTokensExpr(alias),
		usageLogColumn(alias, "cache_creation_tokens"),
		upstreamCacheReadTokensExpr(alias),
	)
}

func upstreamTotalCostExpr(alias string) string {
	ratio := usageLogColumn(alias, "cache_billing_ratio")
	upstreamCost := usageLogColumn(alias, "upstream_total_cost")
	totalCost := usageLogColumn(alias, "total_cost")
	return fmt.Sprintf("CASE WHEN %s < 1 THEN %s ELSE %s END", ratio, upstreamCost, totalCost)
}

func upstreamAccountCostExpr(alias string) string {
	accountStatsCost := usageLogColumn(alias, "account_stats_cost")
	accountRate := usageLogColumn(alias, "account_rate_multiplier")
	return fmt.Sprintf("COALESCE(%s, %s) * COALESCE(%s, 1)", accountStatsCost, upstreamTotalCostExpr(alias), accountRate)
}

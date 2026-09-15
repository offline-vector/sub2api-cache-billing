package repository

import (
	"strings"
	"testing"
)

func TestUpstreamBillingExpressionsUseAuditFieldsAtNeutralRatio(t *testing.T) {
	condition := hasUpstreamBillingAuditExpr("u")
	for _, fragment := range []string{
		"u.cache_billing_ratio < 1",
		"u.upstream_input_tokens <> 0",
		"u.upstream_cache_read_tokens <> 0",
		"u.upstream_total_cost <> 0",
	} {
		if !strings.Contains(condition, fragment) {
			t.Fatalf("audit condition %q missing %q", condition, fragment)
		}
	}

	costExpr := upstreamTotalCostExpr("u")
	if !strings.Contains(costExpr, "THEN u.upstream_total_cost ELSE u.total_cost END") {
		t.Fatalf("upstream cost expression does not preserve neutral-ratio audit: %q", costExpr)
	}

	promptExpr := upstreamPromptTokensExpr("u")
	if !strings.Contains(promptExpr, "THEN u.upstream_input_tokens") {
		t.Fatalf("upstream prompt expression does not preserve neutral-ratio audit: %q", promptExpr)
	}
}

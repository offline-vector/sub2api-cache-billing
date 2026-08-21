package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildUsageBillingCommandKeepsCustomerAndAccountCostsSeparate(t *testing.T) {
	t.Parallel()

	upstreamStandardCost := 1.25
	params := &postUsageBillingParams{
		Cost: &CostBreakdown{
			TotalCost:  1.50,
			ActualCost: 1.50,
		},
		User:                  &User{ID: 1},
		APIKey:                &APIKey{ID: 2, Quota: 100},
		Account:               &Account{ID: 3, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}},
		AccountRateMultiplier: 0.8,
		AccountStandardCost:   &upstreamStandardCost,
	}

	cmd := buildUsageBillingCommand("req-cache-policy", nil, params)
	require.NotNil(t, cmd)
	require.InDelta(t, 1.50, cmd.BalanceCost, 1e-12, "customer balance must use customer-billed cost")
	require.InDelta(t, 1.00, cmd.AccountQuotaCost, 1e-12, "account quota must use upstream standard cost and account multiplier")
}

func TestBuildUsageBillingCommandLegacyAccountCostFallback(t *testing.T) {
	t.Parallel()

	params := &postUsageBillingParams{
		Cost:                  &CostBreakdown{TotalCost: 1.50, ActualCost: 1.50},
		User:                  &User{ID: 1},
		APIKey:                &APIKey{ID: 2},
		Account:               &Account{ID: 3, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}},
		AccountRateMultiplier: 0.8,
	}

	cmd := buildUsageBillingCommand("req-legacy", nil, params)
	require.NotNil(t, cmd)
	require.InDelta(t, 1.20, cmd.AccountQuotaCost, 1e-12)
}

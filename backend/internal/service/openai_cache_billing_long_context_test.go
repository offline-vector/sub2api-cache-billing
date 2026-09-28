//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheBillingAccountStatsPreservesUpstreamBucketsAndLongContextGate(t *testing.T) {
	for _, accountLongContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "account_gate_off", true: "account_gate_on"}[accountLongContext], func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			swapInOpenAILadderCatalog(t, svc)
			svc.cfg.Gateway.OpenAICacheBillingRatio = 0.8
			svc.channelService = newTestChannelServiceForStats(t, &Channel{ID: 1, Status: StatusActive}, 1, PlatformOpenAI)
			key := openAIRecordUsageAPIKeyWithGroup(svc, 1015, true)
			key.GroupID = i64p(1)

			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "cache_account_gate_" + t.Name(),
					Model:     "gpt-5.4-2026-03-05",
					Usage:     OpenAIUsage{InputTokens: 300000, CacheReadInputTokens: 200000, OutputTokens: 2000},
					Duration:  time.Second,
				},
				APIKey:  key,
				User:    &User{ID: 2015},
				Account: &Account{ID: 3015, Platform: PlatformOpenAI, Extra: map[string]any{"openai_long_context_billing_enabled": accountLongContext}},
			})
			require.NoError(t, err)
			require.NotNil(t, logs.lastLog)
			log := logs.lastLog
			require.Equal(t, 160000, log.CacheReadTokens)
			require.Equal(t, 140000, log.InputTokens)
			require.Equal(t, 200000, log.UpstreamCacheReadTokens)
			require.Equal(t, 300000, log.UpstreamInputTokens)
			require.Equal(t, 0.8, log.CacheBillingRatio)
			require.Greater(t, log.TotalCost, log.UpstreamTotalCost)
			wantAccountCost := 100000*2.5e-6 + 200000*0.25e-6 + 2000*15e-6
			if accountLongContext {
				wantAccountCost = (100000*2.5e-6+200000*0.25e-6)*2 + 2000*15e-6*1.5
			}
			require.NotNil(t, log.AccountStatsCost)
			require.InDelta(t, wantAccountCost, *log.AccountStatsCost, 1e-10)
		})
	}
}

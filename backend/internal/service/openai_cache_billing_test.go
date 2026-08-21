package service

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAICacheBillingRatio(t *testing.T) {
	tests := []struct {
		name          string
		usage         OpenAIUsage
		ratio         float64
		wantInput     int
		wantCacheRead int
		wantUpstream  int
		wantRatio     float64
	}{
		{name: "default behavior", usage: OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 80, CacheCreationInputTokens: 5}, ratio: 1, wantInput: 15, wantCacheRead: 80, wantUpstream: 80, wantRatio: 1},
		{name: "sixty percent", usage: OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 80, CacheCreationInputTokens: 5}, ratio: 0.6, wantInput: 47, wantCacheRead: 48, wantUpstream: 80, wantRatio: 0.6},
		{name: "deterministic floor", usage: OpenAIUsage{InputTokens: 10, CacheReadInputTokens: 9}, ratio: 0.6, wantInput: 5, wantCacheRead: 5, wantUpstream: 9, wantRatio: 0.6},
		{name: "no cache", usage: OpenAIUsage{InputTokens: 10}, ratio: 0.6, wantInput: 10, wantCacheRead: 0, wantUpstream: 0, wantRatio: 0.6},
		{name: "cache clamped after creation", usage: OpenAIUsage{InputTokens: 10, CacheReadInputTokens: 20, CacheCreationInputTokens: 4}, ratio: 0.5, wantInput: 3, wantCacheRead: 3, wantUpstream: 6, wantRatio: 0.5},
		{name: "invalid ratio fails closed", usage: OpenAIUsage{InputTokens: 10, CacheReadInputTokens: 8}, ratio: math.NaN(), wantInput: 2, wantCacheRead: 8, wantUpstream: 8, wantRatio: 1},
		{name: "negative upstream values", usage: OpenAIUsage{InputTokens: -10, CacheReadInputTokens: -8, CacheCreationInputTokens: -2}, ratio: 0.6, wantInput: 0, wantCacheRead: 0, wantUpstream: 0, wantRatio: 0.6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyOpenAICacheBillingRatio(tt.usage, tt.ratio)
			if got.BillableInputTokens != tt.wantInput || got.BillableCacheReadTokens != tt.wantCacheRead || got.UpstreamCacheReadTokens != tt.wantUpstream || got.AppliedRatio != tt.wantRatio {
				t.Fatalf("unexpected result: %+v", got)
			}
			if got.BillableInputTokens+got.BillableCacheReadTokens+got.BillableCacheCreationTokens != got.UpstreamInputTokens {
				t.Fatalf("input token invariant broken: %+v", got)
			}
		})
	}
}

func TestApplyGatewayOpenAICacheBillingRatio(t *testing.T) {
	provider := ClaudeUsage{
		InputTokens:              10,
		OutputTokens:             7,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     101,
	}
	billing := provider
	got := applyGatewayOpenAICacheBillingRatio(provider, billing, 0.6)
	if got.UpstreamInputTokens != 131 || got.UpstreamCacheReadTokens != 101 || got.AppliedRatio != 0.6 {
		t.Fatalf("unexpected upstream audit: %+v", got)
	}
	if got.BillableUsage.InputTokens != 51 || got.BillableUsage.CacheReadInputTokens != 60 || got.BillableUsage.CacheCreationInputTokens != 20 {
		t.Fatalf("unexpected billable usage: %+v", got.BillableUsage)
	}
	if got.BillableUsage.InputTokens+got.BillableUsage.CacheCreationInputTokens+got.BillableUsage.CacheReadInputTokens != got.UpstreamInputTokens {
		t.Fatalf("input token invariant broken: %+v", got)
	}
}

func TestApplyGatewayOpenAICacheBillingRatioPreservesProviderAuditForForcedCache(t *testing.T) {
	provider := ClaudeUsage{InputTokens: 100, OutputTokens: 4}
	billing := provider
	billing.CacheReadInputTokens = billing.InputTokens
	billing.InputTokens = 0

	got := applyGatewayOpenAICacheBillingRatio(provider, billing, 0.6)
	if got.UpstreamInputTokens != 100 || got.UpstreamCacheReadTokens != 0 {
		t.Fatalf("forced cache changed provider audit: %+v", got)
	}
	if got.BillableUsage.InputTokens != 40 || got.BillableUsage.CacheReadInputTokens != 60 {
		t.Fatalf("unexpected forced-cache billable usage: %+v", got.BillableUsage)
	}
}

func TestRewriteAnthropicCacheUsageForBilling(t *testing.T) {
	body := []byte(`{"type":"message","usage":{"input_tokens":10,"output_tokens":7,"cache_creation_input_tokens":20,"cache_read_input_tokens":101,"cached_tokens":101},"keep":"yes"}`)
	rewritten, changed := rewriteAnthropicCacheUsageForBilling(body, 0.6)
	if !changed {
		t.Fatal("expected Anthropic usage rewrite")
	}
	if gjson.GetBytes(rewritten, "usage.input_tokens").Int() != 51 ||
		gjson.GetBytes(rewritten, "usage.cache_read_input_tokens").Int() != 60 ||
		gjson.GetBytes(rewritten, "usage.cached_tokens").Int() != 60 ||
		gjson.GetBytes(rewritten, "usage.cache_creation_input_tokens").Int() != 20 ||
		gjson.GetBytes(rewritten, "usage.output_tokens").Int() != 7 ||
		gjson.GetBytes(rewritten, "keep").String() != "yes" {
		t.Fatalf("unexpected Anthropic response rewrite: %s", rewritten)
	}
}

func TestRewriteAnthropicCacheUsageMapForBilling(t *testing.T) {
	usage := map[string]any{
		"input_tokens":                float64(10),
		"output_tokens":               float64(7),
		"cache_creation_input_tokens": float64(20),
		"cache_read_input_tokens":     float64(101),
	}
	if !rewriteAnthropicCacheUsageMapForBilling(usage, 0.6) {
		t.Fatal("expected streaming Anthropic usage rewrite")
	}
	if usage["input_tokens"] != 51 || usage["cache_read_input_tokens"] != 60 || usage["output_tokens"] != float64(7) {
		t.Fatalf("unexpected streaming usage: %#v", usage)
	}
}

func TestGatewayOpenAICacheBillingEligibility(t *testing.T) {
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICacheBillingRatio: 0.6}}}
	openAIAccount := &Account{Platform: PlatformOpenAI}
	if got := svc.openAICacheBillingRatioFor(nil, &ForwardResult{}, openAIAccount); got != 0.6 {
		t.Fatalf("eligible gateway ratio=%v", got)
	}
	for name, result := range map[string]*ForwardResult{
		"image": {ImageCount: 1},
		"audio": {AudioUsage: &AudioUsage{}},
	} {
		if got := svc.openAICacheBillingRatioFor(nil, result, openAIAccount); got != 1 {
			t.Fatalf("%s ratio=%v want=1", name, got)
		}
	}
	if got := svc.openAICacheBillingRatioFor(nil, &ForwardResult{}, &Account{Platform: PlatformAnthropic}); got != 1 {
		t.Fatalf("non-OpenAI ratio=%v want=1", got)
	}
}

func TestRewriteOpenAICacheUsageForBilling(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		ratio      float64
		wantChange bool
		wantPath   string
		wantCache  int64
	}{
		{name: "responses nested", body: `{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":2,"input_tokens_details":{"cached_tokens":81,"other":7}},"keep":"yes"}}`, ratio: 0.6, wantChange: true, wantPath: "response.usage.input_tokens_details.cached_tokens", wantCache: 48},
		{name: "chat nested", body: `{"usage":{"prompt_tokens":50,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":31}}}`, ratio: 0.6, wantChange: true, wantPath: "usage.prompt_tokens_details.cached_tokens", wantCache: 18},
		{name: "wrapped flat aliases", body: `{"data":{"usage":{"input_tokens":20,"cache_read_input_tokens":10,"cache_read_tokens":10,"cached_tokens":10}}}`, ratio: 0.6, wantChange: true, wantPath: "data.usage.cache_read_input_tokens", wantCache: 6},
		{name: "failed event unchanged", body: `{"type":"response.failed","response":{"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":10}}}}`, ratio: 0.6, wantChange: false},
		{name: "ratio one unchanged", body: `{"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":10}}}`, ratio: 1, wantChange: false},
		{name: "invalid json unchanged", body: `{"usage":`, ratio: 0.6, wantChange: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := []byte(tt.body)
			got, changed := rewriteOpenAICacheUsageForBilling(original, tt.ratio)
			if changed != tt.wantChange {
				t.Fatalf("changed=%v want=%v body=%s", changed, tt.wantChange, got)
			}
			if !tt.wantChange {
				if string(got) != tt.body {
					t.Fatalf("unchanged response was modified: %s", got)
				}
				return
			}
			if !json.Valid(got) {
				t.Fatalf("rewritten body is invalid json: %s", got)
			}
			if value := gjson.GetBytes(got, tt.wantPath).Int(); value != tt.wantCache {
				t.Fatalf("cache=%d want=%d body=%s", value, tt.wantCache, got)
			}
			if gjson.GetBytes(got, "response.keep").Exists() && gjson.GetBytes(got, "response.keep").String() != "yes" {
				t.Fatalf("unknown field was not preserved: %s", got)
			}
		})
	}
}

func TestRewriteOpenAICacheUsageInSSEBodyForBilling(t *testing.T) {
	body := []byte("event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":9}}}}\r\n\r\n")
	got, changed := rewriteOpenAICacheUsageInSSEBodyForBilling(body, 0.6)
	if !changed || !bytes.Contains(got, []byte(`"cached_tokens":5`)) {
		t.Fatalf("SSE body was not rewritten: %q", got)
	}
}

func TestClientVisibleCacheMatchesBillableCache(t *testing.T) {
	upstream := OpenAIUsage{InputTokens: 100, CacheReadInputTokens: 81, CacheCreationInputTokens: 5}
	billing := applyOpenAICacheBillingRatio(upstream, 0.6)
	body := []byte(`{"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":81,"cache_write_tokens":5}}}`)
	rewritten, changed := rewriteOpenAICacheUsageForBilling(body, 0.6)
	if !changed {
		t.Fatal("expected client usage rewrite")
	}
	clientUsage, ok := extractOpenAIUsageFromJSONBytes(rewritten)
	if !ok || clientUsage.CacheReadInputTokens != billing.BillableCacheReadTokens {
		t.Fatalf("client cache=%d billable cache=%d", clientUsage.CacheReadInputTokens, billing.BillableCacheReadTokens)
	}
	if clientUsage.InputTokens != upstream.InputTokens {
		t.Fatalf("total input changed: got=%d want=%d", clientUsage.InputTokens, upstream.InputTokens)
	}
}

func TestOpenAICacheBillingEligibility(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICacheBillingRatio: 0.6}}}
	openAIAccount := &Account{Platform: PlatformOpenAI}
	base := &OpenAIForwardResult{}
	if got := svc.openAICacheBillingRatioFor(base, openAIAccount, false); got != 0.6 {
		t.Fatalf("eligible ratio=%v", got)
	}
	for name, result := range map[string]*OpenAIForwardResult{
		"image":       {ImageCount: 1},
		"image token": {Usage: OpenAIUsage{ImageOutputTokens: 1}},
		"video":       {VideoCount: 1},
		"audio":       {AudioUsage: &AudioUsage{}},
		"failed ws":   {OpenAIWSMode: true, UpstreamTerminalEvent: "response.failed"},
	} {
		if got := svc.openAICacheBillingRatioFor(result, openAIAccount, false); got != 1 {
			t.Fatalf("%s ratio=%v want=1", name, got)
		}
	}
	if got := svc.openAICacheBillingRatioFor(base, &Account{Platform: PlatformGrok}, false); got != 1 {
		t.Fatalf("non-OpenAI ratio=%v want=1", got)
	}
	if got := svc.openAICacheBillingRatioFor(base, openAIAccount, true); got != 1 {
		t.Fatalf("cyber ratio=%v want=1", got)
	}
}

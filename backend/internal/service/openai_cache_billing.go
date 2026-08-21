package service

import (
	"bytes"
	"context"
	"math"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const defaultOpenAICacheBillingRatio = 1.0

// openAICacheBillingResult keeps upstream metering separate from the billable
// buckets. Total input is intentionally immutable so context-window decisions
// continue to use the value reported by the upstream.
type openAICacheBillingResult struct {
	UpstreamInputTokens         int
	UpstreamCacheReadTokens     int
	BillableInputTokens         int
	BillableCacheCreationTokens int
	BillableCacheReadTokens     int
	AppliedRatio                float64
}

// gatewayOpenAICacheBillingResult carries the same two metering views for the
// Anthropic-compatible gateway. ClaudeUsage keeps uncached, cache-creation and
// cache-read input in separate buckets, unlike OpenAIUsage whose InputTokens is
// the inclusive prompt total.
type gatewayOpenAICacheBillingResult struct {
	UpstreamInputTokens     int
	UpstreamCacheReadTokens int
	BillableUsage           ClaudeUsage
	AppliedRatio            float64
}

func normalizeOpenAICacheBillingRatio(ratio float64) float64 {
	if ratio <= 0 || ratio > 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return defaultOpenAICacheBillingRatio
	}
	return ratio
}

func applyOpenAICacheBillingRatio(usage OpenAIUsage, ratio float64) openAICacheBillingResult {
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	totalInput := max(usage.InputTokens, 0)
	cacheCreation := min(max(usage.CacheCreationInputTokens, 0), totalInput)
	upstreamCacheRead := min(max(usage.CacheReadInputTokens, 0), totalInput-cacheCreation)
	billableCacheRead := int(math.Floor(float64(upstreamCacheRead) * ratio))
	billableInput := totalInput - cacheCreation - billableCacheRead

	return openAICacheBillingResult{
		UpstreamInputTokens:         totalInput,
		UpstreamCacheReadTokens:     upstreamCacheRead,
		BillableInputTokens:         billableInput,
		BillableCacheCreationTokens: cacheCreation,
		BillableCacheReadTokens:     billableCacheRead,
		AppliedRatio:                ratio,
	}
}

func applyGatewayOpenAICacheBillingRatio(providerUsage, billingUsage ClaudeUsage, ratio float64) gatewayOpenAICacheBillingResult {
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	providerInput := max(providerUsage.InputTokens, 0)
	providerCacheCreation := max(providerUsage.CacheCreationInputTokens, 0)
	providerCacheRead := max(providerUsage.CacheReadInputTokens, 0)

	billingUsage.InputTokens = max(billingUsage.InputTokens, 0)
	billingUsage.CacheCreationInputTokens = max(billingUsage.CacheCreationInputTokens, 0)
	billingUsage.CacheReadInputTokens = max(billingUsage.CacheReadInputTokens, 0)
	billableCacheRead := int(math.Floor(float64(billingUsage.CacheReadInputTokens) * ratio))
	billingUsage.InputTokens += billingUsage.CacheReadInputTokens - billableCacheRead
	billingUsage.CacheReadInputTokens = billableCacheRead

	return gatewayOpenAICacheBillingResult{
		UpstreamInputTokens:     providerInput + providerCacheCreation + providerCacheRead,
		UpstreamCacheReadTokens: providerCacheRead,
		BillableUsage:           billingUsage,
		AppliedRatio:            ratio,
	}
}

func gatewayOpenAICacheBillingEligible(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI
}

func (s *GatewayService) openAICacheBillingRatioFor(ctx context.Context, result *ForwardResult, account *Account) float64 {
	if !gatewayOpenAICacheBillingEligible(account) || result == nil || result.ImageCount > 0 || result.AudioUsage != nil {
		return defaultOpenAICacheBillingRatio
	}
	return s.openAICacheBillingRatioForClient(ctx, account)
}

func (s *GatewayService) openAICacheBillingRatioForClient(ctx context.Context, account *Account) float64 {
	if s == nil || !gatewayOpenAICacheBillingEligible(account) {
		return defaultOpenAICacheBillingRatio
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.settingService != nil {
		return s.settingService.GetOpenAICacheBillingRatio(ctx)
	}
	if s.cfg == nil {
		return defaultOpenAICacheBillingRatio
	}
	return normalizeOpenAICacheBillingRatio(s.cfg.Gateway.OpenAICacheBillingRatio)
}

func rewriteAnthropicCacheUsageMapForBilling(usage map[string]any, ratio float64) bool {
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	if ratio == defaultOpenAICacheBillingRatio || usage == nil {
		return false
	}
	input, inputOK := parseSSEUsageInt(usage["input_tokens"])
	cacheRead, cacheOK := parseSSEUsageInt(usage["cache_read_input_tokens"])
	if !cacheOK {
		cacheRead, cacheOK = parseSSEUsageInt(usage["cached_tokens"])
	}
	if !inputOK || !cacheOK || cacheRead <= 0 {
		return false
	}
	billableCacheRead := int(math.Floor(float64(cacheRead) * ratio))
	usage["input_tokens"] = input + cacheRead - billableCacheRead
	usage["cache_read_input_tokens"] = billableCacheRead
	if _, exists := usage["cached_tokens"]; exists {
		usage["cached_tokens"] = billableCacheRead
	}
	return billableCacheRead != cacheRead
}

func rewriteAnthropicCacheUsageForBilling(body []byte, ratio float64) ([]byte, bool) {
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	if ratio == defaultOpenAICacheBillingRatio || len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}
	input := gjson.GetBytes(body, "usage.input_tokens")
	cacheRead := gjson.GetBytes(body, "usage.cache_read_input_tokens")
	if !cacheRead.Exists() {
		cacheRead = gjson.GetBytes(body, "usage.cached_tokens")
	}
	if !input.Exists() || !cacheRead.Exists() || input.Type != gjson.Number || cacheRead.Type != gjson.Number || cacheRead.Int() <= 0 {
		return body, false
	}
	billableCacheRead := int(math.Floor(float64(cacheRead.Int()) * ratio))
	updated, err := sjson.SetBytes(body, "usage.input_tokens", int(input.Int())+int(cacheRead.Int())-billableCacheRead)
	if err != nil {
		return body, false
	}
	updated, err = sjson.SetBytes(updated, "usage.cache_read_input_tokens", billableCacheRead)
	if err != nil {
		return body, false
	}
	if gjson.GetBytes(updated, "usage.cached_tokens").Exists() {
		updated, err = sjson.SetBytes(updated, "usage.cached_tokens", billableCacheRead)
		if err != nil {
			return body, false
		}
	}
	return updated, billableCacheRead != int(cacheRead.Int())
}

func (s *OpenAIGatewayService) openAICacheBillingRatioFor(result *OpenAIForwardResult, account *Account, cyberBlocked bool) float64 {
	if result == nil || account == nil || account.Platform != PlatformOpenAI || cyberBlocked ||
		!result.SucceededForScheduling() || result.ImageCount > 0 || result.VideoCount > 0 ||
		result.AudioUsage != nil || result.Usage.ImageInputTokens > 0 || result.Usage.ImageOutputTokens > 0 {
		return defaultOpenAICacheBillingRatio
	}
	if s == nil {
		return defaultOpenAICacheBillingRatio
	}
	if s.settingService != nil {
		return s.settingService.GetOpenAICacheBillingRatio(context.Background())
	}
	if s.cfg == nil {
		return defaultOpenAICacheBillingRatio
	}
	return normalizeOpenAICacheBillingRatio(s.cfg.Gateway.OpenAICacheBillingRatio)
}

func (s *OpenAIGatewayService) openAICacheBillingRatioForClient(account *Account) float64 {
	if account == nil || account.Platform != PlatformOpenAI || s == nil {
		return defaultOpenAICacheBillingRatio
	}
	if s.settingService != nil {
		return s.settingService.GetOpenAICacheBillingRatio(context.Background())
	}
	if s.cfg == nil {
		return defaultOpenAICacheBillingRatio
	}
	return normalizeOpenAICacheBillingRatio(s.cfg.Gateway.OpenAICacheBillingRatio)
}

var openAIUsageJSONPaths = [...]string{
	"usage",
	"response.usage",
	"data.usage",
	"data.response.usage",
}

var openAICacheReadJSONPaths = [...]string{
	"input_tokens_details.cached_tokens",
	"prompt_tokens_details.cached_tokens",
	"cache_read_input_tokens",
	"cache_read_tokens",
	"cached_tokens",
}

func openAIEventIsUnbillableTerminal(body []byte) bool {
	switch strings.TrimSpace(gjson.GetBytes(body, "type").String()) {
	case "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "error":
		return true
	default:
		return false
	}
}

// rewriteOpenAICacheUsageForBilling rewrites only the client-facing copy of a
// successful response. The upstream bytes and parsed OpenAIUsage remain the
// audit source. Any malformed field or rewrite error fails closed by returning
// the original document.
func rewriteOpenAICacheUsageForBilling(body []byte, ratio float64) ([]byte, bool) {
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	if ratio == defaultOpenAICacheBillingRatio || len(body) == 0 || !gjson.ValidBytes(body) || openAIEventIsUnbillableTerminal(body) {
		return body, false
	}

	updated := body
	changed := false
	for _, usagePath := range openAIUsageJSONPaths {
		usageValue := gjson.GetBytes(updated, usagePath)
		usage, ok := openAIUsageFromGJSON(usageValue)
		if !ok {
			continue
		}
		billing := applyOpenAICacheBillingRatio(usage, ratio)
		if billing.BillableCacheReadTokens == billing.UpstreamCacheReadTokens {
			continue
		}
		for _, cachePath := range openAICacheReadJSONPaths {
			fullPath := usagePath + "." + cachePath
			field := gjson.GetBytes(updated, fullPath)
			if !field.Exists() || field.Type != gjson.Number {
				continue
			}
			next, err := sjson.SetBytes(updated, fullPath, billing.BillableCacheReadTokens)
			if err != nil {
				return body, false
			}
			updated = next
			changed = true
		}
	}
	return updated, changed
}

func rewriteOpenAICacheUsageInSSEBodyForBilling(body []byte, ratio float64) ([]byte, bool) {
	if normalizeOpenAICacheBillingRatio(ratio) == defaultOpenAICacheBillingRatio || len(body) == 0 {
		return body, false
	}
	lines := bytes.SplitAfter(body, []byte("\n"))
	changed := false
	for i, lineWithEnding := range lines {
		line := bytes.TrimSuffix(lineWithEnding, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		payload, ok := extractOpenAISSEDataLine(string(line))
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		rewritten, didRewrite := rewriteOpenAICacheUsageForBilling([]byte(payload), ratio)
		if !didRewrite {
			continue
		}
		ending := lineWithEnding[len(line):]
		lines[i] = append(append([]byte("data: "), rewritten...), ending...)
		changed = true
	}
	if !changed {
		return body, false
	}
	return bytes.Join(lines, nil), true
}

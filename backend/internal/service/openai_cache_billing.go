package service

import (
	"bytes"
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
		return s.settingService.GetOpenAICacheBillingRatio(nil)
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
		return s.settingService.GetOpenAICacheBillingRatio(nil)
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

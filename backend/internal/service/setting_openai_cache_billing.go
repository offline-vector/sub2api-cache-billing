package service

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
)

const openAICacheBillingRatioDBTimeout = 5 * time.Second
const openAICacheBillingRatioErrorTTL = 5 * time.Second

func (s *SettingService) configuredOpenAICacheBillingRatio() float64 {
	if s == nil || s.cfg == nil {
		return defaultOpenAICacheBillingRatio
	}
	return normalizeOpenAICacheBillingRatio(s.cfg.Gateway.OpenAICacheBillingRatio)
}

func parseStoredOpenAICacheBillingRatio(raw string, missingFallback float64) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return normalizeOpenAICacheBillingRatio(missingFallback)
	}
	ratio, err := strconv.ParseFloat(raw, 64)
	if err != nil || ratio <= 0 || ratio > 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return defaultOpenAICacheBillingRatio
	}
	return ratio
}

func (s *SettingService) storeOpenAICacheBillingRatio(ratio float64) {
	if s == nil {
		return
	}
	ratio = normalizeOpenAICacheBillingRatio(ratio)
	s.openAICacheBillingRatioBits.Store(math.Float64bits(ratio))
	s.openAICacheBillingRatioRetryAt.Store(0)
	s.openAICacheBillingRatioLoaded.Store(true)
}

// GetOpenAICacheBillingRatio returns the persisted operator policy. The first
// request after process start performs one bounded DB read; all later reads are
// atomic and saving the admin setting updates the value immediately.
func (s *SettingService) GetOpenAICacheBillingRatio(ctx context.Context) float64 {
	if s == nil {
		return defaultOpenAICacheBillingRatio
	}
	if s.openAICacheBillingRatioLoaded.Load() {
		return normalizeOpenAICacheBillingRatio(math.Float64frombits(s.openAICacheBillingRatioBits.Load()))
	}
	if retryAt := s.openAICacheBillingRatioRetryAt.Load(); retryAt > time.Now().UnixNano() {
		return normalizeOpenAICacheBillingRatio(math.Float64frombits(s.openAICacheBillingRatioBits.Load()))
	}

	value, _, _ := s.openAICacheBillingRatioSF.Do(SettingKeyOpenAICacheBillingRatio, func() (any, error) {
		if s.openAICacheBillingRatioLoaded.Load() {
			return math.Float64frombits(s.openAICacheBillingRatioBits.Load()), nil
		}
		if s.settingRepo == nil {
			ratio := s.configuredOpenAICacheBillingRatio()
			s.storeOpenAICacheBillingRatio(ratio)
			return ratio, nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICacheBillingRatioDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICacheBillingRatio)
		if errors.Is(err, ErrSettingNotFound) {
			ratio := s.configuredOpenAICacheBillingRatio()
			s.storeOpenAICacheBillingRatio(ratio)
			return ratio, nil
		}
		if err != nil {
			ratio := s.configuredOpenAICacheBillingRatio()
			s.openAICacheBillingRatioBits.Store(math.Float64bits(ratio))
			s.openAICacheBillingRatioRetryAt.Store(time.Now().Add(openAICacheBillingRatioErrorTTL).UnixNano())
			slog.Warn("failed to load OpenAI cache billing ratio; using configured fallback", "error", err)
			return ratio, nil
		}
		ratio := parseStoredOpenAICacheBillingRatio(raw, s.configuredOpenAICacheBillingRatio())
		s.storeOpenAICacheBillingRatio(ratio)
		return ratio, nil
	})
	if ratio, ok := value.(float64); ok {
		return normalizeOpenAICacheBillingRatio(ratio)
	}
	return s.configuredOpenAICacheBillingRatio()
}

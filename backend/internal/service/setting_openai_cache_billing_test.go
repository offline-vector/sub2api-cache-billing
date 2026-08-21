package service

import (
	"context"
	"math"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type cacheBillingSettingRepoStub struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *cacheBillingSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (r *cacheBillingSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}
func (r *cacheBillingSettingRepoStub) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}
func (r *cacheBillingSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (r *cacheBillingSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}
func (r *cacheBillingSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}
func (r *cacheBillingSettingRepoStub) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func TestOpenAICacheBillingRatioLoadsAndUpdatesImmediately(t *testing.T) {
	repo := &cacheBillingSettingRepoStub{values: map[string]string{SettingKeyOpenAICacheBillingRatio: "0.90"}}
	svc := &SettingService{settingRepo: repo, cfg: &config.Config{Gateway: config.GatewayConfig{OpenAICacheBillingRatio: 1}}}
	require.Equal(t, 0.9, svc.GetOpenAICacheBillingRatio(context.Background()))

	settings := &SystemSettings{OpenAICacheBillingRatio: 0.86}
	updates, err := svc.buildSystemSettingsUpdates(context.Background(), settings)
	require.NoError(t, err)
	require.Equal(t, "0.86", updates[SettingKeyOpenAICacheBillingRatio])
	require.NoError(t, repo.SetMultiple(context.Background(), map[string]string{SettingKeyOpenAICacheBillingRatio: "0.86"}))
	svc.storeOpenAICacheBillingRatio(settings.OpenAICacheBillingRatio)
	require.Equal(t, 0.86, svc.GetOpenAICacheBillingRatio(context.Background()))
}

func TestOpenAICacheBillingRatioRejectsInvalidAdminValues(t *testing.T) {
	svc := &SettingService{}
	for _, ratio := range []float64{0, -0.1, 1.01, math.NaN(), math.Inf(1)} {
		_, err := svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{OpenAICacheBillingRatio: ratio})
		require.Error(t, err, "ratio=%v", ratio)
	}
}

func TestParseStoredOpenAICacheBillingRatioFailsClosed(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1.1", "NaN", "+Inf", "not-a-number"} {
		require.Equal(t, 1.0, parseStoredOpenAICacheBillingRatio(raw, 0.86), "raw=%s", raw)
	}
	require.Equal(t, 0.86, parseStoredOpenAICacheBillingRatio("", 0.86))
}

package repository

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSharedProbeTurnStateCache_ReadExpiryAndMalformed(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(service.OpenAIProbeTurnStateReader)
	ctx := context.Background()
	key := "openai:probe-turn-state:v1:10:gpt-6-astra"
	value := &service.OpenAIProbeTurnState{AccountID: 10, Model: "gpt-6-astra", State: strings.Repeat("s", 292), Source: "synthetic_probe", CrossSessionVerified: true, IssuedAtMS: time.Now().Add(-time.Minute).UnixMilli(), ExpiresAtMS: time.Now().Add(time.Minute).UnixMilli()}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, client.Set(ctx, key, raw, time.Minute).Err())
	got, err := cache.GetOpenAIProbeTurnState(ctx, 10, "gpt-6-astra")
	require.NoError(t, err)
	require.Equal(t, value, got)
	for _, tc := range []struct {
		account int64
		model   string
	}{{11, "gpt-6-astra"}, {10, "gpt-5.6-sol"}, {10, "gpt-6-astra:injected"}} {
		got, err = cache.GetOpenAIProbeTurnState(ctx, tc.account, tc.model)
		require.NoError(t, err)
		require.Nil(t, got)
	}
	mr.FastForward(time.Minute)
	got, err = cache.GetOpenAIProbeTurnState(ctx, 10, "gpt-6-astra")
	require.NoError(t, err)
	require.Nil(t, got)
	for _, bad := range []string{"raw_SECRET_invalid_json", strings.Repeat("secret", 400)} {
		require.NoError(t, client.Set(ctx, key, bad, time.Minute).Err())
		got, err = cache.GetOpenAIProbeTurnState(ctx, 10, "gpt-6-astra")
		require.Error(t, err)
		require.Nil(t, got)
		require.NotContains(t, err.Error(), bad)
	}
}

func TestSharedProbeTurnStateCache_UnresponsiveRedisIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var wg sync.WaitGroup
	stopped := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		<-stopped
	}()
	t.Cleanup(func() { close(stopped); _ = listener.Close(); wg.Wait() })
	client := redis.NewClient(&redis.Options{Addr: listener.Addr().String(), ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, PoolSize: 1})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client).(service.OpenAIProbeTurnStateReader)
	start := time.Now()
	value, err := cache.GetOpenAIProbeTurnState(context.Background(), 10, "gpt-6-astra")
	require.Error(t, err)
	require.Nil(t, value)
	require.Less(t, time.Since(start), 750*time.Millisecond, "unresponsive optional cache must not inherit the 10s global timeout")
}

func TestSharedProbeTurnStateCache_PublisherNeverRenewsEcho(t *testing.T) {
	// Execute the exact worker Lua against an isolated Redis-compatible server.
	source, err := os.ReadFile("../../../deploy/diagnostics/turn_state_probe.py")
	require.NoError(t, err)
	_, rest, ok := strings.Cut(string(source), "PUBLISH_STATE_LUA = '''")
	require.True(t, ok)
	lua, _, ok := strings.Cut(rest, "'''")
	require.True(t, ok)
	mr := miniredis.RunT(t)
	mr.SetTime(time.Now())
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	key := "openai:probe-turn-state:v1:10:gpt-6-astra"
	now := time.Now().UnixMilli()
	v := service.OpenAIProbeTurnState{AccountID: 10, Model: "gpt-6-astra", State: strings.Repeat("s", 292), Source: "synthetic_probe", CrossSessionVerified: true, IssuedAtMS: now - 60000, ExpiresAtMS: now + 3540000}
	publish := func(seen string) int64 {
		raw, e := json.Marshal(v)
		require.NoError(t, e)
		n, e := client.Eval(ctx, lua, []string{key, seen}, string(raw)).Int64()
		require.NoError(t, e)
		return n
	}
	require.EqualValues(t, 1, publish(key+":seen:first"))
	before, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	v.IssuedAtMS += 30000
	v.ExpiresAtMS += 30000
	require.EqualValues(t, 0, publish(key+":seen:first"))
	after, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, before, after)
	// A restart/re-observation after expiry is still rejected via the tombstone.
	mr.SetTime(time.Now().Add(2 * time.Hour))
	mr.FastForward(2 * time.Hour)
	v.IssuedAtMS = now + int64(2*time.Hour/time.Millisecond)
	v.ExpiresAtMS = v.IssuedAtMS + 3600000
	require.EqualValues(t, -2, publish(key+":seen:first"))
	v.State = strings.Repeat("n", 292)
	require.EqualValues(t, 1, publish(key+":seen:new"))
}

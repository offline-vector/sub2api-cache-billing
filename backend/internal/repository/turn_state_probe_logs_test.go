package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestTurnStateProbeLogsReaderBoundsAndPrivacy(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	reader := NewGatewayCache(client).(service.TurnStateProbeLogReader)
	status, items, err := reader.ReadTurnStateProbeLogs(ctx)
	require.NoError(t, err)
	require.Nil(t, status)
	require.Empty(t, items)
	require.NoError(t, client.Set(ctx, "openai:probe-admin:v1:status", `{"running":true,"state":"SECRET","credentials":"SECRET"}`, time.Minute).Err())
	raw := `{"id":"` + uuid.NewString() + `","time":"2026-09-17T12:00:00Z","received_state_length":312,"state":"SECRET","prompt":"SECRET","credentials":"SECRET"}`
	require.NoError(t, client.RPush(ctx, "openai:probe-admin:v1:events", "{broken", strings.Repeat("x", 8193), `{"id":"invalid"}`, raw).Err())
	status, items, err = reader.ReadTurnStateProbeLogs(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, 312, *items[0].ReceivedStateLength)
	encoded, err := json.Marshal(service.TurnStateProbeLogPage{Status: status, Items: items})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SECRET")
	require.NotContains(t, string(encoded), "credentials")
	for i := 0; i < 2010; i++ {
		require.NoError(t, client.LPush(ctx, "openai:probe-admin:v1:events", raw).Err())
	}
	_, items, err = reader.ReadTurnStateProbeLogs(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2000)
}

func TestTurnStateProbeLogsExporterLuaTTLAndOrder(t *testing.T) {
	source, err := os.ReadFile("../../../deploy/diagnostics/turn_state_log_export.py")
	require.NoError(t, err)
	_, rest, ok := strings.Cut(string(source), "EXPORT_LUA = '''")
	require.True(t, ok)
	lua, _, ok := strings.Cut(rest, "'''")
	require.True(t, ok)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	keys := []string{"openai:probe-admin:v1:events", "openai:probe-admin:v1:status"}
	snapshot := `{"events":[{"id":"first"},{"id":"second"}],"status":{"running":true}}`
	n, err := client.Eval(ctx, lua, keys, snapshot).Int()
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, 45*time.Second, mr.TTL(keys[1]))
	require.Equal(t, 7*24*time.Hour, mr.TTL(keys[0]))
	first, err := client.LIndex(ctx, keys[0], 0).Result()
	require.NoError(t, err)
	require.Contains(t, first, "first")
	_, err = client.Eval(ctx, lua, keys, snapshot).Result()
	require.NoError(t, err)
	require.EqualValues(t, 2, client.LLen(ctx, keys[0]).Val(), "refresh must not duplicate rows")
	mr.FastForward(time.Minute)
	require.False(t, mr.Exists(keys[1]))
	require.True(t, mr.Exists(keys[0]))
}

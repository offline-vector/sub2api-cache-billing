package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTurnStateIsolation_ExactTokenAndModel(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a, b := &Account{ID: 1}, &Account{ID: 2}
	stateA, stateB := strings.Repeat("a", 292), strings.Repeat("b", 312)
	svc.noteOpenAICodexTurnStateProvenance(c, a, stateA)
	// A later response on B must not reattribute A's token to B.
	svc.noteOpenAICodexTurnStateProvenance(c, b, stateB)
	for _, tc := range []struct {
		name, state, model string
		account            *Account
		want               bool
	}{
		{"292_same_origin", stateA, "model-a", a, true},
		{"312_same_origin", stateB, "model-a", b, true},
		{"old_token_after_failover", stateA, "model-a", b, false},
		{"different_model", stateA, "model-b", a, false},
		{"missing_model", stateA, "", a, false},
		{"unseen_292", strings.Repeat("x", 292), "model-a", a, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.Set(openAITurnStateModelContextKey, tc.model)
			h := http.Header{}
			h.Set(openAIWSTurnStateHeader, tc.state)
			svc.guardOpenAICodexTurnStateEcho(c, tc.account, h)
			if tc.want {
				require.Equal(t, tc.state, h.Get(openAIWSTurnStateHeader))
			} else {
				require.Empty(t, h.Get(openAIWSTurnStateHeader))
			}
		})
	}
}

func TestTurnStateIsolation_ClientBoundariesAndExpiry(t *testing.T) {
	svc := &OpenAIGatewayService{}
	a := &Account{ID: 1}
	c, _ := newTurnStateTestContext(t, 7, "session")
	c.Request.Header.Set("thread-id", "parent")
	svc.noteOpenAICodexTurnStateProvenance(c, a, "state")
	for _, tc := range []struct {
		key             int64
		session, thread string
	}{
		{8, "session", "parent"}, {7, "session", "child"}, {7, "other", ""},
	} {
		other, _ := newTurnStateTestContext(t, tc.key, tc.session)
		other.Request.Header.Set("thread-id", tc.thread)
		h := http.Header{}
		h.Set(openAIWSTurnStateHeader, "state")
		svc.guardOpenAICodexTurnStateEcho(other, a, h)
		require.Empty(t, h.Get(openAIWSTurnStateHeader))
	}
	key := openAITurnStateOriginKey(c, "state")
	raw, _ := svc.openaiCodexTurnStateOrigins.Load(key)
	origin := raw.(openAICodexTurnStateOrigin)
	origin.expiresAt = time.Now().Add(-time.Second)
	svc.openaiCodexTurnStateOrigins.Store(key, origin)
	svc.noteOpenAICodexTurnStateProvenance(c, a, "state")
	h := http.Header{}
	h.Set(openAIWSTurnStateHeader, "state")
	svc.guardOpenAICodexTurnStateEcho(c, a, h)
	require.Empty(t, h.Get(openAIWSTurnStateHeader), "re-observing the same state must not renew its lifetime")
}

func TestTurnStateIsolation_HTTPUsesActualUpstreamModel(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://example.com", "api_key": "test"}}
	c, _ := newTurnStateTestContext(t, 7, "session")
	c.Set(openAITurnStateModelContextKey, "upstream-a")
	state := preferredTurnStateFixture("state")
	svc.noteOpenAICodexTurnStateProvenance(c, a, state)
	c.Request.Header.Set(openAIWSTurnStateHeader, state)
	for _, model := range []string{"upstream-a", "upstream-b"} {
		body := []byte(`{"model":"` + model + `","input":"test"}`)
		req, err := svc.buildUpstreamRequest(context.Background(), c, a, body, "test", false, "", false)
		require.NoError(t, err)
		if model == "upstream-a" {
			require.Equal(t, state, req.Header.Get(openAIWSTurnStateHeader))
		} else {
			require.Empty(t, req.Header.Get(openAIWSTurnStateHeader))
		}
		require.Equal(t, model, openAITurnStateModel(c))
	}
}

func TestTurnStateIsolation_WSCacheAndLifetime(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	store.BindSessionTurnState(1, "scope", "old", time.Hour, 10, "model-a")
	for _, tc := range []struct {
		group, account int64
		scope, model   string
	}{
		{2, 10, "scope", "model-a"}, {1, 11, "scope", "model-a"}, {1, 10, "scope", "model-b"}, {1, 10, "other", "model-a"},
	} {
		_, ok := store.GetSessionTurnState(tc.group, tc.scope, tc.account, tc.model)
		require.False(t, ok)
	}
	key := openAIWSSessionTurnStateKey(1, "scope")
	store.sessionToTurnStateMu.Lock()
	binding := store.sessionToTurnState[key]
	binding.expiresAt = time.Now().Add(-time.Second)
	store.sessionToTurnState[key] = binding
	store.sessionToTurnStateMu.Unlock()
	store.BindSessionTurnState(1, "scope", "old", time.Hour, 10, "model-a")
	_, ok := store.GetSessionTurnState(1, "scope", 10, "model-a")
	require.False(t, ok)
	// A genuinely different upstream token starts a new lifetime, regardless of length.
	newState := strings.Repeat("n", 312)
	store.BindSessionTurnState(1, "scope", newState, time.Hour, 10, "model-a")
	got, ok := store.GetSessionTurnState(1, "scope", 10, "model-a")
	require.True(t, ok)
	require.Equal(t, newState, got)
	store.DeleteSessionTurnState(1, "scope")
	_, ok = store.GetSessionTurnState(1, "scope", 10, "model-a")
	require.False(t, ok)
}

func TestTurnStateIsolation_CompatModelAndIndependentExpiry(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 10}
	ctx := context.Background()
	state := preferredTurnStateFixture("state")
	svc.bindOpenAICompatSessionTurnState(ctx, c, a, "prompt", state, "model-a")
	require.Equal(t, state, svc.getOpenAICompatSessionTurnState(ctx, c, a, "prompt", "model-a"))
	require.Empty(t, svc.getOpenAICompatSessionTurnState(ctx, c, a, "prompt", "model-b"))
	require.Empty(t, svc.getOpenAICompatSessionTurnState(ctx, c, &Account{ID: 11}, "prompt", "model-a"))
	other, _ := newTurnStateTestContext(t, 8, "session")
	require.Empty(t, svc.getOpenAICompatSessionTurnState(ctx, other, a, "prompt", "model-a"))
	thread, _ := newTurnStateTestContext(t, 7, "session")
	thread.Request.Header.Set("thread-id", "other")
	require.Empty(t, svc.getOpenAICompatSessionTurnState(ctx, thread, a, "prompt", "model-a"))
	key := openAICompatTurnStateKey(c, a, "prompt", "model-a")
	raw, _ := svc.openaiCompatSessionResponses.Load(key)
	binding := raw.(openAICompatSessionResponseBinding)
	binding.ExpiresAt = time.Now().Add(-time.Second)
	svc.openaiCompatSessionResponses.Store(key, binding)
	svc.bindOpenAICompatSessionResponseID(ctx, c, a, "prompt", "resp_next")
	svc.bindOpenAICompatSessionTurnState(ctx, c, a, "prompt", state, "model-a")
	require.Empty(t, svc.getOpenAICompatSessionTurnState(ctx, c, a, "prompt", "model-a"))
}

func TestTurnStateIsolation_PoolCompatibility(t *testing.T) {
	a := openAIWSAcquireRequest{Account: &Account{ID: 1}, WSURL: "wss://example.com", TurnStateScope: "key7-threadA", TurnStateModel: "model-a"}
	b := cloneOpenAIWSAcquireRequest(a)
	require.Equal(t, openAIWSAcquireCompatibility(a), openAIWSAcquireCompatibility(b))
	b.TurnStateModel = "model-b"
	require.NotEqual(t, openAIWSAcquireCompatibility(a), openAIWSAcquireCompatibility(b))
	require.False(t, sameOpenAIWSPrewarmTarget(a, b))
	b = cloneOpenAIWSAcquireRequest(a)
	b.TurnStateScope = "key8-threadA"
	require.NotEqual(t, openAIWSAcquireCompatibility(a), openAIWSAcquireCompatibility(b))
	require.False(t, sameOpenAIWSPrewarmTarget(a, b))
}

func TestTurnStateIsolation_PrewarmedHandshakeCapturedOnce(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	scope := openAICodexTurnStateSeed(c)
	store := NewOpenAIWSStateStore(nil)
	headers := http.Header{}
	headers.Set(openAIWSTurnStateHeader, "prewarmed-state")
	conn := newOpenAIWSConn("prewarmed", 1, nil, headers)
	issuedAt := time.Now().Add(-10 * time.Minute)
	conn.createdAtNano.Store(issuedAt.UnixNano())
	lease := &openAIWSConnLease{conn: conn, reused: true}
	require.Equal(t, "prewarmed-state", svc.captureOpenAIWSHandshakeTurnState(c, a, lease, store, 1, scope, "model-a"))
	raw, ok := svc.openaiCodexTurnStateOrigins.Load(openAITurnStateOriginKey(c, "prewarmed-state"))
	require.True(t, ok)
	origin := raw.(openAICodexTurnStateOrigin)
	require.WithinDuration(t, issuedAt.Add(time.Hour), origin.expiresAt, time.Millisecond)
	// An old pooled handshake must not overwrite a newer HTTP/WS state.
	store.BindSessionTurnState(1, scope, "new-state", time.Hour, a.ID, "model-a")
	require.Empty(t, svc.captureOpenAIWSHandshakeTurnState(c, a, lease, store, 1, scope, "model-a"))
	got, ok := store.GetSessionTurnState(1, scope, a.ID, "model-a")
	require.True(t, ok)
	require.Equal(t, "new-state", got)
	staleConn := newOpenAIWSConn("expired", 1, nil, headers)
	staleConn.createdAtNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	require.Empty(t, svc.captureOpenAIWSHandshakeTurnState(c, a, &openAIWSConnLease{conn: staleConn}, store, 1, scope, "model-a"))
}

func TestTurnStateIsolation_ChainUpdatesWithoutLengthSelection(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	for _, length := range []int{292, 312, 292} {
		state := strings.Repeat("s", length)
		upstream := http.Header{}
		upstream.Set(openAIWSTurnStateHeader, state)
		svc.relayOpenAICodexTurnState(c, a, upstream)
		require.Equal(t, state, c.Writer.Header().Get(openAIWSTurnStateHeader))
		outgoing := http.Header{}
		outgoing.Set(openAIWSTurnStateHeader, state)
		svc.guardOpenAICodexTurnStateEcho(c, a, outgoing)
		require.Equal(t, state, outgoing.Get(openAIWSTurnStateHeader))
	}
	// Deliberately absent client state stays absent (new client turn semantics).
	outgoing := http.Header{}
	svc.guardOpenAICodexTurnStateEcho(c, a, outgoing)
	require.Empty(t, outgoing.Get(openAIWSTurnStateHeader))
}

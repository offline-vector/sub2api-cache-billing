package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type probeStateReaderStub struct {
	GatewayCache
	value *OpenAIProbeTurnState
	err   error
	calls int
}

func (r *probeStateReaderStub) GetOpenAIProbeTurnState(ctx context.Context, account int64, model string) (*OpenAIProbeTurnState, error) {
	r.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("shared read must be bounded")
	}
	return r.value, r.err // deliberately does not filter: gateway must validate
}

func verifiedProbeFixture() *OpenAIProbeTurnState {
	now := time.Now().Add(-time.Minute)
	return &OpenAIProbeTurnState{AccountID: 10, Model: "gpt-6-astra", State: preferredTurnStateFixture("synthetic"), Source: "synthetic_probe", ProviderAccountHash: openAITurnStateDigest("test-account"), IssuedAtMS: now.UnixMilli(), ExpiresAtMS: now.Add(time.Hour).UnixMilli(), CrossSessionVerified: true}
}

func TestSharedProbeTurnState_SyntheticSeedAvailableToIndependentClients(t *testing.T) {
	r := &probeStateReaderStub{value: verifiedProbeFixture()}
	cfg := &config.Config{}
	cfg.Gateway.SharedProbeTurnStateEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, cache: r}
	a := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	for _, key := range []int64{7, 8} {
		c, _ := newTurnStateTestContext(t, key, "different-user-session")
		c.Set(openAITurnStateModelContextKey, "gpt-6-astra")
		h := http.Header{}
		svc.selectPreferredOpenAITurnState(c, a, h)
		require.Equal(t, r.value.State, h.Get(openAICodexTurnStateHeader))
		svc.noteOpenAICodexTurnStateProvenance(c, a, r.value.State) // upstream echoes it
		_, imported := svc.openaiPreferredTurnStates.Load(preferredOpenAITurnStateKey(c, a))
		require.False(t, imported, "shared pool revocation must not be hidden by session cache")
		origin, _ := svc.openaiCodexTurnStateOrigins.Load(openAITurnStateOriginKey(c, r.value.State))
		require.Equal(t, r.value.ExpiresAtMS, origin.(openAICodexTurnStateOrigin).expiresAt.UnixMilli())
	}
	require.Equal(t, 2, r.calls)
	// Real-user 292 stays private and takes precedence in its own session only.
	c, _ := newTurnStateTestContext(t, 7, "user-private-session")
	c.Set(openAITurnStateModelContextKey, "gpt-6-astra")
	private := preferredTurnStateFixture("user-private")
	svc.noteOpenAICodexTurnStateProvenance(c, a, private)
	h := http.Header{}
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Equal(t, private, h.Get(openAICodexTurnStateHeader))
	other, _ := newTurnStateTestContext(t, 8, "user-private-session")
	other.Set(openAITurnStateModelContextKey, "gpt-6-astra")
	h.Set(openAICodexTurnStateHeader, private)
	svc.selectPreferredOpenAITurnState(other, a, h)
	require.Equal(t, r.value.State, h.Get(openAICodexTurnStateHeader))
	require.NotEqual(t, private, h.Get(openAICodexTurnStateHeader))
	// Revocation takes effect at the next lookup, with no retained global seed.
	r.value = nil
	svc.selectPreferredOpenAITurnState(other, a, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestSharedProbeTurnState_RejectsInvalidRecordAndFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*OpenAIProbeTurnState)
	}{
		{"other_account", func(v *OpenAIProbeTurnState) { v.AccountID++ }},
		{"other_model", func(v *OpenAIProbeTurnState) { v.Model = "gpt-5.6-sol" }},
		{"unverified", func(v *OpenAIProbeTurnState) { v.CrossSessionVerified = false }},
		{"user_origin", func(v *OpenAIProbeTurnState) { v.Source = "user" }},
		{"312", func(v *OpenAIProbeTurnState) { v.State = strings.Repeat("a", 312) }},
		{"header_injection", func(v *OpenAIProbeTurnState) { v.State = "\r\n" + strings.Repeat("a", 290) }},
		{"expired", func(v *OpenAIProbeTurnState) { v.ExpiresAtMS = time.Now().Add(-time.Second).UnixMilli() }},
		{"future", func(v *OpenAIProbeTurnState) { v.IssuedAtMS = time.Now().Add(time.Minute).UnixMilli() }},
		{"extended_ttl", func(v *OpenAIProbeTurnState) { v.ExpiresAtMS = v.IssuedAtMS + int64(2*time.Hour/time.Millisecond) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &probeStateReaderStub{value: verifiedProbeFixture()}
			tc.edit(r.value)
			cfg := &config.Config{}
			cfg.Gateway.SharedProbeTurnStateEnabled = true
			svc := &OpenAIGatewayService{cfg: cfg, cache: r}
			c, _ := newTurnStateTestContext(t, 7, "session")
			c.Set(openAITurnStateModelContextKey, "gpt-6-astra")
			h := http.Header{}
			svc.selectPreferredOpenAITurnState(c, &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}, h)
			require.Empty(t, h.Get(openAICodexTurnStateHeader))
		})
	}
	r := &probeStateReaderStub{value: verifiedProbeFixture(), err: errors.New("redis unavailable")}
	cfg := &config.Config{}
	cfg.Gateway.SharedProbeTurnStateEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, cache: r}
	c, _ := newTurnStateTestContext(t, 7, "session")
	c.Set(openAITurnStateModelContextKey, "gpt-6-astra")
	h := http.Header{}
	a := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	cfg.Gateway.SharedProbeTurnStateEnabled = false
	r.err = nil
	before := r.calls
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Equal(t, before, r.calls)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
}

func TestSharedProbeTurnState_RealHTTPBuilderKeepsSessionAndPayloadIndependent(t *testing.T) {
	r := &probeStateReaderStub{value: verifiedProbeFixture()}
	cfg := &config.Config{}
	cfg.Gateway.SharedProbeTurnStateEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, cache: r}
	a := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	var sessions []string
	for _, key := range []int64{7, 8} {
		c, _ := newTurnStateTestContext(t, key, "same-declared-session")
		body := []byte(`{"model":"gpt-6-astra","input":"private user payload","previous_response_id":"resp_private"}`)
		req, err := svc.buildUpstreamRequest(context.Background(), c, a, body, "test-token", false, "same-declared-session", false)
		require.NoError(t, err)
		require.Equal(t, r.value.State, req.Header.Get(openAICodexTurnStateHeader))
		actual, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.JSONEq(t, string(body), string(actual))
		sessions = append(sessions, req.Header.Get("session_id"))
	}
	require.NotEmpty(t, sessions[0])
	require.NotEqual(t, sessions[0], sessions[1])
	// API-key accounts must never receive a ChatGPT OAuth probe state.
	a.Type = AccountTypeAPIKey
	before := r.calls
	c, _ := newTurnStateTestContext(t, 7, "session")
	c.Set(openAITurnStateModelContextKey, "gpt-6-astra")
	h := http.Header{}
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	require.Equal(t, before, r.calls)
}

func TestSharedProbeTurnState_WSAndPassthroughBuilders(t *testing.T) {
	r := &probeStateReaderStub{value: verifiedProbeFixture()}
	cfg := &config.Config{}
	cfg.Gateway.SharedProbeTurnStateEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg, cache: r}
	a := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	c, _ := newTurnStateTestContext(t, 7, "client-session")
	headers, session, err := svc.buildOpenAIWSHeaders(context.Background(), c, a, "test-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, "", "", "", "gpt-6-astra", "")
	require.NoError(t, err)
	require.Equal(t, r.value.State, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "client-session", session.SessionID)
	require.NotEqual(t, session.SessionID, headers.Get("session_id"))
	body := []byte(`{"model":"gpt-6-astra","input":"private"}`)
	req, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, a, body, "test-token")
	require.NoError(t, err)
	require.Equal(t, r.value.State, req.Header.Get(openAICodexTurnStateHeader))
	// Same local row but a different provider identity must reject old state.
	a.Credentials["chatgpt_account_id"] = "replacement-account"
	req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, a, body, "test-token")
	require.NoError(t, err)
	require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
}

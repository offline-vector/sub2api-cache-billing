package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func preferredTurnStateFixture(label string) string {
	return label + strings.Repeat("x", openAIPreferredTurnStateLength-len(label))
}

func TestPreferredTurnState_Retains292Across312AndUpdatesToNew292(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	old, next := preferredTurnStateFixture("old"), preferredTurnStateFixture("new")
	issued := time.Now().Add(-time.Minute)
	svc.noteOpenAICodexTurnStateProvenanceAt(c, a, old, issued)
	svc.noteOpenAICodexTurnStateProvenance(c, a, strings.Repeat("b", 312))
	h := http.Header{}
	h.Set(openAIWSTurnStateHeader, strings.Repeat("b", 312))
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Equal(t, old, h.Get(openAIWSTurnStateHeader))
	// A genuinely new 292 replaces the preference; a client echo of the older
	// token must not roll it back. Empty client headers use the same preference.
	svc.noteOpenAICodexTurnStateProvenance(c, a, next)
	for _, incoming := range []string{old, "", strings.Repeat("b", 312)} {
		h.Set(openAIWSTurnStateHeader, incoming)
		svc.selectPreferredOpenAITurnState(c, a, h)
		require.Equal(t, next, h.Get(openAIWSTurnStateHeader))
	}
	svc.noteOpenAICodexTurnStateProvenanceAt(c, a, old, issued)
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Equal(t, next, h.Get(openAIWSTurnStateHeader))
}

func TestPreferredTurnState_NoForeignUnknownOrExpiredToken(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	state := preferredTurnStateFixture("known")
	svc.noteOpenAICodexTurnStateProvenance(c, a, state)
	for _, tc := range []struct {
		key, account   int64
		session, model string
	}{
		{8, 1, "session", "model-a"}, {7, 2, "session", "model-a"},
		{7, 1, "other", "model-a"}, {7, 1, "session", "model-b"},
	} {
		other, _ := newTurnStateTestContext(t, tc.key, tc.session)
		other.Set(openAITurnStateModelContextKey, tc.model)
		h := http.Header{}
		h.Set(openAIWSTurnStateHeader, state)
		svc.selectPreferredOpenAITurnState(other, &Account{ID: tc.account}, h)
		require.Empty(t, h.Get(openAIWSTurnStateHeader))
	}
	key := preferredOpenAITurnStateKey(c, a)
	raw, _ := svc.openaiPreferredTurnStates.Load(key)
	pref := raw.(preferredOpenAITurnState)
	pref.origin.expiresAt = time.Now().Add(-time.Second)
	svc.openaiPreferredTurnStates.Store(key, pref)
	svc.openaiCodexTurnStateOrigins.Store(openAITurnStateOriginKey(c, state), pref.origin)
	svc.noteOpenAICodexTurnStateProvenance(c, a, state)
	h := http.Header{}
	h.Set(openAIWSTurnStateHeader, state)
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Empty(t, h.Get(openAIWSTurnStateHeader))
	svc.noteOpenAICodexTurnStateProvenance(c, a, strings.Repeat("b", 312))
	h.Set(openAIWSTurnStateHeader, strings.Repeat("b", 312))
	svc.selectPreferredOpenAITurnState(c, a, h)
	require.Empty(t, h.Get(openAIWSTurnStateHeader), "only 312 available: omit state")
}

func TestPreferredTurnState_ReobservingIdentical292DoesNotRenew(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	state := preferredTurnStateFixture("same")
	svc.noteOpenAICodexTurnStateProvenanceAt(c, a, state, time.Now().Add(-time.Minute))
	key := preferredOpenAITurnStateKey(c, a)
	before, _ := svc.openaiPreferredTurnStates.Load(key)
	svc.noteOpenAICodexTurnStateProvenance(c, a, state)
	after, _ := svc.openaiPreferredTurnStates.Load(key)
	require.Equal(t, before, after)
}

func TestPreferredTurnState_CompatKeeps292WhenUpstreamReturns312(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "session")
	a := &Account{ID: 1}
	ctx := context.Background()
	old, next := preferredTurnStateFixture("old"), preferredTurnStateFixture("next")
	svc.bindOpenAICompatSessionTurnState(ctx, c, a, "prompt", old, "model-a")
	svc.bindOpenAICompatSessionTurnState(ctx, c, a, "prompt", strings.Repeat("b", 312), "model-a")
	require.Equal(t, old, svc.getOpenAICompatSessionTurnState(ctx, c, a, "prompt", "model-a"))
	svc.bindOpenAICompatSessionTurnState(ctx, c, a, "prompt", next, "model-a")
	require.Equal(t, next, svc.getOpenAICompatSessionTurnState(ctx, c, a, "prompt", "model-a"))
}

package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OpenAIProbeTurnState contains only state from synthetic, content-free probes.
// Never publish user responses, sessions, continuation IDs or conversation data.
type OpenAIProbeTurnState struct {
	AccountID            int64  `json:"account_id"`
	Model                string `json:"model"`
	State                string `json:"state"`
	Source               string `json:"source"`
	IssuedAtMS           int64  `json:"issued_at_ms"`
	ExpiresAtMS          int64  `json:"expires_at_ms"`
	CrossSessionVerified bool   `json:"cross_session_verified"`
	ProviderAccountHash  string `json:"provider_account_hash"`
}

// Optional capability: the gateway may only read this pool, never populate it
// from real user traffic. Only the separate synthetic worker is a writer.
type OpenAIProbeTurnStateReader interface {
	GetOpenAIProbeTurnState(context.Context, int64, string) (*OpenAIProbeTurnState, error)
}

func (v *OpenAIProbeTurnState) ValidFor(accountID int64, model string, now time.Time) bool {
	if v == nil || v.AccountID != accountID || v.Model != model || v.Source != "synthetic_probe" || !v.CrossSessionVerified {
		return false
	}
	if model != "gpt-6-astra" && model != "gpt-5.6-sol" {
		return false
	}
	if len(v.State) != openAIPreferredTurnStateLength || strings.ContainsAny(v.State, "\r\n\x00") {
		return false
	}
	issued, expires := time.UnixMilli(v.IssuedAtMS), time.UnixMilli(v.ExpiresAtMS)
	return v.IssuedAtMS > 0 && !issued.After(now) && expires.After(now) && expires.After(issued) && expires.Sub(issued) <= time.Hour
}

// Shared state is only an outbound bootstrap header. It never changes the
// client's session identity, previous_response_id, payload or socket ownership.
func (s *OpenAIGatewayService) selectSharedProbeTurnState(c *gin.Context, account *Account, headers http.Header) bool {
	if s == nil || s.cfg == nil || !s.cfg.Gateway.SharedProbeTurnStateEnabled || c == nil || c.Request == nil || account == nil || !account.IsOpenAIOAuth() || headers == nil {
		return false
	}
	reader, ok := s.cache.(OpenAIProbeTurnStateReader)
	if !ok {
		return false
	}
	model := openAITurnStateModel(c)
	if model != "gpt-6-astra" && model != "gpt-5.6-sol" {
		return false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 150*time.Millisecond)
	defer cancel()
	value, err := reader.GetOpenAIProbeTurnState(ctx, account.ID, model)
	if err != nil || !value.ValidFor(account.ID, model, time.Now()) || account.GetChatGPTAccountID() == "" || value.ProviderAccountHash != openAITurnStateDigest(account.GetChatGPTAccountID()) {
		return false
	}
	headers.Set(openAICodexTurnStateHeader, value.State)
	// Remember the ORIGINAL issue/expiry before an upstream echo can be seen.
	// An echoed shared seed must not become a freshly issued private 292.
	if key := openAITurnStateOriginKey(c, value.State); key != "" {
		s.openaiCodexTurnStateOrigins.LoadOrStore(key, openAICodexTurnStateOrigin{accountID: account.ID, model: model, createdAt: time.UnixMilli(value.IssuedAtMS), expiresAt: time.UnixMilli(value.ExpiresAtMS), source: "synthetic_probe"})
		s.sweepOpenAICodexTurnStateOrigins()
	}
	if count := s.openaiSharedProbeTurnStateReads.Add(1); count <= 5 || count%128 == 0 {
		logger.L().Info("openai shared probe turn state selected", zap.Int64("account_id", account.ID), zap.String("upstream_model", model), zap.Int("state_length", len(value.State)), zap.Uint64("selection_count", count))
	}
	// Do not import it into per-client retention: doing so would hide
	// revocation/updates in Redis behind a per-session one-hour cache.
	return true
}

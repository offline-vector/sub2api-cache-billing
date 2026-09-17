package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// openAICodexTurnStateHeader 是 Codex 的回合状态头。上游在响应头中铸造该
// 不透明 blob，客户端在同一回合的后续请求中原样回带（codex-rs 侧从
// /responses SSE、/responses/compact JSON 与 WS 握手三种响应中捕获，见
// codex-api/src/sse/responses.rs 与 endpoint/compact.rs）。
const openAICodexTurnStateHeader = "x-codex-turn-state"

// Each opaque token is attributed to its issuing account and actual upstream
// model, within the client's execution scope. A session's most recent account
// is insufficient: concurrent requests can return states from different accounts.
type openAICodexTurnStateOrigin struct {
	accountID int64
	model     string
	createdAt time.Time
	expiresAt time.Time
	source    string
}

// A local retention policy, not a claim about upstream compute or quality.
const openAIPreferredTurnStateLength = 292

type preferredOpenAITurnState struct {
	state  string
	origin openAICodexTurnStateOrigin
}

func preferredOpenAITurnStateKey(c *gin.Context, account *Account) string {
	scope, model := openAICodexTurnStateSeed(c), openAITurnStateModel(c)
	if account == nil || account.ID <= 0 || scope == "" || model == "" {
		return ""
	}
	return fmt.Sprintf("%d:%q:%q", account.ID, model, scope)
}

func (s *OpenAIGatewayService) retainPreferredOpenAITurnState(c *gin.Context, account *Account, state string, origin openAICodexTurnStateOrigin) {
	key := preferredOpenAITurnStateKey(c, account)
	if s == nil || key == "" || len(state) != openAIPreferredTurnStateLength || origin.source == "synthetic_probe" || origin.accountID != account.ID || origin.model != openAITurnStateModel(c) || !time.Now().Before(origin.expiresAt) {
		return
	}
	next := preferredOpenAITurnState{state: state, origin: origin}
	for {
		raw, loaded := s.openaiPreferredTurnStates.LoadOrStore(key, next)
		if !loaded {
			return
		}
		old := raw.(preferredOpenAITurnState)
		// Re-observing old bytes or checking out an older pooled handshake must
		// not renew the lifetime or displace a newer 292 token.
		if old.state == state || !origin.createdAt.After(old.origin.createdAt) {
			return
		}
		if s.openaiPreferredTurnStates.CompareAndSwap(key, old, next) {
			return
		}
	}
}

// Outbound policy: only send an unexpired, upstream-observed 292 token for this
// exact account/model/client scope. A 312 response never overwrites the retained
// 292. No preferred token means no state header, not reuse of an expired token.
func (s *OpenAIGatewayService) selectPreferredOpenAITurnState(c *gin.Context, account *Account, h http.Header) {
	if s == nil || h == nil {
		return
	}
	s.guardOpenAICodexTurnStateEcho(c, account, h)
	// A client echo of a synthetic seed must still consult the shared pool so
	// removal/update there is not bypassed by client headers or local provenance.
	if raw, ok := s.openaiCodexTurnStateOrigins.Load(openAITurnStateOriginKey(c, extractOpenAICodexTurnState(h))); ok && raw.(openAICodexTurnStateOrigin).source == "synthetic_probe" {
		h.Del(openAICodexTurnStateHeader)
	}
	if len(extractOpenAICodexTurnState(h)) != openAIPreferredTurnStateLength {
		h.Del(openAICodexTurnStateHeader)
	}
	key := preferredOpenAITurnStateKey(c, account)
	if key == "" {
		h.Del(openAICodexTurnStateHeader)
		s.selectSharedProbeTurnState(c, account, h)
		return
	}
	if raw, ok := s.openaiPreferredTurnStates.Load(key); ok {
		preferred := raw.(preferredOpenAITurnState)
		if time.Now().Before(preferred.origin.expiresAt) {
			h.Set(openAICodexTurnStateHeader, preferred.state)
			logger.L().Debug("openai preferred turn state selected", zap.Int64("account_id", account.ID), zap.String("upstream_model", preferred.origin.model), zap.String("state_digest", openAITurnStateDigest(preferred.state)[:16]), zap.Float64("state_age_seconds", time.Since(preferred.origin.createdAt).Seconds()))
			// Sparse, non-secret production evidence without changing log levels.
			if count := s.openaiPreferredTurnStateSends.Add(1); count <= 3 || count%128 == 0 {
				logger.L().Info("openai turn state policy applied", zap.String("policy", "prefer_292"), zap.Int("state_length", len(preferred.state)), zap.Int64("account_id", account.ID), zap.String("upstream_model", preferred.origin.model), zap.Uint64("selection_count", count))
			}
		} else {
			s.openaiPreferredTurnStates.CompareAndDelete(key, preferred)
		}
	}
	if extractOpenAICodexTurnState(h) == "" {
		s.selectSharedProbeTurnState(c, account, h)
	}
}

const (
	openAITurnStateModelContextKey = "openai_turn_state_upstream_model"
	openAITurnStateScopeContextKey = "openai_turn_state_scope"
)

func openAITurnStatePoolScope(c *gin.Context, scope string) string {
	if scope != "" {
		return scope
	}
	// Anonymous requests do not share cached state. Keep even their continuation
	// sockets isolated by API key; fresh unscoped requests also force a new dial.
	return fmt.Sprintf("anonymous-key:%d", getAPIKeyIDFromContext(c))
}

func openAITurnStateDigest(state string) string {
	digest := sha256.Sum256([]byte(state))
	return hex.EncodeToString(digest[:])
}

func openAITurnStateOriginKey(c *gin.Context, state string) string {
	seed := openAICodexTurnStateSeed(c)
	if seed == "" || state == "" {
		return ""
	}
	return seed + "\x00" + openAITurnStateDigest(state)
}

func openAITurnStateModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(openAITurnStateModelContextKey)
}

// Prefer the original (pre-namespace-rewrite) execution scope. The header-only
// fallback uses the same derivation, so HTTP and WS agree on token ownership.
func openAICodexTurnStateSeed(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if scope := c.GetString(openAITurnStateScopeContextKey); scope != "" {
		return scope
	}
	scope, _ := resolveOpenAIWSExecutionScope(c, nil, getAPIKeyIDFromContext(c))
	return scope
}

// relayOpenAICodexTurnState 将上游响应中的 turn-state 显式写入下游响应头，
// 并记录铸造账号。必须在响应头提交点调用（WriteHeader 之前、且确认本次
// 上游响应就是将要写回客户端的响应之后）。上游无该头时主动清除 writer 上
// 可能残留的上一 failover attempt 的值——否则换号后旧账号的 blob 会粘到
// 新账号的响应上，这正是本文件要防止的跨账号矛盾。
func (s *OpenAIGatewayService) relayOpenAICodexTurnState(c *gin.Context, account *Account, upstream http.Header) {
	if c == nil || c.Writer == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		c.Writer.Header().Del(canonical)
		return
	}
	c.Writer.Header().Set(canonical, state)
	s.noteOpenAICodexTurnStateProvenance(c, account, state)
}

// stageOpenAICodexTurnState 将上游 turn-state 暂存到延迟提交的响应头集合
// （首输出守卫路径先缓存头、见到首个输出事件才提交）。此处**不**记录铸造
// 账号：该 attempt 仍可能在首输出超时后 failover，暂存头会被整体丢弃，
// 客户端从未收到该 blob。溯源必须在真正提交时记录，见
// noteStagedOpenAICodexTurnStateCommitted。
func stageOpenAICodexTurnState(dst *http.Header, upstream http.Header) {
	if dst == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		if *dst != nil {
			dst.Del(canonical)
		}
		return
	}
	if *dst == nil {
		*dst = http.Header{}
	}
	dst.Set(canonical, state)
}

// noteStagedOpenAICodexTurnStateCommitted 在暂存响应头真正写入下游时记录
// 铸造账号——只有此刻客户端才确定收到了该 blob，溯源表才与客户端持有的
// 值一致（否则被 failover 丢弃的 attempt 会污染溯源，导致后续误剥离）。
func (s *OpenAIGatewayService) noteStagedOpenAICodexTurnStateCommitted(c *gin.Context, account *Account, staged http.Header) {
	if staged == nil || strings.TrimSpace(staged.Get(openAICodexTurnStateHeader)) == "" {
		return
	}
	s.noteOpenAICodexTurnStateProvenance(c, account, extractOpenAICodexTurnState(staged))
}

func extractOpenAICodexTurnState(upstream http.Header) string {
	if upstream == nil {
		return ""
	}
	return strings.TrimSpace(upstream.Get(openAICodexTurnStateHeader))
}

// noteOpenAICodexTurnStateProvenance records scope + token digest -> issuer.
func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenance(c *gin.Context, account *Account, state string) {
	s.noteOpenAICodexTurnStateProvenanceAt(c, account, state, time.Now())
}

func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenanceAt(c *gin.Context, account *Account, state string, issuedAt time.Time) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	key := openAITurnStateOriginKey(c, state)
	model := openAITurnStateModel(c)
	if key == "" || model == "" {
		return
	}
	// An identical token is not a newly issued token. Never extend its lifetime
	// on a replayed response or a pooled connection's old handshake headers.
	actual, loaded := s.openaiCodexTurnStateOrigins.LoadOrStore(key, openAICodexTurnStateOrigin{
		accountID: account.ID,
		model:     model,
		createdAt: issuedAt,
		expiresAt: issuedAt.Add(s.openAIWSSessionStickyTTL()),
	})
	s.retainPreferredOpenAITurnState(c, account, state, actual.(openAICodexTurnStateOrigin))
	if !loaded {
		logger.L().Debug("openai turn state captured", zap.Int64("account_id", account.ID), zap.String("upstream_model", model), zap.Int("state_length", len(state)), zap.String("state_digest", openAITurnStateDigest(state)[:16]))
	}
	s.sweepOpenAICodexTurnStateOrigins()
}

// Observe a handshake once, including when the first client receives a prewarmed
// connection. Its lifetime starts at the dial, never at pool checkout.
func (s *OpenAIGatewayService) captureOpenAIWSHandshakeTurnState(c *gin.Context, account *Account, lease *openAIWSConnLease, store OpenAIWSStateStore, groupID int64, scope, model string) string {
	if s == nil || account == nil || lease == nil || lease.conn == nil {
		return ""
	}
	state := strings.TrimSpace(lease.HandshakeHeader(openAIWSTurnStateHeader))
	issuedAt := lease.conn.createdAt()
	remaining := time.Until(issuedAt.Add(s.openAIWSSessionStickyTTL()))
	if state == "" || issuedAt.IsZero() || remaining <= 0 || !lease.conn.turnStateCaptured.CompareAndSwap(false, true) {
		return ""
	}
	if store != nil && scope != "" {
		store.BindSessionTurnState(groupID, scope, state, remaining, account.ID, model)
	}
	s.noteOpenAICodexTurnStateProvenanceAt(c, account, state, issuedAt)
	return state
}

// Only echo a known, unexpired token from this scope, account and upstream model.
// Unknown tokens (including after a process restart) start a fresh chain. HTTP
// clients own turn semantics: do not inject a cached state when they omit it.
func (s *OpenAIGatewayService) guardOpenAICodexTurnStateEcho(c *gin.Context, account *Account, h http.Header) {
	if s == nil || h == nil || account == nil {
		return
	}
	state := extractOpenAICodexTurnState(h)
	if state == "" {
		return
	}
	key := openAITurnStateOriginKey(c, state)
	raw, _ := s.openaiCodexTurnStateOrigins.Load(key)
	origin, known := raw.(openAICodexTurnStateOrigin)
	model := openAITurnStateModel(c)
	valid := key != "" && known && model != "" && origin.model == model && origin.accountID == account.ID && time.Now().Before(origin.expiresAt)
	if !valid {
		h.Del(openAICodexTurnStateHeader)
	}
	age := float64(0)
	if known && !origin.createdAt.IsZero() {
		age = time.Since(origin.createdAt).Seconds()
	}
	logger.L().Debug("openai turn state echo", zap.Int64("account_id", account.ID), zap.String("upstream_model", model), zap.Bool("accepted", valid), zap.Bool("known", known), zap.Float64("state_age_seconds", age), zap.Int("state_length", len(state)), zap.String("state_digest", openAITurnStateDigest(state)[:16]))
}

// sweepOpenAICodexTurnStateOrigins 机会式清扫过期溯源记录：每 256 次写入
// 全量遍历一轮，防止仅靠读侧惰性删除导致的慢泄漏（会话键无上界）。
func (s *OpenAIGatewayService) sweepOpenAICodexTurnStateOrigins() {
	if s.openaiCodexTurnStateWrites.Add(1)%256 != 0 {
		return
	}
	now := time.Now()
	s.openaiCodexTurnStateOrigins.Range(func(key, value any) bool {
		origin, ok := value.(openAICodexTurnStateOrigin)
		if !ok || (!origin.expiresAt.IsZero() && now.After(origin.expiresAt)) {
			s.openaiCodexTurnStateOrigins.Delete(key)
		}
		return true
	})
	s.openaiPreferredTurnStates.Range(func(key, value any) bool {
		preferred := value.(preferredOpenAITurnState)
		if !now.Before(preferred.origin.expiresAt) {
			s.openaiPreferredTurnStates.CompareAndDelete(key, value)
		}
		return true
	})
}

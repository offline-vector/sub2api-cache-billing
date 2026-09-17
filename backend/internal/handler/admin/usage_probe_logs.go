package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *UsageHandler) SetTurnStateProbeLogService(s *service.TurnStateProbeLogService) {
	h.probeLogs = s
}

// ListTurnStateProbeLogs is registered only inside the admin-authenticated group.
// There is deliberately no endpoint for arbitrary request/proxy/header injection.
func (h *UsageHandler) ListTurnStateProbeLogs(c *gin.Context) {
	var accountID int64
	if raw := c.Query("account_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			response.BadRequest(c, "Invalid account_id")
			return
		}
		accountID = parsed
	}
	page, err := h.probeLogs.List(c.Request.Context(), accountID, c.Query("model"), c.Query("before"))
	if errors.Is(err, service.ErrInvalidProbeLogFilter) {
		response.BadRequest(c, "Invalid probe log filter")
		return
	}
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Probe logs temporarily unavailable")
		return
	}
	response.Success(c, page)
}

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUsageProbeLogsBadFiltersAndUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &UsageHandler{}
	router := gin.New()
	router.GET("/probe-logs", h.ListTurnStateProbeLogs)
	for _, tc := range []struct {
		query string
		code  int
	}{
		{"?account_id=-1", 400}, {"?account_id=abc", 400}, {"?model=unknown", 400}, {"?before=bad", 400}, {"", 503},
	} {
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/probe-logs"+tc.query, nil))
		require.Equal(t, tc.code, out.Code)
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/probe-logs", nil))
	require.Equal(t, 404, out.Code, "no mutation/request trigger endpoint")
}

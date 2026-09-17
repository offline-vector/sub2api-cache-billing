package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTurnStateAuditLengthsOnly(t *testing.T) {
	sent := http.Header{http.CanonicalHeaderKey(openAICodexTurnStateHeader): {strings.Repeat("s", 292)}}
	received := http.Header{http.CanonicalHeaderKey(openAICodexTurnStateHeader): {strings.Repeat("r", 312)}}
	audit := observeTurnStateHeaders(sent, received, "http")
	require.Equal(t, 292, audit.SentLength)
	require.Equal(t, 312, audit.ReceivedLength)
	encoded, err := json.Marshal(audit)
	require.NoError(t, err)
	require.JSONEq(t, `{"sent_length":292,"received_length":312,"transport":"http"}`, string(encoded))
	require.Nil(t, observedHTTPTurnState(nil))
	require.Nil(t, observedHTTPTurnState(&http.Response{}))
	require.Equal(t, &TurnStateAudit{Transport: "http"}, observedHTTPTurnState(&http.Response{Request: &http.Request{}}))
}

func TestTurnStateAuditPooledConnectionKeepsActualHandshake(t *testing.T) {
	lease := &openAIWSConnLease{conn: &openAIWSConn{turnStateAudit: &TurnStateAudit{ReceivedLength: 292, Transport: "ws_handshake"}}}
	audit := lease.turnStateAudit()
	require.Zero(t, audit.SentLength) // A preferred state selected later was not sent on this socket.
	require.Equal(t, 292, audit.ReceivedLength)
	audit.SentLength = 312
	require.Zero(t, lease.turnStateAudit().SentLength)
	require.Nil(t, (&openAIWSConnLease{}).turnStateAudit())
}

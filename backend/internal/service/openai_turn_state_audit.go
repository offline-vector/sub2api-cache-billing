package service

import "net/http"

// TurnStateAudit contains lengths only, never the opaque state or a digest.
// Nil on a usage row means unobserved/historical, not an absent header.
type TurnStateAudit struct {
	SentLength     int    `json:"sent_length"`
	ReceivedLength int    `json:"received_length"`
	Transport      string `json:"transport"`
}

func observeTurnStateHeaders(sent, received http.Header, transport string) *TurnStateAudit {
	return &TurnStateAudit{
		SentLength:     len(extractOpenAICodexTurnState(sent)),
		ReceivedLength: len(extractOpenAICodexTurnState(received)),
		Transport:      transport,
	}
}

func observedHTTPTurnState(resp *http.Response) *TurnStateAudit {
	if resp == nil || resp.Request == nil {
		return nil
	}
	return observeTurnStateHeaders(resp.Request.Header, resp.Header, "http")
}

func (l *openAIWSConnLease) turnStateAudit() *TurnStateAudit {
	if l == nil || l.conn == nil || l.conn.turnStateAudit == nil {
		return nil
	}
	audit := *l.conn.turnStateAudit
	return &audit
}

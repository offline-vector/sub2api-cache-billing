package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Explicit metadata-only projection. Unknown JSON fields (including future raw
// state, prompts and credentials) cannot be returned by the admin API.
type TurnStateProbeEvent struct {
	ID                        string   `json:"id"`
	Time                      string   `json:"time"`
	Event                     string   `json:"event,omitempty"`
	AccountID                 int64    `json:"account_id,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Route                     string   `json:"route,omitempty"`
	Attempt                   int      `json:"attempt,omitempty"`
	SentStateLength           *int     `json:"sent_state_length,omitempty"`
	ReceivedStateLength       *int     `json:"received_state_length,omitempty"`
	HTTPStatus                *int     `json:"http_status,omitempty"`
	ElapsedSeconds            *float64 `json:"elapsed_s,omitempty"`
	Completed                 bool     `json:"completed"`
	ResponseModel             string   `json:"response_model,omitempty"`
	ErrorCode                 string   `json:"error_code,omitempty"`
	ErrorType                 string   `json:"error_type,omitempty"`
	TransientError            string   `json:"transient_error,omitempty"`
	UpstreamServer            string   `json:"upstream_server,omitempty"`
	RetryAfterSeconds         *float64 `json:"retry_after_s,omitempty"`
	WaitSeconds               *float64 `json:"wait_seconds,omitempty"`
	CrossSessionCheck         bool     `json:"cross_session_check,omitempty"`
	SharedStatePublished      bool     `json:"shared_state_published,omitempty"`
	VerificationOutputMatches *bool    `json:"verification_output_matches,omitempty"`
}

type TurnStateProbeWorkerStatus struct {
	Running         bool     `json:"running"`
	UpdatedAt       string   `json:"updated_at"`
	AccountIDs      []int64  `json:"account_ids"`
	Models          []string `json:"models"`
	Concurrency     int      `json:"concurrency"`
	IntervalSeconds float64  `json:"interval_seconds"`
}

type TurnStateProbeLogPage struct {
	Status         *TurnStateProbeWorkerStatus `json:"status"`
	Items          []TurnStateProbeEvent       `json:"items"`
	NextBefore     string                      `json:"next_before,omitempty"`
	RetentionLimit int                         `json:"retention_limit"`
}

type TurnStateProbeLogReader interface {
	ReadTurnStateProbeLogs(context.Context) (*TurnStateProbeWorkerStatus, []TurnStateProbeEvent, error)
}

type TurnStateProbeLogService struct{ reader TurnStateProbeLogReader }

func NewTurnStateProbeLogService(cache GatewayCache) *TurnStateProbeLogService {
	reader, _ := cache.(TurnStateProbeLogReader)
	return &TurnStateProbeLogService{reader: reader}
}

var ErrInvalidProbeLogFilter = errors.New("invalid probe log filter")
var ErrProbeLogsUnavailable = errors.New("probe logs unavailable")

func (s *TurnStateProbeLogService) List(ctx context.Context, accountID int64, model, before string) (*TurnStateProbeLogPage, error) {
	if accountID < 0 || (model != "" && model != "gpt-6-astra" && model != "gpt-5.6-sol") {
		return nil, ErrInvalidProbeLogFilter
	}
	if before != "" {
		if _, err := uuid.Parse(before); err != nil {
			return nil, ErrInvalidProbeLogFilter
		}
	}
	if s == nil || s.reader == nil {
		return nil, ErrProbeLogsUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	status, events, err := s.reader.ReadTurnStateProbeLogs(ctx)
	if err != nil {
		return nil, ErrProbeLogsUnavailable
	}
	page := &TurnStateProbeLogPage{Status: status, Items: []TurnStateProbeEvent{}, RetentionLimit: 2000}
	found := before == ""
	for _, event := range events {
		if !found {
			if event.ID == before {
				found = true
			}
			continue
		}
		if accountID > 0 && event.AccountID != accountID || model != "" && event.Model != model {
			continue
		}
		if len(page.Items) == 50 {
			page.NextBefore = page.Items[49].ID
			break
		}
		page.Items = append(page.Items, event)
	}
	return page, nil
}

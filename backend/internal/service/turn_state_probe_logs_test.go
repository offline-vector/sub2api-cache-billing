package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type probeLogReaderStub struct {
	events []TurnStateProbeEvent
	err    error
}

func (r probeLogReaderStub) ReadTurnStateProbeLogs(context.Context) (*TurnStateProbeWorkerStatus, []TurnStateProbeEvent, error) {
	return &TurnStateProbeWorkerStatus{Running: true}, r.events, r.err
}

func TestTurnStateProbeLogsFiltersAndPagination(t *testing.T) {
	rows := []TurnStateProbeEvent{}
	for i := 0; i < 120; i++ {
		rows = append(rows, TurnStateProbeEvent{ID: uuid.NewString(), AccountID: int64(i%2 + 1), Model: "gpt-6-astra"})
	}
	svc := &TurnStateProbeLogService{reader: probeLogReaderStub{events: rows}}
	first, err := svc.List(context.Background(), 1, "gpt-6-astra", "")
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.Equal(t, rows[98].ID, first.NextBefore)
	next, err := svc.List(context.Background(), 1, "gpt-6-astra", first.NextBefore)
	require.NoError(t, err)
	require.Len(t, next.Items, 10)
	require.Empty(t, next.NextBefore)
	require.Equal(t, rows[100].ID, next.Items[0].ID)
	for _, e := range append(first.Items, next.Items...) {
		require.EqualValues(t, 1, e.AccountID)
	}
	empty, err := svc.List(context.Background(), 0, "gpt-5.6-sol", "")
	require.NoError(t, err)
	require.Empty(t, empty.Items)
	require.NotNil(t, empty.Items)
	// A rotated-out cursor yields an empty page, never silently repeats page one.
	empty, err = svc.List(context.Background(), 0, "", uuid.NewString())
	require.NoError(t, err)
	require.Empty(t, empty.Items)
}

func TestTurnStateProbeLogsValidationAndUnavailable(t *testing.T) {
	svc := &TurnStateProbeLogService{reader: probeLogReaderStub{err: errors.New("private credential")}}
	for _, tc := range []struct {
		account       int64
		model, before string
	}{{-1, "", ""}, {1, "unknown", ""}, {0, "", "invalid"}} {
		_, err := svc.List(context.Background(), tc.account, tc.model, tc.before)
		require.ErrorIs(t, err, ErrInvalidProbeLogFilter)
	}
	_, err := svc.List(context.Background(), 0, "", "")
	require.ErrorIs(t, err, ErrProbeLogsUnavailable)
	require.NotContains(t, err.Error(), "private credential")
	_, err = (*TurnStateProbeLogService)(nil).List(context.Background(), 0, "", "")
	require.ErrorIs(t, err, ErrProbeLogsUnavailable)
}

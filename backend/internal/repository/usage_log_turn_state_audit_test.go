package repository

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type turnStateAuditScanner struct{ value sql.NullString }

func (s turnStateAuditScanner) Scan(dest ...any) error {
	columns := strings.Split(usageLogSelectColumns, ", ")
	for i, target := range dest {
		v := reflect.ValueOf(target).Elem()
		v.Set(reflect.Zero(v.Type()))
		if columns[i] == "turn_state_audit" {
			v.Set(reflect.ValueOf(s.value))
		}
	}
	return nil
}

func TestUsageLogTurnStateAuditRoundTrip(t *testing.T) {
	audit := &service.TurnStateAudit{SentLength: 292, ReceivedLength: 312, Transport: "ws_handshake"}
	prepared := prepareUsageLogInsert(&service.UsageLog{TurnStateAudit: audit})
	columns := strings.Split(usageLogSelectColumns, ", ")[1:] // id is not inserted
	index := -1
	for i, name := range columns {
		if name == "turn_state_audit" {
			index = i
		}
	}
	require.NotEqual(t, -1, index)
	require.Equal(t, "jsonb", usageLogInsertArgTypes[index])
	encoded := prepared.args[index].(string)
	decoded, err := scanUsageLog(turnStateAuditScanner{sql.NullString{String: encoded, Valid: true}})
	require.NoError(t, err)
	require.Equal(t, audit, decoded.TurnStateAudit)
	unknown, err := scanUsageLog(turnStateAuditScanner{})
	require.NoError(t, err)
	require.Nil(t, unknown.TurnStateAudit)
	require.Nil(t, prepareUsageLogInsert(&service.UsageLog{}).args[index])
	query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
	require.Contains(t, query, "turn_state_audit")
	require.Contains(t, args, encoded)
}

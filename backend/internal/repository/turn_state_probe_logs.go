package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var _ service.TurnStateProbeLogReader = (*gatewayCache)(nil)

func (c *gatewayCache) ReadTurnStateProbeLogs(ctx context.Context) (*service.TurnStateProbeWorkerStatus, []service.TurnStateProbeEvent, error) {
	if c == nil || c.rdb == nil {
		return nil, nil, errors.New("probe log cache unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return nil, nil, ctx.Err()
	}
	client := c.rdb.WithTimeout(timeout)
	pipe := client.Pipeline()
	statusCmd := pipe.Get(ctx, "openai:probe-admin:v1:status")
	eventsCmd := pipe.LRange(ctx, "openai:probe-admin:v1:events", 0, 1999)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, err
	}
	var status *service.TurnStateProbeWorkerStatus
	if raw, e := statusCmd.Bytes(); e == nil && len(raw) <= 16384 {
		var decoded service.TurnStateProbeWorkerStatus
		if json.Unmarshal(raw, &decoded) == nil {
			status = &decoded
		}
	}
	rows, err := eventsCmd.Result()
	if err != nil {
		return nil, nil, err
	}
	items := make([]service.TurnStateProbeEvent, 0, len(rows))
	for _, raw := range rows {
		if len(raw) > 8192 {
			continue
		}
		var event service.TurnStateProbeEvent
		if json.Unmarshal([]byte(raw), &event) != nil {
			continue
		}
		if _, err := uuid.Parse(event.ID); err != nil {
			continue
		}
		items = append(items, event)
	}
	return status, items, nil
}

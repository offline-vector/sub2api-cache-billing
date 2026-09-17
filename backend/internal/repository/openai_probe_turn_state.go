package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var _ service.OpenAIProbeTurnStateReader = (*gatewayCache)(nil)

func (c *gatewayCache) GetOpenAIProbeTurnState(ctx context.Context, accountID int64, model string) (*service.OpenAIProbeTurnState, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || (model != "gpt-6-astra" && model != "gpt-5.6-sol") {
		return nil, nil
	}
	key := fmt.Sprintf("openai:probe-turn-state:v1:%d:%s", accountID, model)
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	timeout := time.Until(deadline)
	if timeout <= 0 {
		return nil, ctx.Err()
	}
	// The shared Redis client does not enable ContextTimeoutEnabled. Bound
	// socket I/O on a pool-sharing clone too, without changing global timeouts.
	raw, err := c.rdb.WithTimeout(timeout).Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 2048 {
		return nil, errors.New("probe state record too large")
	}
	var value service.OpenAIProbeTurnState
	if json.Unmarshal(raw, &value) != nil {
		return nil, errors.New("invalid probe state record")
	}
	return &value, nil
}

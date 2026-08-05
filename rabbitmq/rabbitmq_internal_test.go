package rabbitmq

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStartConsumer_CtxCancelledWhileWaitingForConsumeMuDoesNotBlockForever
// locks in the fix for consumeMu blocking Subscribe/SubscribeWithOptions
// indefinitely, ignoring the caller's ctx, when another goroutine is
// stalled holding it across a slow Qos/Consume broker round trip.
func TestStartConsumer_CtxCancelledWhileWaitingForConsumeMuDoesNotBlockForever(t *testing.T) {
	r := NewEventBus(nil)

	// Simulate another goroutine holding the Qos+Consume lock, as if its own
	// broker RPC had stalled.
	r.consumeMu <- struct{}{}
	defer func() { <-r.consumeMu }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := r.startConsumer(ctx, "queue", 0)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

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

// TestStartConsumer_RejectsPlainConsumerAfterPoolClaimedChannelQos locks in
// the fix for a plain (prefetch == 0) consumer silently inheriting a
// SubscribeWithOptions call's leaked channel-wide prefetch default: once
// qosOwnedByPool is set (which only happens after a real Qos(prefetch>0, ...)
// call has already succeeded), startConsumer must reject a later
// prefetch == 0 call before it ever touches the channel (r.ch is nil here,
// so any attempt to call Consume on it would panic), rather than creating a
// consumer bound by the pool's leftover prefetch.
func TestStartConsumer_RejectsPlainConsumerAfterPoolClaimedChannelQos(t *testing.T) {
	r := NewEventBus(nil)
	r.qosOwnedByPool = true

	_, _, err := r.startConsumer(context.Background(), "queue", 0)
	require.ErrorContains(t, err, "SubscribeWithOptions")
}

// TestStartConsumer_SecondPooledConsumerDoesNotNeedGuard documents that two
// SubscribeWithOptions calls in a row remain safe without the guard above:
// each sets its own Qos value atomically immediately before its own Consume
// call, so it is never contaminated by an earlier pooled subscription's
// value the way a plain Subscribe would be.
func TestStartConsumer_SecondPooledConsumerDoesNotNeedGuard(t *testing.T) {
	r := NewEventBus(nil)
	r.qosOwnedByPool = true

	// prefetch > 0 always takes the explicit-Qos branch regardless of
	// qosOwnedByPool, so this reaches (and panics on) the nil channel's
	// Qos call rather than being rejected up front -- proving the guard
	// only fires for prefetch == 0.
	require.Panics(t, func() {
		_, _, _ = r.startConsumer(context.Background(), "queue", 10)
	})
}

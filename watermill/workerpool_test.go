package watermill_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediusfy/modulex/watermill"
	"github.com/stretchr/testify/require"
)

func TestEventBus_SubscribeWithOptionsBoundsConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := watermill.NewEventBus(20, false, false)
	var active, maxActive, received atomic.Int32
	done := make(chan struct{})

	require.NoError(t, bus.SubscribeWithOptions(ctx, "work", func(context.Context, []byte) error {
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		if received.Add(1) == 20 {
			close(done)
		}
		return nil
	}, watermill.SubscribeOptions{Workers: 3, QueueCapacity: 5}))

	for i := 0; i < 20; i++ {
		require.NoError(t, bus.Publish(ctx, "work", []byte("payload")))
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for concurrent messages")
	}
	require.LessOrEqual(t, maxActive.Load(), int32(3))
	require.NoError(t, bus.Close(ctx))
}

func TestEventBus_ConcurrentCloseDrainsHandlerBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := watermill.NewEventBus(0, false, false)
	started := make(chan struct{})
	release := make(chan struct{})
	require.NoError(t, bus.SubscribeWithOptions(ctx, "work", func(context.Context, []byte) error {
		close(started)
		<-release
		return nil
	}, watermill.SubscribeOptions{Workers: 1}))
	require.NoError(t, bus.Publish(ctx, "work", []byte("payload")))
	<-started

	closed := make(chan error, 1)
	go func() { closed <- bus.Close(ctx) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before handler completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-closed)
}

func TestEventBus_SubscribeWithOptionsRejectsInvalidOptions(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	err := bus.SubscribeWithOptions(context.Background(), "work", func(context.Context, []byte) error { return nil }, watermill.SubscribeOptions{})
	require.Error(t, err)
}

// TestEventBus_SubscribeWithOptionsNacksMessageWhenSubmitRejects exercises
// the path where workerpool.Processor.Submit rejects a message (here,
// because its context is already cancelled): the handler must not run, and
// the rejection must not disrupt a separate, healthy subscription. Note this
// does not verify the Nack call itself lands — Watermill's gochannel
// resolves an unresolved message's blocked sender goroutine via its own
// closing cascade once EventBus.Close runs (deferred below), independent of
// Ack/Nack, so a short-lived test like this one cannot distinguish "Nacked"
// from "silently dropped" by observation alone.
func TestEventBus_SubscribeWithOptionsNacksMessageWhenSubmitRejects(t *testing.T) {
	subCtx, subCancel := context.WithCancel(context.Background())

	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	var rejectedCalls atomic.Int32
	require.NoError(t, bus.SubscribeWithOptions(subCtx, "rejected", func(context.Context, []byte) error {
		rejectedCalls.Add(1)
		return nil
	}, watermill.SubscribeOptions{Workers: 1, QueueCapacity: 1}))

	// Cancel before publishing: every message context gochannel derives from
	// subCtx from this point on is already Done, so Submit rejects
	// immediately instead of running the task.
	subCancel()
	require.NoError(t, bus.Publish(context.Background(), "rejected", []byte("payload")))

	require.Never(t, func() bool { return rejectedCalls.Load() != 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"handler must not run once Submit rejects the message")

	// A separate, healthy subscription must keep working.
	healthy := make(chan struct{})
	require.NoError(t, bus.SubscribeWithOptions(context.Background(), "healthy", func(context.Context, []byte) error {
		close(healthy)
		return nil
	}, watermill.SubscribeOptions{Workers: 1, QueueCapacity: 1}))
	require.NoError(t, bus.Publish(context.Background(), "healthy", []byte("payload")))

	select {
	case <-healthy:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for healthy subscription to process its message")
	}
}

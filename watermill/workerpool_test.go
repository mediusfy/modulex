package watermill_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediusfy/modulex/watermill"
	"github.com/mediusfy/modulex/workerpool"
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
	}, workerpool.Options{Workers: 3, QueueCapacity: 5}))

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
	}, workerpool.Options{Workers: 1}))
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

// TestEventBus_SubscribeWithOptionsAcksMessageWhenHandlerPanics locks in the
// fix for a handler panic leaving a message permanently unresolved: the
// panic must still result in msg.Ack() (matching the existing
// ack-regardless-of-outcome policy for handler errors) and must not corrupt
// the worker, which must keep processing subsequent messages.
func TestEventBus_SubscribeWithOptionsAcksMessageWhenHandlerPanics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	var calls atomic.Int32
	processed := make(chan struct{}, 1)
	require.NoError(t, bus.SubscribeWithOptions(ctx, "panicking", func(context.Context, []byte) error {
		if calls.Add(1) == 1 {
			panic("simulated handler panic")
		}
		close(processed)
		return nil
	}, workerpool.Options{Workers: 1, QueueCapacity: 1}))

	require.NoError(t, bus.Publish(ctx, "panicking", []byte("first")))
	require.NoError(t, bus.Publish(ctx, "panicking", []byte("second")))

	select {
	case <-processed:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the second message to be processed after a handler panic")
	}
}

func TestEventBus_SubscribeWithOptionsRejectsInvalidOptions(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	err := bus.SubscribeWithOptions(context.Background(), "work", func(context.Context, []byte) error { return nil }, workerpool.Options{})
	require.Error(t, err)
}

// TestEventBus_SubscribeWithOptionsCloseUnblocksPendingSubmit exercises the
// path where a Submit call is blocked waiting for pool capacity when Close
// runs. Submit is given the subscription's own subCtx (not msg.Context(),
// which is derived from gochannel's subscription context and is not
// guaranteed to ever be cancelled by EventBus.Close): Close must still be
// able to unblock it via subCtx cancellation, or Close would hang forever
// with the subscription goroutine leaked.
func TestEventBus_SubscribeWithOptionsCloseUnblocksPendingSubmit(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)

	started := make(chan struct{})
	release := make(chan struct{})
	var handlerCalled atomic.Bool
	require.NoError(t, bus.SubscribeWithOptions(context.Background(), "blocked", func(context.Context, []byte) error {
		if !handlerCalled.Swap(true) {
			close(started)
			<-release
		}
		return nil
	}, workerpool.Options{Workers: 1, QueueCapacity: 0}))

	require.NoError(t, bus.Publish(context.Background(), "blocked", []byte("first")))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first handler to start")
	}

	// The worker is occupied and QueueCapacity is 0, so this Submit call
	// blocks waiting for capacity until either the worker frees up or subCtx
	// is cancelled by Close. Give it time to actually reach that blocked
	// state before triggering Close below.
	require.NoError(t, bus.Publish(context.Background(), "blocked", []byte("second")))
	time.Sleep(50 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- bus.Close(context.Background()) }()

	select {
	case err := <-closed:
		t.Fatalf("Close returned before the blocked handler was released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the pending Submit should have been released by subscription cancellation")
	}
}

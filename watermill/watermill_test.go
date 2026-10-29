package watermill_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex/watermill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payloads   [][]byte
		handlerErr error
	}{
		{
			name:       "single message round-trip",
			payloads:   [][]byte{[]byte("hello")},
			handlerErr: nil,
		},
		{
			name:       "multiple messages round-trip",
			payloads:   [][]byte{[]byte("one"), []byte("two"), []byte("three")},
			handlerErr: nil,
		},
		{
			name:       "handler error is tolerated",
			payloads:   [][]byte{[]byte("boom")},
			handlerErr: errors.New("handler failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			bus := watermill.NewEventBus(0, false, false)
			defer func() { _ = bus.Close(ctx) }()

			var (
				mu       sync.Mutex
				received [][]byte
				done     = make(chan struct{})
				once     sync.Once
			)

			// Handler errors are acknowledged (not Nack'd) to prevent infinite
			// redelivery from the in-memory GoChannel, so every payload is delivered
			// exactly once.
			allReceived := func() bool {
				return len(received) == len(tt.payloads)
			}

			handler := func(_ context.Context, payload []byte) error {
				mu.Lock()
				received = append(received, payload)
				if allReceived() {
					once.Do(func() { close(done) })
				}
				mu.Unlock()
				return tt.handlerErr
			}

			require.NoError(t, bus.Subscribe(ctx, "test.topic", handler))

			for _, p := range tt.payloads {
				require.NoError(t, bus.Publish(ctx, "test.topic", p))
			}

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("timed out waiting for messages")
			}

			mu.Lock()
			defer mu.Unlock()

			require.Len(t, received, len(tt.payloads))
			seen := make(map[string]struct{}, len(received))
			for _, p := range received {
				seen[string(p)] = struct{}{}
			}
			for _, want := range tt.payloads {
				require.Contains(t, seen, string(want))
			}
		})
	}
}

func TestEventBus_Close(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := watermill.NewEventBus(0, false, false)
	require.NoError(t, bus.Subscribe(ctx, "test.topic", func(context.Context, []byte) error { return nil }))
	require.NoError(t, bus.Close(ctx))

	// Publishing after close should fail.
	require.Error(t, bus.Publish(ctx, "test.topic", []byte("after close")))
}

func TestEventBus_RejectsCancelledContextAndNilHandler(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, bus.Publish(ctx, "topic", []byte("payload")), context.Canceled)
	assert.Error(t, bus.Subscribe(context.Background(), "topic", nil))
	assert.ErrorIs(t, bus.Subscribe(ctx, "topic", func(context.Context, []byte) error { return nil }), context.Canceled)
}

// TestEventBus_CloseWithManyConcurrentSubscriptions guards against a bug
// where each subscription's cleanup goroutine removed itself from the shared
// cancel-func bookkeeping by comparing fmt.Sprintf("%p", ...) of two
// context.CancelFunc values. That comparison is unreliable: closures created
// from the same call site can format identically, so concurrent cleanups
// could remove the wrong entry. Close() cancelling every remaining tracked
// subscription (run under -race, and under goleak via TestMain) is what
// would surface the corruption: either a data race on the shared slice/map,
// or a leaked goroutine because its cancel func was never invoked.
func TestEventBus_PlainHandlerPanicIsRecovered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(ctx) }()

	panicPayload := []byte("panic-trigger")
	subsequentPayload := []byte("after-panic")
	received := make(chan []byte, 1)

	require.NoError(t, bus.Subscribe(ctx, "test.topic", func(_ context.Context, payload []byte) error {
		if string(payload) == string(panicPayload) {
			panic("simulated handler panic")
		}
		received <- payload
		return nil
	}))

	require.NoError(t, bus.Publish(ctx, "test.topic", panicPayload))
	require.NoError(t, bus.Publish(ctx, "test.topic", subsequentPayload))

	select {
	case got := <-received:
		assert.Equal(t, subsequentPayload, got)
	case <-ctx.Done():
		t.Fatal("subscription did not continue after handler panic")
	}
}

func TestEventBus_SubscriptionRespectsCallerContextCancellation(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())

	handlerCalled := make(chan struct{})
	require.NoError(t, bus.Subscribe(ctx, "test.topic", func(_ context.Context, _ []byte) error {
		close(handlerCalled)
		return nil
	}))

	require.NoError(t, bus.Publish(context.Background(), "test.topic", []byte("hello")))

	select {
	case <-handlerCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}

	// Canceling the caller context must stop the subscription. goleak will
	// report a leaked goroutine if the subscription loop ignores the
	// cancellation.
	cancel()
}

// TestEventBus_HandlerContextNotTiedToPublisherContext asserts that a
// handler's context is independent of the publisher's ctx: cancelling the
// ctx passed to Publish (e.g. because it was an HTTP request context
// cancelled the moment its response was written) must not cause the
// handler's ctx to observe that cancellation, since the handler may run
// well after the publishing request completed on this in-process
// GoChannel-backed bus.
func TestEventBus_HandlerContextNotTiedToPublisherContext(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	release := make(chan struct{})
	handlerCtxErr := make(chan error, 1)
	require.NoError(t, bus.Subscribe(context.Background(), "test.topic", func(ctx context.Context, _ []byte) error {
		<-release
		handlerCtxErr <- ctx.Err()
		return nil
	}))

	publishCtx, publishCancel := context.WithCancel(context.Background())
	require.NoError(t, bus.Publish(publishCtx, "test.topic", []byte("hello")))

	// Simulate the publisher's own request context ending immediately after
	// Publish returns, then only afterwards let the handler check its ctx.
	publishCancel()
	close(release)

	select {
	case err := <-handlerCtxErr:
		assert.NoError(t, err, "handler ctx must not be derived from (and therefore cancelled along with) the publisher's own request context")
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

// TestEventBus_HandlerContextCancelledByClose asserts that, unlike the
// publisher's ctx, the handler's ctx is tied to its own subscription's
// lifetime: cancelling that subscription via EventBus.Close must cancel the
// in-flight handler's ctx.
func TestEventBus_HandlerContextCancelledByClose(t *testing.T) {
	bus := watermill.NewEventBus(0, false, false)

	handlerStarted := make(chan struct{})
	handlerCtxDone := make(chan struct{})
	require.NoError(t, bus.Subscribe(context.Background(), "test.topic", func(ctx context.Context, _ []byte) error {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerCtxDone)
		return ctx.Err()
	}))

	require.NoError(t, bus.Publish(context.Background(), "test.topic", []byte("hello")))

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}

	closeDone := make(chan struct{})
	go func() {
		_ = bus.Close(context.Background())
		close(closeDone)
	}()

	select {
	case <-handlerCtxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler ctx was not cancelled by Close")
	}

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the handler observed cancellation")
	}
}

// TestEventBus_PropagatesTraceContextViaMetadataNotCtx asserts that trace
// context is carried from Publish to the handler via the message's
// Metadata (the same mechanism the NATS/RabbitMQ adapters use via headers),
// rather than by attaching the publisher's live ctx to the message.
func TestEventBus_PropagatesTraceContextViaMetadataNotCtx(t *testing.T) {
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prevPropagator) })

	bus := watermill.NewEventBus(0, false, false)
	defer func() { _ = bus.Close(context.Background()) }()

	wantSpanCtx := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    oteltrace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     oteltrace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	publishCtx := oteltrace.ContextWithSpanContext(context.Background(), wantSpanCtx)

	gotSpanCtx := make(chan oteltrace.SpanContext, 1)
	require.NoError(t, bus.Subscribe(context.Background(), "test.topic", func(ctx context.Context, _ []byte) error {
		gotSpanCtx <- oteltrace.SpanContextFromContext(ctx)
		return nil
	}))

	require.NoError(t, bus.Publish(publishCtx, "test.topic", []byte("hello")))

	select {
	case got := <-gotSpanCtx:
		assert.Equal(t, wantSpanCtx.TraceID(), got.TraceID(), "trace ID should propagate to the handler via message metadata")
		assert.Equal(t, wantSpanCtx.SpanID(), got.SpanID(), "span ID should propagate to the handler via message metadata")
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

func TestEventBus_CloseWithManyConcurrentSubscriptions(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const subscriberCount = 50

	bus := watermill.NewEventBus(0, false, false)

	var wg sync.WaitGroup
	wg.Add(subscriberCount)
	for i := 0; i < subscriberCount; i++ {
		topic := fmt.Sprintf("test.topic.%d", i)
		go func() {
			defer wg.Done()
			assert.NoError(t, bus.Subscribe(ctx, topic, func(context.Context, []byte) error { return nil }))
		}()
	}
	wg.Wait()

	require.NoError(t, bus.Close(ctx))

	// A subsequent Subscribe/Publish must still work correctly, confirming
	// the EventBus is left in a consistent state after the concurrent close.
	require.Error(t, bus.Publish(ctx, "test.topic.0", []byte("after close")))
}

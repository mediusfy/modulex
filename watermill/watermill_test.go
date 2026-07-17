package watermill_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex/watermill"
	"github.com/stretchr/testify/require"
)

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

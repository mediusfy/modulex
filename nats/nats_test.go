package nats_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startEmbeddedServer starts a NATS server suitable for tests.
func startEmbeddedServer(t *testing.T) *server.Server {
	t.Helper()
	s := natsserver.RunRandClientPortServer()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(s.Shutdown)
	return s
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	s := startEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	tests := []struct {
		name       string
		payloads   [][]byte
		handlerErr error
	}{
		{
			name:     "single message round-trip",
			payloads: [][]byte{[]byte("hello nats")},
		},
		{
			name:     "multiple messages round-trip",
			payloads: [][]byte{[]byte("first"), []byte("second")},
		},
		{
			name:       "handler error is tolerated",
			payloads:   [][]byte{[]byte("payload")},
			handlerErr: errors.New("handler failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := natsadapter.NewEventBus(conn)
			t.Cleanup(func() { _ = eb.Close(context.Background()) })

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			const topic = "test.topic"
			received := make(chan []byte, len(tt.payloads))
			var handlerOnce sync.Once
			handlerInvoked := make(chan struct{})

			err := eb.Subscribe(ctx, topic, func(_ context.Context, data []byte) error {
				handlerOnce.Do(func() { close(handlerInvoked) })
				received <- data
				return tt.handlerErr
			})
			require.NoError(t, err)

			for _, p := range tt.payloads {
				require.NoError(t, eb.Publish(ctx, topic, p))
			}

			select {
			case <-handlerInvoked:
			case <-ctx.Done():
				t.Fatal("timed out waiting for handler invocation")
			}

			for _, want := range tt.payloads {
				select {
				case got := <-received:
					assert.Equal(t, want, got)
				case <-ctx.Done():
					t.Fatal("timed out waiting for message")
				}
			}
		})
	}
}

func TestEventBus_CloseUnsubscribes(t *testing.T) {
	s := startEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	eb := natsadapter.NewEventBus(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receivedBefore := make(chan []byte, 1)
	require.NoError(t, eb.Subscribe(ctx, "test.topic", func(_ context.Context, data []byte) error {
		receivedBefore <- data
		return nil
	}))

	require.NoError(t, eb.Publish(ctx, "test.topic", []byte("before close")))
	select {
	case got := <-receivedBefore:
		assert.Equal(t, []byte("before close"), got)
	case <-ctx.Done():
		t.Fatal("timed out waiting for message before close")
	}

	require.NoError(t, eb.Close(ctx))

	// After Close, the previous handler should no longer be invoked.
	receivedAfter := make(chan []byte, 1)
	eb2 := natsadapter.NewEventBus(conn)
	require.NoError(t, eb2.Subscribe(ctx, "test.topic", func(_ context.Context, data []byte) error {
		receivedAfter <- data
		return nil
	}))

	require.NoError(t, eb2.Publish(ctx, "test.topic", []byte("after close")))
	select {
	case got := <-receivedBefore:
		t.Fatalf("old handler received message after close: %q", got)
	case got := <-receivedAfter:
		assert.Equal(t, []byte("after close"), got)
	case <-ctx.Done():
		t.Fatal("timed out waiting for message after close")
	}
}

func TestEventBus_ImplementsInterface(t *testing.T) {
	var _ modulex.EventBus = (*natsadapter.EventBus)(nil)
}

package rabbitmq_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/mediusfy/modulex"
	rabbitadapter "github.com/mediusfy/modulex/rabbitmq"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// rabbitMQURL returns the broker URL to use for tests. It defaults to a local
// RabbitMQ instance and can be overridden with the RABBITMQ_URL environment
// variable.
func rabbitMQURL() string {
	if u := os.Getenv("RABBITMQ_URL"); u != "" {
		return u
	}
	return "amqp://guest:guest@localhost:5672/"
}

// connectRabbitMQ attempts to connect to RabbitMQ. Tests that require a live
// broker call this and skip when it returns an error.
func connectRabbitMQ(t *testing.T) (*amqp.Connection, *amqp.Channel) {
	t.Helper()

	conn, err := amqp.Dial(rabbitMQURL())
	if err != nil {
		t.Skipf("RabbitMQ not available at %s: %v", rabbitMQURL(), err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ch, err := conn.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ch.Close() })

	return conn, ch
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	tests := []struct {
		name       string
		payloads   [][]byte
		handlerErr error
	}{
		{
			name:     "single message round-trip",
			payloads: [][]byte{[]byte("hello rabbitmq")},
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
			eb := rabbitadapter.NewEventBus(ch)
			t.Cleanup(func() { _ = eb.Close(context.Background()) })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			queue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
			received := make(chan []byte, len(tt.payloads))
			require.NoError(t, eb.Subscribe(ctx, queue, func(_ context.Context, data []byte) error {
				received <- data
				return tt.handlerErr
			}))

			for _, p := range tt.payloads {
				require.NoError(t, eb.Publish(ctx, queue, p))
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

func TestEventBus_Close(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
	require.NoError(t, eb.Subscribe(ctx, queue, func(context.Context, []byte) error {
		return nil
	}))

	require.NoError(t, eb.Close(ctx))

	// EventBus does not own the channel: Close must not close it. A
	// subsequent operation on the same channel should still succeed.
	_, err := ch.QueueDeclare(queue, true, false, false, false, nil)
	assert.NoError(t, err, "EventBus.Close must not close the caller-owned channel")
}

func TestEventBus_ImplementsInterface(t *testing.T) {
	var _ modulex.EventBus = (*rabbitadapter.EventBus)(nil)
}

package rabbitmq_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
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

func TestEventBus_RejectsInvalidInputsBeforeUsingChannel(t *testing.T) {
	eb := rabbitadapter.NewEventBus(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, eb.Publish(ctx, "queue", []byte("payload")), context.Canceled)
	assert.Error(t, eb.Subscribe(context.Background(), "queue", nil))
	assert.ErrorIs(t, eb.Subscribe(ctx, "queue", func(context.Context, []byte) error { return nil }), context.Canceled)
}

// syncBuffer is a concurrency-safe io.Writer wrapping a bytes.Buffer, needed
// because the adapter logs from its own consumer goroutine while the test
// polls the buffer's contents from the main goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestEventBus_HandlerErrorIsLogged(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	tests := []struct {
		name       string
		handlerErr error
		wantLogged bool
	}{
		{
			name:       "handler error is logged",
			handlerErr: errors.New("handler failed"),
			wantLogged: true,
		},
		{
			name:       "handler success is not logged as an error",
			handlerErr: nil,
			wantLogged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf syncBuffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			eb := rabbitadapter.NewEventBus(ch, rabbitadapter.WithLogger(logger))
			t.Cleanup(func() { _ = eb.Close(context.Background()) })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			queue := fmt.Sprintf("test.queue.logging.%d", time.Now().UnixNano())
			handlerInvoked := make(chan struct{})

			require.NoError(t, eb.Subscribe(ctx, queue, func(context.Context, []byte) error {
				close(handlerInvoked)
				return tt.handlerErr
			}))

			require.NoError(t, eb.Publish(ctx, queue, []byte("payload")))

			select {
			case <-handlerInvoked:
			case <-ctx.Done():
				t.Fatal("timed out waiting for handler invocation")
			}

			require.Eventually(t, func() bool {
				return tt.wantLogged == strings.Contains(logBuf.String(), "handler error")
			}, 2*time.Second, 10*time.Millisecond, "unexpected log output: %q", logBuf.String())

			if tt.wantLogged {
				assert.Contains(t, logBuf.String(), queue)
			}
		})
	}
}

// TestEventBus_HandlerErrorNacksWithoutRequeue verifies that a failing
// handler results in the message being nacked without requeue rather than
// silently acknowledged (auto-ack) or redelivered forever. A fresh consumer
// on the same queue must not observe the failed message again.
func TestEventBus_HandlerErrorNacksWithoutRequeue(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue := fmt.Sprintf("test.queue.nack.%d", time.Now().UnixNano())
	firstAttempt := make(chan struct{})
	var firstAttemptOnce bool

	require.NoError(t, eb.Subscribe(ctx, queue, func(context.Context, []byte) error {
		if !firstAttemptOnce {
			firstAttemptOnce = true
			close(firstAttempt)
			return errors.New("simulated processing failure")
		}
		t.Error("handler invoked a second time: message was redelivered despite no-requeue nack")
		return nil
	}))

	require.NoError(t, eb.Publish(ctx, queue, []byte("payload")))

	select {
	case <-firstAttempt:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first handler invocation")
	}

	// Give a would-be redelivery time to arrive before declaring the queue empty.
	time.Sleep(200 * time.Millisecond)

	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	require.NoError(t, err)
	assert.Zero(t, q.Messages, "queue should be empty: failed message must not be requeued")
}

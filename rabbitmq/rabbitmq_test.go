package rabbitmq_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/internal/eventbustest"
	rabbitadapter "github.com/mediusfy/modulex/rabbitmq"
	"github.com/mediusfy/modulex/workerpool"
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

// connectRabbitMQ attempts to connect to RabbitMQ. Tests and benchmarks that
// require a live broker call this and skip when it returns an error.
func connectRabbitMQ(t testing.TB) (*amqp.Connection, *amqp.Channel) {
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

	eventbustest.RunPublishSubscribeTests(t, func() modulex.EventBus {
		return rabbitadapter.NewEventBus(ch)
	}, fmt.Sprintf("test.queue.%d", time.Now().UnixNano()), 10*time.Second)
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

func TestEventBus_RejectsSubscriptionsAfterClose(t *testing.T) {
	eb := rabbitadapter.NewEventBus(nil)
	require.NoError(t, eb.Close(context.Background()))

	err := eb.Subscribe(context.Background(), "queue", func(context.Context, []byte) error { return nil })
	assert.ErrorContains(t, err, "event bus is closed")
}

// TestEventBus_PlainSubscribeDoesNotChangeSharedChannelQos locks in the fix
// for plain Subscribe silently resetting Qos on a channel shared with code
// outside the EventBus (see NewEventBus's doc comment on channel sharing):
// a Qos value set before Subscribe is called must still be in effect for
// consumers created directly on the channel afterward.
func TestEventBus_PlainSubscribeDoesNotChangeSharedChannelQos(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	require.NoError(t, ch.Qos(1, 0, false))

	eb := rabbitadapter.NewEventBus(ch)
	subQueue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
	require.NoError(t, eb.Subscribe(context.Background(), subQueue, func(context.Context, []byte) error { return nil }))
	require.NoError(t, eb.Close(context.Background()))

	probeQueue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
	_, err := ch.QueueDeclare(probeQueue, true, false, false, false, nil)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		require.NoError(t, ch.PublishWithContext(context.Background(), "", probeQueue, false, false,
			amqp.Publishing{Body: []byte("x")}))
	}

	msgs, err := ch.Consume(probeQueue, "", false, false, false, false, nil)
	require.NoError(t, err)

	received := 0
	deadline := time.After(500 * time.Millisecond)
collect:
	for {
		select {
		case <-msgs:
			received++
		case <-deadline:
			break collect
		}
	}

	assert.Equal(t, 1, received,
		"prefetch set by code sharing the channel must survive a plain EventBus.Subscribe call")
}

func TestEventBus_RejectsInvalidWorkerOptionsBeforeUsingChannel(t *testing.T) {
	eb := rabbitadapter.NewEventBus(nil)
	err := eb.SubscribeWithOptions(context.Background(), "queue", func(context.Context, []byte) error { return nil }, workerpool.Options{})
	assert.ErrorContains(t, err, "invalid worker options")
}

func TestEventBus_HandlerErrorIsLogged(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eventbustest.RunHandlerErrorLoggingTests(t, func(logger *slog.Logger) modulex.EventBus {
		return rabbitadapter.NewEventBus(ch, rabbitadapter.WithLogger(logger))
	}, fmt.Sprintf("test.queue.logging.%d", time.Now().UnixNano()), 10*time.Second, 2*time.Second)
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

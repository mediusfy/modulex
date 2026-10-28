package rabbitmq_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
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

// TestEventBus_PlainSubscribeAfterSubscribeWithOptionsIsRejected locks in the
// fix for a plain Subscribe call silently inheriting a prior
// SubscribeWithOptions call's prefetch: Channel.Qos with global=false
// changes the channel's default for every consumer created afterward (not
// only the one immediately following it), and there is no way to read that
// value back to restore it once the pooled subscription no longer needs it.
// A plain Subscribe made on the same EventBus after a pooled one must
// therefore fail instead of silently creating an under- or over-bounded
// consumer.
func TestEventBus_PlainSubscribeAfterSubscribeWithOptionsIsRejected(t *testing.T) {
	_, ch := connectRabbitMQ(t)
	eb := rabbitadapter.NewEventBus(ch)
	defer func() { _ = eb.Close(context.Background()) }()

	pooledQueue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
	require.NoError(t, eb.SubscribeWithOptions(context.Background(), pooledQueue,
		func(context.Context, []byte) error { return nil },
		workerpool.Options{Workers: 2, QueueCapacity: 2}))

	plainQueue := fmt.Sprintf("test.queue.%d", time.Now().UnixNano())
	err := eb.Subscribe(context.Background(), plainQueue, func(context.Context, []byte) error { return nil })
	assert.ErrorContains(t, err, "SubscribeWithOptions")
}

func TestEventBus_RejectsInvalidWorkerOptionsBeforeUsingChannel(t *testing.T) {
	eb := rabbitadapter.NewEventBus(nil)
	err := eb.SubscribeWithOptions(context.Background(), "queue", func(context.Context, []byte) error { return nil }, workerpool.Options{})
	assert.ErrorContains(t, err, "invalid worker options")
}

func TestEventBus_PlainHandlerPanicIsRecovered(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue := fmt.Sprintf("test.queue.panic.%d", time.Now().UnixNano())
	panicPayload := []byte("panic-trigger")
	subsequentPayload := []byte("after-panic")
	received := make(chan []byte, 1)

	require.NoError(t, eb.Subscribe(ctx, queue, func(_ context.Context, data []byte) error {
		if string(data) == string(panicPayload) {
			panic("simulated handler panic")
		}
		received <- data
		return nil
	}))

	require.NoError(t, eb.Publish(ctx, queue, panicPayload))
	require.NoError(t, eb.Publish(ctx, queue, subsequentPayload))

	select {
	case got := <-received:
		assert.Equal(t, subsequentPayload, got)
	case <-ctx.Done():
		t.Fatal("subscription did not continue after handler panic")
	}
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

// TestEventBus_PublishSetsPersistentDeliveryMode locks in the fix for M6:
// queues are declared durable, but before the fix Publish never set
// DeliveryMode, so amqp091-go defaulted to amqp.Transient -- a broker
// restart would drop an in-flight "durable" message from memory before it
// was ever written to disk.
func TestEventBus_PublishSetsPersistentDeliveryMode(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	queue := fmt.Sprintf("test.queue.persistent.%d", time.Now().UnixNano())
	_, err := ch.QueueDeclare(queue, true, false, false, false, nil)
	require.NoError(t, err)

	require.NoError(t, eb.Publish(context.Background(), queue, []byte("payload")))

	d, ok, err := ch.Get(queue, false)
	require.NoError(t, err)
	require.True(t, ok, "published message must have landed in the queue")
	require.NoError(t, d.Ack(false))

	assert.Equal(t, amqp.Persistent, d.DeliveryMode,
		"Publish must mark messages persistent so they survive a broker restart, matching the durable queue they are published to")
}

// TestEventBus_PublisherConfirmsRoundTrip locks in the addition of
// WithPublisherConfirms for M6: Publish must still deliver successfully
// (and only return once the broker has acked the message) when publisher
// confirms are enabled.
func TestEventBus_PublisherConfirmsRoundTrip(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch, rabbitadapter.WithPublisherConfirms())
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	queue := fmt.Sprintf("test.queue.confirm.%d", time.Now().UnixNano())
	_, err := ch.QueueDeclare(queue, true, false, false, false, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, eb.Publish(ctx, queue, []byte("confirmed-payload")),
		"Publish with WithPublisherConfirms must succeed and return only after the broker acks the message")

	// A second Publish exercises confirmOnce: Confirm must only be enabled
	// once, and subsequent Publish calls on this EventBus must keep
	// working.
	require.NoError(t, eb.Publish(ctx, queue, []byte("second-confirmed-payload")))

	d, ok, err := ch.Get(queue, false)
	require.NoError(t, err)
	require.True(t, ok, "the first confirmed message must have actually landed in the queue")
	require.NoError(t, d.Ack(false))
	assert.Equal(t, []byte("confirmed-payload"), d.Body)

	d2, ok, err := ch.Get(queue, false)
	require.NoError(t, err)
	require.True(t, ok, "the second confirmed message must have actually landed in the queue")
	require.NoError(t, d2.Ack(false))
	assert.Equal(t, []byte("second-confirmed-payload"), d2.Body)
}

// TestEventBus_TwoInstancesSharingChannelDoNotCollideOnConsumerTag locks in
// the fix for L8: before the fix, consumer tags were generated as
// "modulex-consumer-N" per EventBus instance, so two EventBus instances
// sharing one *amqp.Channel (a documented supported pattern) both start
// their own nextTag at 0 and would both try to register the identical tag
// "modulex-consumer-0". A duplicate consumer tag is a channel-level AMQP
// protocol error that closes the whole shared channel, not just the second
// Subscribe call, so both EventBus instances (and anything else sharing
// that channel) would stop working.
func TestEventBus_TwoInstancesSharingChannelDoNotCollideOnConsumerTag(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb1 := rabbitadapter.NewEventBus(ch)
	eb2 := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb1.Close(context.Background()) })
	t.Cleanup(func() { _ = eb2.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue1 := fmt.Sprintf("test.queue.tag1.%d", time.Now().UnixNano())
	queue2 := fmt.Sprintf("test.queue.tag2.%d", time.Now().UnixNano())

	received1 := make(chan []byte, 1)
	received2 := make(chan []byte, 1)

	require.NoError(t, eb1.Subscribe(ctx, queue1, func(_ context.Context, data []byte) error {
		received1 <- data
		return nil
	}))
	require.NoError(t, eb2.Subscribe(ctx, queue2, func(_ context.Context, data []byte) error {
		received2 <- data
		return nil
	}))

	require.NoError(t, eb1.Publish(ctx, queue1, []byte("from eb1")))
	require.NoError(t, eb2.Publish(ctx, queue2, []byte("from eb2")))

	select {
	case got := <-received1:
		assert.Equal(t, []byte("from eb1"), got)
	case <-ctx.Done():
		t.Fatal("timed out waiting for eb1's message; the shared channel may have been closed by a duplicate consumer tag")
	}
	select {
	case got := <-received2:
		assert.Equal(t, []byte("from eb2"), got)
	case <-ctx.Done():
		t.Fatal("timed out waiting for eb2's message; the shared channel may have been closed by a duplicate consumer tag")
	}

	// If the channel had already been closed by a duplicate-tag protocol
	// error, a further operation on it would fail.
	_, err := ch.QueueDeclare(fmt.Sprintf("test.queue.tagcheck.%d", time.Now().UnixNano()), true, false, false, false, nil)
	assert.NoError(t, err, "the shared channel must still be usable: a duplicate consumer tag would have closed it")
}

// TestEventBus_HandlerContextCancelledErrorRequeues locks in part of the
// fix for M5: a handler error that is (or wraps) context.Canceled or
// context.DeadlineExceeded must be requeued (Nack with requeue=true), not
// treated as a poison message, since the handler aborted because its
// context ended rather than because it actually failed to process the
// message. Without this, and with no dead-letter exchange configured, such
// a message would be lost forever instead of being retried once a consumer
// is available to run it to completion.
func TestEventBus_HandlerContextCancelledErrorRequeues(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queue := fmt.Sprintf("test.queue.ctxcancel.%d", time.Now().UnixNano())
	var attempts int32
	done := make(chan struct{})

	require.NoError(t, eb.Subscribe(ctx, queue, func(context.Context, []byte) error {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// Simulate a well-behaved handler that aborted because some
			// (unrelated) deadline/cancellation it was honoring ended, not
			// because it failed to process the message.
			return context.Canceled
		}
		close(done)
		return nil
	}))

	require.NoError(t, eb.Publish(ctx, queue, []byte("payload")))

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for redelivery after a context.Canceled handler error")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt32(&attempts), int32(2),
		"a handler error that is context.Canceled must be requeued (Nack with requeue=true), not dropped")
}

// TestEventBus_HandlerContextNotCancelledBySubscriptionCtx locks in the
// core of the fix for M5: a handler's context must not derive from (and so
// must not be cancelled by) the Subscribe call's own context, because that
// context is frequently the caller's long-lived shutdown/signal context
// (e.g. cancelled on SIGTERM) which is cancelled before EventBus.Close ever
// runs. Before the fix, a handler already running when that happened would
// see its context cancelled out from under it; a well-behaved handler
// checking ctx.Err() would then return a context-cancellation error and
// get nacked -- previously without requeue, losing the message with no DLX
// configured (see TestEventBus_HandlerContextCancelledErrorRequeues for
// that half of the fix).
func TestEventBus_HandlerContextNotCancelledBySubscriptionCtx(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	queue := fmt.Sprintf("test.queue.subctx.%d", time.Now().UnixNano())
	handlerStarted := make(chan struct{})
	result := make(chan error, 1)

	require.NoError(t, eb.Subscribe(subCtx, queue, func(hctx context.Context, _ []byte) error {
		close(handlerStarted)
		// Give the test time to cancel subCtx while this handler is still
		// running.
		time.Sleep(300 * time.Millisecond)
		result <- hctx.Err()
		return hctx.Err()
	}))

	require.NoError(t, eb.Publish(context.Background(), queue, []byte("payload")))

	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	// Simulate the caller's own shutdown/signal context being cancelled
	// before EventBus.Close is ever called.
	subCancel()

	select {
	case err := <-result:
		assert.NoError(t, err, "handler's own context must not be cancelled merely because the subscription's own context was cancelled")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler result")
	}
}

// TestEventBus_ConsumeLoopCancelsConsumerTagOnCtxCancellation locks in the
// other half of the fix for M5: the pull loop must cancel its consumer tag
// at the broker whenever it exits, including when it exits because its own
// subscription-scoped context was cancelled directly (not only when
// EventBus.Close runs). Before the fix, only Close cancelled consumer tags,
// so a subscription whose own ctx was cancelled independently of Close left
// its consumer (and any prefetched, unacked messages) registered and
// pinned at the broker indefinitely.
func TestEventBus_ConsumeLoopCancelsConsumerTagOnCtxCancellation(t *testing.T) {
	_, ch := connectRabbitMQ(t)

	eb := rabbitadapter.NewEventBus(ch)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	subCtx, subCancel := context.WithCancel(context.Background())

	queue := fmt.Sprintf("test.queue.cancel.%d", time.Now().UnixNano())
	require.NoError(t, eb.Subscribe(subCtx, queue, func(context.Context, []byte) error { return nil }))

	// Give the consumer a moment to actually register at the broker.
	time.Sleep(200 * time.Millisecond)

	// Cancel the subscription's own context directly, without ever calling
	// Close.
	subCancel()

	// Poll with a probe connection until the broker-side consumer has
	// actually been cancelled: an exclusive consumer can only be declared
	// on a queue with no other consumers attached, so this fails for as
	// long as modulex's own (now-cancelled) consumer is still registered.
	// Each attempt uses a fresh connection/channel because a failed
	// exclusive-consume attempt closes the channel it was issued on.
	require.Eventually(t, func() bool {
		probeConn, err := amqp.Dial(rabbitMQURL())
		if err != nil {
			return false
		}
		defer func() { _ = probeConn.Close() }()
		probeCh, err := probeConn.Channel()
		if err != nil {
			return false
		}
		defer func() { _ = probeCh.Close() }()

		tag := fmt.Sprintf("probe-%d", time.Now().UnixNano())
		_, err = probeCh.Consume(queue, tag, false, true /* exclusive */, false, false, nil)
		return err == nil
	}, 5*time.Second, 200*time.Millisecond,
		"consumeLoop must cancel its consumer tag at the broker when it exits due to ctx cancellation, not only via Close")
}

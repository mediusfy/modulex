package nats_test

import (
	"context"
	"fmt"
	"sync/atomic"
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

// startJetStreamEmbeddedServer starts a NATS server with JetStream enabled.
// startEmbeddedServer (nats_test.go) does not enable JetStream, so
// JetStream-dependent tests need this dedicated helper instead.
func startJetStreamEmbeddedServer(t *testing.T) *server.Server {
	t.Helper()
	opts := server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s := natsserver.RunServer(&opts)
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(s.Shutdown)
	return s
}

func TestJetStreamEventBus_ImplementsInterface(t *testing.T) {
	var _ modulex.EventBus = (*natsadapter.JetStreamEventBus)(nil)
}

func TestJetStreamEventBus_Publish(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	js, err := conn.JetStream()
	require.NoError(t, err)

	tests := []struct {
		name        string
		declareOnce func(t *testing.T, subject string)
		wantErr     bool
	}{
		{
			name: "publish to a declared stream is acknowledged and delivered",
			declareOnce: func(t *testing.T, subject string) {
				_, err := js.AddStream(&nats.StreamConfig{
					Name:     fmt.Sprintf("TEST%d", time.Now().UnixNano()),
					Subjects: []string{subject},
				})
				require.NoError(t, err)
			},
		},
		{
			name:        "publish to a subject with no matching stream returns an error",
			declareOnce: func(t *testing.T, subject string) {},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject := fmt.Sprintf("test.jetstream.%d", time.Now().UnixNano())
			tt.declareOnce(t, subject)

			eb := natsadapter.NewJetStreamEventBus(js)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := eb.Publish(ctx, subject, []byte("hello jetstream"))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify the message actually landed in the stream, not just that
			// PublishMsg returned no error.
			sub, err := js.PullSubscribe(subject, fmt.Sprintf("test-consumer-%d", time.Now().UnixNano()))
			require.NoError(t, err)
			msgs, err := sub.Fetch(1, nats.MaxWait(2*time.Second))
			require.NoError(t, err)
			require.Len(t, msgs, 1)
			assert.Equal(t, []byte("hello jetstream"), msgs[0].Data)
			require.NoError(t, msgs[0].Ack())
		})
	}
}

func TestJetStreamEventBus_SubscribeUnsupported(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js)
	err = eb.Subscribe(context.Background(), "any.subject", func(context.Context, []byte) error { return nil })
	assert.ErrorIs(t, err, natsadapter.ErrJetStreamSubscribeUnsupported)
}

func TestJetStreamEventBus_CloseDoesNotCloseConnection(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js)
	require.NoError(t, eb.Close(context.Background()))

	assert.False(t, conn.IsClosed(), "EventBus.Close must not close the caller-owned connection")
}

func TestJetStreamEventBus_PublishRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	eb := natsadapter.NewJetStreamEventBus(nil)
	assert.ErrorIs(t, eb.Publish(ctx, "any.subject", []byte("payload")), context.Canceled)
}

// --- DurableConsumer (SubscribeDurable) tests ---

func TestJetStreamEventBus_ImplementsDurableConsumer(t *testing.T) {
	var _ modulex.DurableConsumer = (*natsadapter.JetStreamEventBus)(nil)
	var _ modulex.Publisher = (*natsadapter.JetStreamEventBus)(nil)
	var _ modulex.Subscriber = (*natsadapter.JetStreamEventBus)(nil)
}

// durableTestSubject returns a unique wildcard subject prefix and a topic
// under it, and declares a JetStream stream covering the whole prefix
// (including any dead-letter subject formed by appending a suffix to the
// topic) so Publish/SubscribeDurable/dead-letter republish all land in one
// stream.
func durableTestSubject(t *testing.T, js nats.JetStreamContext) (topic string) {
	t.Helper()
	prefix := fmt.Sprintf("test.durable.%d", time.Now().UnixNano())
	_, err := js.AddStream(&nats.StreamConfig{
		Name:     fmt.Sprintf("DURABLE%d", time.Now().UnixNano()),
		Subjects: []string{prefix + ".>"},
	})
	require.NoError(t, err)
	return prefix + ".events"
}

// sequencedDurableHandler returns a DurableHandler that records every
// message it is given on received (blocking until read, so tests can
// observe delivery order) and returns the next decision from decisions for
// each call, defaulting to Ack once decisions is exhausted.
func sequencedDurableHandler(decisions []modulex.AckDecision, received chan<- modulex.DurableMessage) modulex.DurableHandler {
	var idx int32
	return func(_ context.Context, msg modulex.DurableMessage) modulex.AckDecision {
		i := int(atomic.AddInt32(&idx, 1) - 1)
		received <- msg
		if i < len(decisions) {
			return decisions[i]
		}
		return modulex.Ack
	}
}

func TestJetStreamEventBus_SubscribeDurable_RequiresConsumerName(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	err = eb.SubscribeDurable(context.Background(), topic, func(context.Context, modulex.DurableMessage) modulex.AckDecision {
		return modulex.Ack
	})
	assert.ErrorIs(t, err, natsadapter.ErrDurableConsumerNameRequired)
}

func TestJetStreamEventBus_SubscribeDurable_RejectsNilHandler(t *testing.T) {
	eb := natsadapter.NewJetStreamEventBus(nil)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	err := eb.SubscribeDurable(context.Background(), "any.subject", nil, modulex.WithConsumerName("c1"))
	require.Error(t, err)
}

func TestJetStreamEventBus_SubscribeDurable_RejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	eb := natsadapter.NewJetStreamEventBus(nil)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	err := eb.SubscribeDurable(ctx, "any.subject", func(context.Context, modulex.DurableMessage) modulex.AckDecision {
		return modulex.Ack
	}, modulex.WithConsumerName("c1"))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestJetStreamEventBus_SubscribeDurable_RejectsAfterClose(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js)
	require.NoError(t, eb.Close(context.Background()))

	topic := durableTestSubject(t, js)
	err = eb.SubscribeDurable(context.Background(), topic, func(context.Context, modulex.DurableMessage) modulex.AckDecision {
		return modulex.Ack
	}, modulex.WithConsumerName("c1"))
	require.Error(t, err)
}

func TestJetStreamEventBus_SubscribeDurable_AckAcknowledgesAndStopsRedelivery(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(300*time.Millisecond),
		natsadapter.WithDurableFetchWait(200*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler([]modulex.AckDecision{modulex.Ack}, received)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("ack-consumer")))

	select {
	case msg := <-received:
		assert.Equal(t, []byte("payload"), msg.Payload)
		assert.Equal(t, 1, msg.DeliveryCount)
		assert.False(t, msg.Redelivered)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first delivery")
	}

	// Give JetStream more than one ack-wait window to redeliver if the ack
	// did not register; a second delivery would indicate Ack was not
	// correctly translated into msg.Ack().
	select {
	case msg := <-received:
		t.Fatalf("unexpected redelivery after Ack: %+v", msg)
	case <-time.After(3 * (300 * time.Millisecond)):
	}
}

func TestJetStreamEventBus_SubscribeDurable_NackRedeliversWithDeliveryMetadata(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(200*time.Millisecond),
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
		natsadapter.WithDurableMaxDeliver(5),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler([]modulex.AckDecision{modulex.Nack, modulex.Ack}, received)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("nack-consumer")))

	var first, second modulex.DurableMessage
	select {
	case first = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first delivery")
	}
	select {
	case second = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for redelivery after Nack")
	}

	assert.Equal(t, 1, first.DeliveryCount)
	assert.False(t, first.Redelivered)
	assert.Equal(t, 2, second.DeliveryCount)
	assert.True(t, second.Redelivered)
	assert.Equal(t, first.Payload, second.Payload)
}

// TestJetStreamEventBus_SubscribeDurable_PanicRecoveredAndTreatedAsNack is a
// regression test written during independent review (not part of the
// original implementation): an unrecovered panic in a goroutine crashes the
// entire process, not just that goroutine, so a single panicking
// DurableHandler invocation must never be allowed to take down the whole
// host process. This asserts the panic is recovered, logged, and treated
// exactly like an explicit Nack (the message is redelivered), and that the
// consume loop keeps running afterward rather than silently dying.
func TestJetStreamEventBus_SubscribeDurable_PanicRecoveredAndTreatedAsNack(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(200*time.Millisecond),
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
		natsadapter.WithDurableMaxDeliver(5),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

	var attempt int32
	acked := make(chan modulex.DurableMessage, 1)
	handler := func(_ context.Context, msg modulex.DurableMessage) modulex.AckDecision {
		n := atomic.AddInt32(&attempt, 1)
		if n == 1 {
			panic("simulated handler panic on first delivery")
		}
		acked <- msg
		return modulex.Ack
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("panic-consumer")))

	// If the panic were not recovered, the whole test binary would crash
	// here (an unrecovered goroutine panic terminates the process) rather
	// than this select ever completing.
	select {
	case msg := <-acked:
		assert.Equal(t, []byte("payload"), msg.Payload)
		assert.True(t, msg.Redelivered, "message should have been redelivered after the panicking first attempt was treated as Nack")
		assert.Equal(t, 2, msg.DeliveryCount)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for redelivery after the panicking handler invocation")
	}
}

func TestJetStreamEventBus_SubscribeDurable_UnrecognizedDecisionIsTreatedAsNack(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(200*time.Millisecond),
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

	received := make(chan modulex.DurableMessage, 10)
	// AckDecision(99) is not Ack, Nack, or DeadLetter; the adapter must
	// treat it as Nack (retry) rather than silently acking or dropping.
	handler := sequencedDurableHandler([]modulex.AckDecision{modulex.AckDecision(99), modulex.Ack}, received)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("unknown-decision-consumer")))

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first delivery")
	}
	select {
	case msg := <-received:
		assert.Equal(t, 2, msg.DeliveryCount)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for redelivery after unrecognized decision")
	}
}

func TestJetStreamEventBus_SubscribeDurable_DeadLetterTerminatesAndRepublishes(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(200*time.Millisecond),
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("poison")))

	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler([]modulex.AckDecision{modulex.DeadLetter}, received)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("dlq-consumer")))

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}

	// The dead-lettered message must show up on the dead-letter subject
	// (default suffix ".DEAD").
	dlSub, err := js.PullSubscribe(topic+".DEAD", fmt.Sprintf("dlq-check-%d", time.Now().UnixNano()))
	require.NoError(t, err)
	msgs, err := dlSub.Fetch(1, nats.MaxWait(3*time.Second))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, []byte("poison"), msgs[0].Data)
	require.NoError(t, msgs[0].Ack())

	// The original delivery must never be redelivered after a terminal
	// DeadLetter decision.
	select {
	case msg := <-received:
		t.Fatalf("unexpected redelivery after DeadLetter: %+v", msg)
	case <-time.After(3 * (200 * time.Millisecond)):
	}
}

func TestJetStreamEventBus_SubscribeDurable_DeadLetterSuffixDisabledSkipsRepublish(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableAckWait(200*time.Millisecond),
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
		natsadapter.WithDurableDeadLetterSuffix(""),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("poison")))

	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler([]modulex.AckDecision{modulex.DeadLetter}, received)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("no-dlq-consumer")))

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}

	// With republishing disabled, nothing should land on the default
	// dead-letter subject.
	dlSub, err := js.PullSubscribe(topic+".DEAD", fmt.Sprintf("no-dlq-check-%d", time.Now().UnixNano()))
	require.NoError(t, err)
	_, err = dlSub.Fetch(1, nats.MaxWait(500*time.Millisecond))
	assert.ErrorIs(t, err, nats.ErrTimeout)
}

func TestJetStreamEventBus_SubscribeDurable_ConsumerIdentityResumesAckedProgress(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("first")))

	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler(nil, received) // always Ack

	ctx1, cancel1 := context.WithCancel(context.Background())
	require.NoError(t, eb.SubscribeDurable(ctx1, topic, handler, modulex.WithConsumerName("resume-consumer")))

	select {
	case msg := <-received:
		assert.Equal(t, []byte("first"), msg.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first message")
	}

	// Give the ack time to land, then tear down this subscription
	// (simulating a consumer process restart) without closing the bus.
	time.Sleep(100 * time.Millisecond)
	cancel1()

	require.NoError(t, eb.Publish(context.Background(), topic, []byte("second")))

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	require.NoError(t, eb.SubscribeDurable(ctx2, topic, handler, modulex.WithConsumerName("resume-consumer")))

	// Reusing the same ConsumerName must resume from the acked position:
	// only "second" should be (re)delivered, never "first" again.
	select {
	case msg := <-received:
		assert.Equal(t, []byte("second"), msg.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second message")
	}
	select {
	case msg := <-received:
		t.Fatalf("unexpected extra delivery, consumer identity did not resume correctly: %+v", msg)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestJetStreamEventBus_SubscribeDurable_ReplayPolicy(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableFetchWait(150*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("pre-existing")))
	// Let the publish settle before any consumer attaches.
	time.Sleep(50 * time.Millisecond)

	t.Run("ReplayAll delivers pre-existing messages to a brand-new consumer", func(t *testing.T) {
		received := make(chan modulex.DurableMessage, 10)
		handler := sequencedDurableHandler(nil, received)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, eb.SubscribeDurable(ctx, topic, handler,
			modulex.WithConsumerName("replay-all-consumer"),
			modulex.WithReplayPolicy(modulex.ReplayAll),
		))

		select {
		case msg := <-received:
			assert.Equal(t, []byte("pre-existing"), msg.Payload)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for replayed message")
		}
	})

	t.Run("ReplayNew skips pre-existing messages for a brand-new consumer", func(t *testing.T) {
		received := make(chan modulex.DurableMessage, 10)
		handler := sequencedDurableHandler(nil, received)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, eb.SubscribeDurable(ctx, topic, handler,
			modulex.WithConsumerName("replay-new-consumer"),
			modulex.WithReplayPolicy(modulex.ReplayNew),
		))

		select {
		case msg := <-received:
			t.Fatalf("ReplayNew must not deliver the pre-existing message, got: %+v", msg)
		case <-time.After(500 * time.Millisecond):
		}

		require.NoError(t, eb.Publish(context.Background(), topic, []byte("published-after-subscribe")))
		select {
		case msg := <-received:
			assert.Equal(t, []byte("published-after-subscribe"), msg.Payload)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for post-subscribe message")
		}
	})
}

func TestJetStreamEventBus_SubscribeDurable_ContextCancellationStopsLoop(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableFetchWait(100*time.Millisecond),
	)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })

	topic := durableTestSubject(t, js)
	received := make(chan modulex.DurableMessage, 10)
	handler := sequencedDurableHandler(nil, received)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, eb.SubscribeDurable(ctx, topic, handler, modulex.WithConsumerName("cancel-consumer")))

	// No message was ever published, so the pull loop is idle, repeatedly
	// polling. Cancelling the subscription's own context must stop it
	// promptly without requiring Close.
	cancel()

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	require.NoError(t, eb.Close(closeCtx), "Close must not time out waiting for an already-cancelled durable loop")
}

func TestJetStreamEventBus_Close_WaitsForInFlightHandler(t *testing.T) {
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)

	eb := natsadapter.NewJetStreamEventBus(js,
		natsadapter.WithDurableFetchWait(100*time.Millisecond),
	)

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

	handlerStarted := make(chan struct{})
	var handlerFinished atomic.Bool
	handler := func(context.Context, modulex.DurableMessage) modulex.AckDecision {
		close(handlerStarted)
		time.Sleep(200 * time.Millisecond)
		handlerFinished.Store(true)
		return modulex.Ack
	}

	require.NoError(t, eb.SubscribeDurable(context.Background(), topic, handler, modulex.WithConsumerName("shutdown-consumer")))

	select {
	case <-handlerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	require.NoError(t, eb.Close(closeCtx))
	assert.True(t, handlerFinished.Load(), "Close must wait for the in-flight handler to finish before returning")
}

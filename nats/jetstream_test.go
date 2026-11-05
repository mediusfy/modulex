package nats_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/internal/eventbustest"
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

// jsCase is one named scenario of a table-driven JetStream test.
type jsCase struct {
	name string
	run  func(t *testing.T)
}

// runJSCases runs every case as a subtest.
func runJSCases(t *testing.T, cases []jsCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, tt.run)
	}
}

// newJetStream starts an embedded JetStream server and returns a JetStream
// context on a connection that is closed at test end.
func newJetStream(t *testing.T) nats.JetStreamContext {
	t.Helper()
	s := startJetStreamEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)
	return js
}

// newDurableBus builds a JetStreamEventBus that is closed at test end.
func newDurableBus(t *testing.T, js nats.JetStreamContext, opts ...natsadapter.JetStreamOption) *natsadapter.JetStreamEventBus {
	t.Helper()
	eb := natsadapter.NewJetStreamEventBus(js, opts...)
	t.Cleanup(func() { _ = eb.Close(context.Background()) })
	return eb
}

// ackHandler is a DurableHandler that always acknowledges.
func ackHandler(context.Context, modulex.DurableMessage) modulex.AckDecision {
	return modulex.Ack
}

// recvMsg waits for one delivery or fails the test.
func recvMsg[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

// expectNoMsg fails the test if anything arrives within d.
func expectNoMsg[T any](t *testing.T, ch <-chan T, d time.Duration, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %+v", what, v)
	case <-time.After(d):
	}
}

// fetchAndAck pulls one message from subject through a fresh consumer and
// acknowledges it.
func fetchAndAck(t *testing.T, js nats.JetStreamContext, subject, prefix string, wait time.Duration) *nats.Msg {
	t.Helper()
	sub, err := js.PullSubscribe(subject, fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
	require.NoError(t, err)
	msgs, err := sub.Fetch(1, nats.MaxWait(wait))
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.NoError(t, msgs[0].Ack())
	return msgs[0]
}

// closeWithin closes eb and requires it to finish within d.
func closeWithin(t *testing.T, eb *natsadapter.JetStreamEventBus, d time.Duration, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	require.NoError(t, eb.Close(ctx), msg)
}

// subscribeDurable subscribes with a consumer name and a cancel-at-cleanup context.
func subscribeDurable(t *testing.T, eb *natsadapter.JetStreamEventBus, topic, consumer string, h modulex.DurableHandler, extra ...modulex.DurableSubscribeOption) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := append([]modulex.DurableSubscribeOption{modulex.WithConsumerName(consumer)}, extra...)
	require.NoError(t, eb.SubscribeDurable(ctx, topic, h, opts...))
	return cancel
}

func TestJetStreamEventBus_ImplementsInterface(t *testing.T) {
	tests := []struct {
		name string
		impl any
		want any
	}{
		{name: "satisfies modulex.EventBus", impl: (*natsadapter.JetStreamEventBus)(nil), want: (*modulex.EventBus)(nil)},
		{name: "satisfies modulex.DurableConsumer", impl: (*natsadapter.JetStreamEventBus)(nil), want: (*modulex.DurableConsumer)(nil)},
		{name: "satisfies modulex.Publisher", impl: (*natsadapter.JetStreamEventBus)(nil), want: (*modulex.Publisher)(nil)},
		{name: "satisfies modulex.Subscriber", impl: (*natsadapter.JetStreamEventBus)(nil), want: (*modulex.Subscriber)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Implements(t, tt.want, tt.impl)
		})
	}
}

func TestJetStreamEventBus_Publish(t *testing.T) {
	js := newJetStream(t)

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
			msg := fetchAndAck(t, js, subject, "test-consumer", 2*time.Second)
			assert.Equal(t, []byte("hello jetstream"), msg.Data)
		})
	}
}

func TestJetStreamEventBus_SubscribeUnsupported(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "subscribe unsupported", run: func(t *testing.T) {
			eb := natsadapter.NewJetStreamEventBus(newJetStream(t))
			err := eb.Subscribe(context.Background(), "any.subject", func(context.Context, []byte) error { return nil })
			assert.ErrorIs(t, err, natsadapter.ErrJetStreamSubscribeUnsupported)
		}},
	})
}

func TestJetStreamEventBus_CloseDoesNotCloseConnection(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "close does not close connection", run: func(t *testing.T) {
			s := startJetStreamEmbeddedServer(t)
			conn, err := nats.Connect(s.ClientURL())
			require.NoError(t, err)
			t.Cleanup(conn.Close)
			js, err := conn.JetStream()
			require.NoError(t, err)

			eb := natsadapter.NewJetStreamEventBus(js)
			require.NoError(t, eb.Close(context.Background()))

			assert.False(t, conn.IsClosed(), "EventBus.Close must not close the caller-owned connection")
		}},
	})
}

// TestJetStreamEventBus_RejectsInvalidCalls covers calls that fail before any
// JetStream round trip (nil context, cancelled context, nil handler).
func TestJetStreamEventBus_RejectsInvalidCalls(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	tests := []struct {
		name    string
		call    func(eb *natsadapter.JetStreamEventBus) error
		wantErr error
	}{
		{
			name: "publish rejects cancelled context",
			call: func(eb *natsadapter.JetStreamEventBus) error {
				return eb.Publish(cancelled(), "any.subject", []byte("payload"))
			},
			wantErr: context.Canceled,
		},
		{
			name: "durable rejects nil handler",
			call: func(eb *natsadapter.JetStreamEventBus) error {
				return eb.SubscribeDurable(context.Background(), "any.subject", nil, modulex.WithConsumerName("c1"))
			},
		},
		{
			name: "durable rejects cancelled context",
			call: func(eb *natsadapter.JetStreamEventBus) error {
				return eb.SubscribeDurable(cancelled(), "any.subject", ackHandler, modulex.WithConsumerName("c1"))
			},
			wantErr: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := newDurableBus(t, nil)
			err := tt.call(eb)
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

// --- DurableConsumer (SubscribeDurable) tests ---

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

func TestJetStreamEventBus_SubscribeDurable_Rejects(t *testing.T) {
	tests := []struct {
		name      string
		closeBus  bool
		opts      []modulex.DurableSubscribeOption
		wantIsErr error
	}{
		{name: "durable requires consumer name", wantIsErr: natsadapter.ErrDurableConsumerNameRequired},
		{name: "durable rejects after close", closeBus: true, opts: []modulex.DurableSubscribeOption{modulex.WithConsumerName("c1")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js := newJetStream(t)
			eb := newDurableBus(t, js)
			if tt.closeBus {
				require.NoError(t, eb.Close(context.Background()))
			}
			topic := durableTestSubject(t, js)
			err := eb.SubscribeDurable(context.Background(), topic, ackHandler, tt.opts...)
			require.Error(t, err)
			if tt.wantIsErr != nil {
				assert.ErrorIs(t, err, tt.wantIsErr)
			}
		})
	}
}

func TestJetStreamEventBus_SubscribeDurable_AckAcknowledgesAndStopsRedelivery(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "durable ack acknowledges and stops redelivery", run: func(t *testing.T) {
			js := newJetStream(t)
			eb := newDurableBus(t, js,
				natsadapter.WithDurableAckWait(300*time.Millisecond),
				natsadapter.WithDurableFetchWait(200*time.Millisecond),
			)
			topic := durableTestSubject(t, js)
			require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

			received := make(chan modulex.DurableMessage, 10)
			subscribeDurable(t, eb, topic, "ack-consumer", sequencedDurableHandler([]modulex.AckDecision{modulex.Ack}, received))

			msg := recvMsg(t, received, "first delivery")
			assert.Equal(t, []byte("payload"), msg.Payload)
			assert.Equal(t, 1, msg.DeliveryCount)
			assert.False(t, msg.Redelivered)

			// Give JetStream more than one ack-wait window to redeliver if the ack
			// did not register.
			expectNoMsg(t, received, 3*(300*time.Millisecond), "redelivery after Ack")
		}},
	})
}

// TestJetStreamEventBus_SubscribeDurable_RedeliversAfterNonAck covers the
// decisions that must all behave like a Nack: an explicit Nack, an
// unrecognized decision (not Ack, Nack or DeadLetter), and a handler panic.
// An unrecovered panic would crash the whole test binary, so the panic row
// also proves the consume loop recovers and keeps running.
func TestJetStreamEventBus_SubscribeDurable_RedeliversAfterNonAck(t *testing.T) {
	panicOnce := func(received chan<- modulex.DurableMessage) modulex.DurableHandler {
		var attempt int32
		return func(_ context.Context, msg modulex.DurableMessage) modulex.AckDecision {
			if atomic.AddInt32(&attempt, 1) == 1 {
				panic("simulated handler panic on first delivery")
			}
			received <- msg
			return modulex.Ack
		}
	}
	sequenced := func(decisions ...modulex.AckDecision) func(chan<- modulex.DurableMessage) modulex.DurableHandler {
		return func(received chan<- modulex.DurableMessage) modulex.DurableHandler {
			return sequencedDurableHandler(decisions, received)
		}
	}

	tests := []struct {
		name       string
		consumer   string
		opts       []natsadapter.JetStreamOption
		handler    func(chan<- modulex.DurableMessage) modulex.DurableHandler
		deliveries int
	}{
		{
			name:       "durable nack redelivers with delivery metadata",
			consumer:   "nack-consumer",
			opts:       []natsadapter.JetStreamOption{natsadapter.WithDurableMaxDeliver(5)},
			handler:    sequenced(modulex.Nack, modulex.Ack),
			deliveries: 2,
		},
		{
			name:       "durable panic recovered and treated as nack",
			consumer:   "panic-consumer",
			opts:       []natsadapter.JetStreamOption{natsadapter.WithDurableMaxDeliver(5)},
			handler:    panicOnce,
			deliveries: 1,
		},
		{
			// AckDecision(99) must be retried rather than silently acked or dropped.
			name:       "durable unrecognized decision is treated as nack",
			consumer:   "unknown-decision-consumer",
			handler:    sequenced(modulex.AckDecision(99), modulex.Ack),
			deliveries: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js := newJetStream(t)
			opts := append([]natsadapter.JetStreamOption{
				natsadapter.WithDurableAckWait(200 * time.Millisecond),
				natsadapter.WithDurableFetchWait(150 * time.Millisecond),
			}, tt.opts...)
			eb := newDurableBus(t, js, opts...)
			topic := durableTestSubject(t, js)
			require.NoError(t, eb.Publish(context.Background(), topic, []byte("payload")))

			received := make(chan modulex.DurableMessage, 10)
			subscribeDurable(t, eb, topic, tt.consumer, tt.handler(received))

			var msgs []modulex.DurableMessage
			for i := 0; i < tt.deliveries; i++ {
				msgs = append(msgs, recvMsg(t, received, fmt.Sprintf("delivery %d", i+1)))
			}
			last := msgs[len(msgs)-1]
			assert.Equal(t, []byte("payload"), last.Payload)
			assert.Equal(t, 2, last.DeliveryCount)
			assert.True(t, last.Redelivered, "message should have been redelivered after the non-Ack first attempt")
			if len(msgs) > 1 {
				assert.Equal(t, 1, msgs[0].DeliveryCount)
				assert.False(t, msgs[0].Redelivered)
				assert.Equal(t, msgs[0].Payload, last.Payload)
			}
		})
	}
}

func TestJetStreamEventBus_SubscribeDurable_DeadLetter(t *testing.T) {
	tests := []struct {
		name     string
		consumer string
		opts     []natsadapter.JetStreamOption
		wantDead bool
	}{
		{name: "durable dead letter terminates and republishes", consumer: "dlq-consumer", wantDead: true},
		{
			name:     "durable dead letter suffix disabled skips republish",
			consumer: "no-dlq-consumer",
			opts:     []natsadapter.JetStreamOption{natsadapter.WithDurableDeadLetterSuffix("")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js := newJetStream(t)
			opts := append([]natsadapter.JetStreamOption{
				natsadapter.WithDurableAckWait(200 * time.Millisecond),
				natsadapter.WithDurableFetchWait(150 * time.Millisecond),
			}, tt.opts...)
			eb := newDurableBus(t, js, opts...)
			topic := durableTestSubject(t, js)
			require.NoError(t, eb.Publish(context.Background(), topic, []byte("poison")))

			received := make(chan modulex.DurableMessage, 10)
			subscribeDurable(t, eb, topic, tt.consumer, sequencedDurableHandler([]modulex.AckDecision{modulex.DeadLetter}, received))
			recvMsg(t, received, "delivery")

			if !tt.wantDead {
				// With republishing disabled, nothing lands on the default
				// dead-letter subject (default suffix ".DEAD").
				dlSub, err := js.PullSubscribe(topic+".DEAD", fmt.Sprintf("no-dlq-check-%d", time.Now().UnixNano()))
				require.NoError(t, err)
				_, err = dlSub.Fetch(1, nats.MaxWait(500*time.Millisecond))
				assert.ErrorIs(t, err, nats.ErrTimeout)
				return
			}
			msg := fetchAndAck(t, js, topic+".DEAD", "dlq-check", 3*time.Second)
			assert.Equal(t, []byte("poison"), msg.Data)

			// The original delivery must never be redelivered after a terminal
			// DeadLetter decision.
			expectNoMsg(t, received, 3*(200*time.Millisecond), "redelivery after DeadLetter")
		})
	}
}

// ConsumerIdentityResumesAckedProgress publishes "first", consumes it, tears
// the subscription down (ctx cancel, or a full Close plus a new bus simulating
// a restart), publishes "second" and asserts only "second" is delivered.
func TestJetStreamEventBus_SubscribeDurable_ConsumerIdentityResumesAckedProgress(t *testing.T) {
	const consumerName = "resume-consumer"
	tests := []struct {
		name string
		// restart tears down the first subscription (it was started on eb1)
		// and returns the bus that should publish and resume.
		restart func(t *testing.T, js nats.JetStreamContext, eb1 *natsadapter.JetStreamEventBus, cancel1 context.CancelFunc, streamName string) *natsadapter.JetStreamEventBus
	}{
		{
			name: "durable consumer identity resumes acked progress after ctx cancel",
			restart: func(t *testing.T, _ nats.JetStreamContext, eb1 *natsadapter.JetStreamEventBus, cancel1 context.CancelFunc, _ string) *natsadapter.JetStreamEventBus {
				cancel1()
				return eb1
			},
		},
		{
			name: "durable consumer survives close",
			restart: func(t *testing.T, js nats.JetStreamContext, eb1 *natsadapter.JetStreamEventBus, _ context.CancelFunc, streamName string) *natsadapter.JetStreamEventBus {
				closeWithin(t, eb1, 5*time.Second, "first bus must close")

				// The consumer must still exist after a graceful Close: nats.go must
				// not have deleted it as a side effect of Unsubscribe.
				info, err := js.ConsumerInfo(streamName, consumerName)
				require.NoError(t, err, "durable consumer must survive a graceful Close")
				assert.EqualValues(t, 1, info.AckFloor.Consumer, "the acked position must also survive Close")

				// A brand-new EventBus simulates a process restart.
				return newDurableBus(t, js, natsadapter.WithDurableFetchWait(100*time.Millisecond))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js := newJetStream(t)
			eb1 := newDurableBus(t, js, natsadapter.WithDurableFetchWait(100*time.Millisecond))
			topic := durableTestSubject(t, js)
			streamName, err := js.StreamNameBySubject(topic)
			require.NoError(t, err)
			require.NoError(t, eb1.Publish(context.Background(), topic, []byte("first")))

			received := make(chan modulex.DurableMessage, 10)
			handler := sequencedDurableHandler(nil, received) // always Ack
			cancel1 := subscribeDurable(t, eb1, topic, consumerName, handler)
			assert.Equal(t, []byte("first"), recvMsg(t, received, "first message").Payload)

			// Give the ack time to land before tearing down.
			time.Sleep(100 * time.Millisecond)
			eb2 := tt.restart(t, js, eb1, cancel1, streamName)

			require.NoError(t, eb1.Publish(context.Background(), topic, []byte("second")))
			subscribeDurable(t, eb2, topic, consumerName, handler)

			// Reusing the same ConsumerName must resume from the acked position:
			// only "second" should be (re)delivered, never "first" again.
			assert.Equal(t, []byte("second"), recvMsg(t, received, "second message").Payload)
			expectNoMsg(t, received, 500*time.Millisecond, "extra delivery, consumer identity did not resume correctly")
		})
	}
}

// TestJetStreamEventBus_SubscribeDurable_FetchTerminalErrorStopsLoopPromptly
// locks in the fix for M3: before the fix, durableConsumeLoop retried any
// non-timeout Fetch error immediately and forever, hot-spinning at full CPU
// if the underlying consumer was deleted server-side. This deletes the
// durable consumer under an active pull loop and asserts the loop treats the
// resulting error as terminal and stops promptly.
func TestJetStreamEventBus_SubscribeDurable_FetchTerminalErrorStopsLoopPromptly(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "durable fetch terminal error stops loop promptly", run: func(t *testing.T) {
			js := newJetStream(t)

			var logBuf eventbustest.SyncBuffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			eb := newDurableBus(t, js,
				natsadapter.WithJetStreamLogger(logger),
				natsadapter.WithDurableFetchWait(100*time.Millisecond),
			)

			topic := durableTestSubject(t, js)
			streamName, err := js.StreamNameBySubject(topic)
			require.NoError(t, err)

			const consumerName = "terminal-error-consumer"
			subscribeDurable(t, eb, topic, consumerName, ackHandler)

			// No message was ever published, so the loop is idle. Delete the
			// consumer server-side so the next Fetch fails terminally.
			require.NoError(t, js.DeleteConsumer(streamName, consumerName))

			require.Eventually(t, func() bool {
				return strings.Contains(logBuf.String(), "durable fetch terminal error, stopping consume loop")
			}, 5*time.Second, 50*time.Millisecond,
				"durableConsumeLoop must detect the consumer's deletion as a terminal error and stop, not retry forever; log so far: %q", logBuf.String())

			closeWithin(t, eb, 2*time.Second, "Close must not time out: the consume loop should have already exited after the terminal fetch error")
		}},
	})
}

// TestJetStreamEventBus_SubscribeDurable_NackBacksOffAndDeadLettersAtMaxDeliver
// locks in the fix for M4: Nack must use msg.NakWithDelay with a backoff
// derived from the delivery count, and once a Nack would be the delivery that
// reaches WithDurableMaxDeliver, the adapter must proactively dead-letter the
// message instead of letting it silently stop being redelivered.
func TestJetStreamEventBus_SubscribeDurable_NackBacksOffAndDeadLettersAtMaxDeliver(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "durable nack backs off and dead letters at max deliver", run: func(t *testing.T) {
			js := newJetStream(t)

			const ackWait = 400 * time.Millisecond
			eb := newDurableBus(t, js,
				natsadapter.WithDurableAckWait(ackWait),
				natsadapter.WithDurableFetchWait(100*time.Millisecond),
				natsadapter.WithDurableMaxDeliver(2),
			)

			topic := durableTestSubject(t, js)
			require.NoError(t, eb.Publish(context.Background(), topic, []byte("poison")))

			type delivery struct {
				msg modulex.DurableMessage
				at  time.Time
			}
			received := make(chan delivery, 10)
			handler := func(_ context.Context, msg modulex.DurableMessage) modulex.AckDecision {
				received <- delivery{msg: msg, at: time.Now()}
				return modulex.Nack
			}
			subscribeDurable(t, eb, topic, "backoff-consumer", handler)

			first := recvMsg(t, received, "first delivery")
			assert.Equal(t, 1, first.msg.DeliveryCount)
			second := recvMsg(t, received, "redelivery after Nack")
			assert.Equal(t, 2, second.msg.DeliveryCount)

			// The redelivery must not have been near-instant: NakWithDelay's
			// backoff (starting at ackWait) must have actually elapsed.
			elapsed := second.at.Sub(first.at)
			assert.GreaterOrEqual(t, elapsed, ackWait/2,
				"redelivery after Nack happened too fast (%s); NakWithDelay backoff was not applied", elapsed)

			// This delivery's count (2) reaches WithDurableMaxDeliver (2), so the
			// adapter must dead-letter it now even though the handler returned Nack.
			msg := fetchAndAck(t, js, topic+".DEAD", "backoff-dlq-check", 3*time.Second)
			assert.Equal(t, []byte("poison"), msg.Data)

			expectNoMsg(t, received, 3*ackWait, "third delivery after max-deliver dead-letter")
		}},
	})
}

func TestJetStreamEventBus_SubscribeDurable_ReplayPolicy(t *testing.T) {
	js := newJetStream(t)
	eb := newDurableBus(t, js, natsadapter.WithDurableFetchWait(150*time.Millisecond))

	topic := durableTestSubject(t, js)
	require.NoError(t, eb.Publish(context.Background(), topic, []byte("pre-existing")))
	// Let the publish settle before any consumer attaches.
	time.Sleep(50 * time.Millisecond)

	tests := []struct {
		name     string
		consumer string
		policy   modulex.ReplayPolicy
		check    func(t *testing.T, received <-chan modulex.DurableMessage)
	}{
		{
			name:     "ReplayAll delivers pre-existing messages to a brand-new consumer",
			consumer: "replay-all-consumer",
			policy:   modulex.ReplayAll,
			check: func(t *testing.T, received <-chan modulex.DurableMessage) {
				assert.Equal(t, []byte("pre-existing"), recvMsg(t, received, "replayed message").Payload)
			},
		},
		{
			name:     "ReplayNew skips pre-existing messages for a brand-new consumer",
			consumer: "replay-new-consumer",
			policy:   modulex.ReplayNew,
			check: func(t *testing.T, received <-chan modulex.DurableMessage) {
				expectNoMsg(t, received, 500*time.Millisecond, "delivery of the pre-existing message under ReplayNew")

				require.NoError(t, eb.Publish(context.Background(), topic, []byte("published-after-subscribe")))
				assert.Equal(t, []byte("published-after-subscribe"), recvMsg(t, received, "post-subscribe message").Payload)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			received := make(chan modulex.DurableMessage, 10)
			subscribeDurable(t, eb, topic, tt.consumer, sequencedDurableHandler(nil, received), modulex.WithReplayPolicy(tt.policy))
			tt.check(t, received)
		})
	}
}

func TestJetStreamEventBus_SubscribeDurable_ContextCancellationStopsLoop(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "durable context cancellation stops loop", run: func(t *testing.T) {
			js := newJetStream(t)
			eb := newDurableBus(t, js, natsadapter.WithDurableFetchWait(100*time.Millisecond))
			topic := durableTestSubject(t, js)

			cancel := subscribeDurable(t, eb, topic, "cancel-consumer", ackHandler)

			// The pull loop is idle; cancelling the subscription's own context
			// must stop it promptly without requiring Close.
			cancel()
			closeWithin(t, eb, 2*time.Second, "Close must not time out waiting for an already-cancelled durable loop")
		}},
	})
}

func TestJetStreamEventBus_Close_WaitsForInFlightHandler(t *testing.T) {
	runJSCases(t, []jsCase{
		{name: "close waits for in flight handler", run: func(t *testing.T) {
			js := newJetStream(t)
			eb := newDurableBus(t, js, natsadapter.WithDurableFetchWait(100*time.Millisecond))

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
			subscribeDurable(t, eb, topic, "shutdown-consumer", handler)
			recvMsg(t, handlerStarted, "handler to start")

			closeWithin(t, eb, 5*time.Second, "Close must not time out while a handler is in flight")
			assert.True(t, handlerFinished.Load(), "Close must wait for the in-flight handler to finish before returning")
		}},
	})
}

// TestJetStreamEventBus_SubscribeDurable_ReplayPolicyChangeOnExistingConsumer
// pins the behavior when a durable consumer already exists and a later
// SubscribeDurable call requests a different replay policy: JetStream does
// not allow a consumer's deliver policy to change after creation, so the
// existing consumer must keep working under its original policy instead of
// SubscribeDurable failing the whole subscription.
func TestJetStreamEventBus_SubscribeDurable_ReplayPolicyChangeOnExistingConsumer(t *testing.T) {
	tests := []struct {
		name     string
		first    modulex.ReplayPolicy
		second   modulex.ReplayPolicy
		consumer string
	}{
		{"ReplayAll then ReplayNew", modulex.ReplayAll, modulex.ReplayNew, "replay-change-all-new"},
		{"ReplayNew then ReplayAll", modulex.ReplayNew, modulex.ReplayAll, "replay-change-new-all"},
	}

	js := newJetStream(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := newDurableBus(t, js, natsadapter.WithDurableFetchWait(150*time.Millisecond))
			topic := durableTestSubject(t, js)

			received := make(chan modulex.DurableMessage, 10)
			subscribeDurable(t, eb, topic, tt.consumer, sequencedDurableHandler(nil, received), modulex.WithReplayPolicy(tt.first))
			require.NoError(t, eb.Close(context.Background()))

			eb2 := newDurableBus(t, js, natsadapter.WithDurableFetchWait(150*time.Millisecond))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			require.NoError(t, eb2.SubscribeDurable(ctx, topic, sequencedDurableHandler(nil, received),
				modulex.WithConsumerName(tt.consumer), modulex.WithReplayPolicy(tt.second)),
				"re-subscribing with a different replay policy must not fail")

			require.NoError(t, eb2.Publish(context.Background(), topic, []byte("after")))
			assert.Equal(t, []byte("after"), recvMsg(t, received, "message on re-subscribed consumer").Payload)
		})
	}
}

package nats_test

import (
	"context"
	"log/slog"
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
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// startEmbeddedServer starts a NATS server suitable for tests.
func startEmbeddedServer(t *testing.T) *server.Server {
	t.Helper()
	s := natsserver.RunRandClientPortServer()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(s.Shutdown)
	return s
}

// connectEmbedded starts an embedded NATS server and returns a connection
// that is closed on test cleanup.
func connectEmbedded(t *testing.T) *nats.Conn {
	t.Helper()
	s := startEmbeddedServer(t)
	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn
}

func TestEventBus_PublishSubscribe(t *testing.T) {
	tests := []struct {
		name  string
		topic string
	}{
		{name: "round trip on test.topic", topic: "test.topic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := connectEmbedded(t)
			eventbustest.RunPublishSubscribeTests(t, func() modulex.EventBus {
				return natsadapter.NewEventBus(conn)
			}, tt.topic, 5*time.Second)
		})
	}
}

func TestEventBus_CloseUnsubscribes(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, conn *nats.Conn)
	}{
		{name: "old handler not invoked after close", run: func(t *testing.T, conn *nats.Conn) {
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

			// EventBus does not own the connection: Close must not close it.
			assert.False(t, conn.IsClosed(), "EventBus.Close must not close the caller-owned connection")

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
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, connectEmbedded(t))
		})
	}
}

func TestEventBus_ImplementsInterface(t *testing.T) {
	tests := []struct {
		name string
		bus  modulex.EventBus
	}{
		{name: "EventBus satisfies modulex.EventBus", bus: (*natsadapter.EventBus)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Implements(t, (*modulex.EventBus)(nil), tt.bus)
		})
	}
}

func TestEventBus_RejectsInvalidSubscriptionInputs(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	noop := func(context.Context, []byte) error { return nil }

	tests := []struct {
		name string
		call func(eb *natsadapter.EventBus) error
	}{
		{name: "publish with cancelled context", call: func(eb *natsadapter.EventBus) error {
			return eb.Publish(cancelled, "topic", []byte("payload"))
		}},
		{name: "subscribe with nil handler", call: func(eb *natsadapter.EventBus) error {
			return eb.Subscribe(context.Background(), "topic", nil)
		}},
		{name: "subscribe with cancelled context", call: func(eb *natsadapter.EventBus) error {
			return eb.Subscribe(cancelled, "topic", noop)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, tt.call(natsadapter.NewEventBus(nil)))
		})
	}
}

func TestEventBus_PlainHandlerPanicIsRecovered(t *testing.T) {
	tests := []struct {
		name              string
		panicPayload      []byte
		subsequentPayload []byte
	}{
		{name: "subscription continues after panic", panicPayload: []byte("panic-trigger"), subsequentPayload: []byte("after-panic")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := natsadapter.NewEventBus(connectEmbedded(t))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			received := make(chan []byte, 1)

			require.NoError(t, eb.Subscribe(ctx, "test.topic", func(_ context.Context, data []byte) error {
				if string(data) == string(tt.panicPayload) {
					panic("simulated handler panic")
				}
				received <- data
				return nil
			}))

			require.NoError(t, eb.Publish(ctx, "test.topic", tt.panicPayload))
			require.NoError(t, eb.Publish(ctx, "test.topic", tt.subsequentPayload))

			select {
			case got := <-received:
				assert.Equal(t, tt.subsequentPayload, got)
			case <-ctx.Done():
				t.Fatal("subscription did not continue after handler panic")
			}
		})
	}
}

func TestEventBus_HandlerErrorIsLogged(t *testing.T) {
	tests := []struct {
		name  string
		topic string
	}{
		{name: "handler error is logged", topic: "test.topic.logging"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := connectEmbedded(t)
			eventbustest.RunHandlerErrorLoggingTests(t, func(logger *slog.Logger) modulex.EventBus {
				return natsadapter.NewEventBus(conn, natsadapter.WithLogger(logger))
			}, tt.topic, 5*time.Second, time.Second)
		})
	}
}

// TestEventBus_RejectsSubscribeAfterClose locks in part of the fix for M7:
// unlike the JetStream, RabbitMQ, and Watermill EventBus adapters, Subscribe
// did not previously reject a call made after Close, so a caller could
// register a new subscription on an already-closed bus that Close has no
// further opportunity to wait for or clean up.
func TestEventBus_RejectsSubscribeAfterClose(t *testing.T) {
	tests := []struct {
		name    string
		topic   string
		wantErr string
	}{
		{name: "subscribe after close", topic: "test.topic.after.close", wantErr: "event bus is closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := natsadapter.NewEventBus(connectEmbedded(t))
			require.NoError(t, eb.Close(context.Background()))

			err := eb.Subscribe(context.Background(), tt.topic, func(context.Context, []byte) error { return nil })
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestEventBus_CloseWaitsForInFlightHandler locks in the other part of the
// fix for M7: Close previously unsubscribed and returned immediately,
// without waiting for a handler invocation already in flight to finish --
// unlike JetStreamEventBus.Close (durableWG) and rabbitmq.EventBus.Close
// (active/stopped), which both wait for in-flight work before returning.
func TestEventBus_CloseWaitsForInFlightHandler(t *testing.T) {
	tests := []struct {
		name         string
		topic        string
		handlerSleep time.Duration
	}{
		{name: "close waits for slow handler", topic: "test.topic.wait", handlerSleep: 200 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := natsadapter.NewEventBus(connectEmbedded(t))

			handlerStarted := make(chan struct{})
			var handlerFinished atomic.Bool
			require.NoError(t, eb.Subscribe(context.Background(), tt.topic, func(context.Context, []byte) error {
				close(handlerStarted)
				time.Sleep(tt.handlerSleep)
				handlerFinished.Store(true)
				return nil
			}))

			require.NoError(t, eb.Publish(context.Background(), tt.topic, []byte("payload")))

			select {
			case <-handlerStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for handler to start")
			}

			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			require.NoError(t, eb.Close(closeCtx))
			assert.True(t, handlerFinished.Load(), "Close must wait for the in-flight handler to finish before returning")
		})
	}
}

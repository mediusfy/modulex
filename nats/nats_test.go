package nats_test

import (
	"context"
	"log/slog"
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

func TestEventBus_PublishSubscribe(t *testing.T) {
	s := startEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	eventbustest.RunPublishSubscribeTests(t, func() modulex.EventBus {
		return natsadapter.NewEventBus(conn)
	}, "test.topic", 5*time.Second)
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
}

func TestEventBus_ImplementsInterface(t *testing.T) {
	var _ modulex.EventBus = (*natsadapter.EventBus)(nil)
}

func TestEventBus_RejectsInvalidSubscriptionInputs(t *testing.T) {
	eb := natsadapter.NewEventBus(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Error(t, eb.Publish(ctx, "topic", []byte("payload")))
	assert.Error(t, eb.Subscribe(context.Background(), "topic", nil))
	assert.Error(t, eb.Subscribe(ctx, "topic", func(context.Context, []byte) error { return nil }))
}

func TestEventBus_HandlerErrorIsLogged(t *testing.T) {
	s := startEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	eventbustest.RunHandlerErrorLoggingTests(t, func(logger *slog.Logger) modulex.EventBus {
		return natsadapter.NewEventBus(conn, natsadapter.WithLogger(logger))
	}, "test.topic.logging", 5*time.Second, time.Second)
}

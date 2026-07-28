package nats_test

import (
	"context"
	"fmt"
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

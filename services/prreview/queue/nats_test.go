package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex/services/prreview/webhook"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	natsadapter "github.com/mediusfy/modulex/nats"
)

// startJetStreamEmbeddedServer mirrors nats/jetstream_test.go's helper of
// the same purpose: an embedded, JetStream-enabled NATS server, no Docker
// required.
func startJetStreamEmbeddedServer(t *testing.T) *server.Server {
	t.Helper()
	opts := server.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s := natsserver.RunServer(&opts)
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded nats server never became ready")
	}
	t.Cleanup(s.Shutdown)
	return s
}

func newTestJetStream(t *testing.T) (nats.JetStreamContext, string) {
	t.Helper()
	s := startJetStreamEmbeddedServer(t)

	conn, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	stream := fmt.Sprintf("TEST%d", time.Now().UnixNano())
	if err := EnsureStream(js, stream, ReviewsStreamSubjects...); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	return js, stream
}

func TestEnsureStream(t *testing.T) {
	tests := []struct {
		name  string
		calls int
	}{
		{name: "idempotent on existing stream", calls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js, stream := newTestJetStream(t) // makes the first EnsureStream call
			for i := 1; i < tt.calls; i++ {
				if err := EnsureStream(js, stream, ReviewsStreamSubjects...); err != nil {
					t.Fatalf("EnsureStream call %d: %v", i+1, err)
				}
			}
			if _, err := js.StreamInfo(stream); err != nil {
				t.Fatalf("stream info: %v", err)
			}
		})
	}
}

func testReviewRequest(suffix string) webhook.ReviewRequest {
	return webhook.ReviewRequest{
		InstallationID: 1,
		Owner:          "o",
		Repo:           "r",
		PRNumber:       1,
		HeadSHA:        "sha-" + suffix,
		BaseRef:        "main",
		CloneURL:       "https://example.invalid/o/r.git",
		DeliveryID:     "d-" + suffix,
	}
}

func TestNATS_EnqueueConsume(t *testing.T) {
	good := testReviewRequest("ok")
	tests := []struct {
		name string
		// raw, when non-nil, is published directly (bypassing Enqueue's JSON
		// marshal) to exercise the path a corrupted message would hit.
		raw      []byte
		failures int // handler errors before it succeeds
		ackWait  time.Duration
		// wantHandled is whether handle must receive the request; when false
		// the message must be dead-lettered before reaching handle.
		wantHandled  bool
		wantAttempts int
	}{
		{name: "enqueue then consume acks", ackWait: 2 * time.Second, wantHandled: true, wantAttempts: 1},
		{name: "redelivers on handler error", failures: 1, ackWait: time.Second, wantHandled: true, wantAttempts: 2},
		{name: "dead-letters malformed payload", raw: []byte("not json"), ackWait: time.Second, wantHandled: false},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			js, _ := newTestJetStream(t)
			bus := natsadapter.NewJetStreamEventBus(js,
				natsadapter.WithDurableMaxDeliver(8),
				natsadapter.WithDurableAckWait(tt.ackWait),
			)
			n := &NATS{Bus: bus}
			if tt.raw != nil {
				if err := bus.Publish(context.Background(), ReviewsSubject, tt.raw); err != nil {
					t.Fatalf("publish raw: %v", err)
				}
			} else if err := n.Enqueue(context.Background(), good); err != nil {
				t.Fatalf("enqueue: %v", err)
			}

			var (
				mu       sync.Mutex
				attempts int
			)
			handled := make(chan webhook.ReviewRequest, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				_ = Consume(ctx, bus, n.subject(), fmt.Sprintf("test-consumer-%d", i), func(_ context.Context, got webhook.ReviewRequest) error {
					mu.Lock()
					attempts++
					a := attempts
					mu.Unlock()
					if a <= tt.failures {
						return fmt.Errorf("transient failure")
					}
					handled <- got
					return nil
				})
			}()

			wait := 10 * time.Second
			if !tt.wantHandled {
				wait = 2 * time.Second
			}
			select {
			case got := <-handled:
				if !tt.wantHandled {
					t.Fatal("handle was called with a malformed payload; it should have been dead-lettered")
				}
				if got != good {
					t.Fatalf("got %+v, want %+v", got, good)
				}
			case <-time.After(wait):
				if tt.wantHandled {
					t.Fatal("timed out waiting for delivery")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if tt.wantHandled && attempts != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, tt.wantAttempts)
			}
		})
	}
}

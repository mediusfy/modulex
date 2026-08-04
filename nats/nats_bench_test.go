package nats_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// benchPayload mirrors workerpool's samplePayload so JSON decode cost is
// comparable across packages' benchmarks.
type benchPayload struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp"`
	Attempt   int               `json:"attempt"`
	Metadata  map[string]string `json:"metadata"`
}

func newBenchPayload(b *testing.B) []byte {
	b.Helper()
	data, err := json.Marshal(benchPayload{
		ID:        "01J8Z9K2N4Q7R8S9T0V1W2X3Y4",
		Type:      "order.created",
		Timestamp: 1735689600,
		Attempt:   1,
		Metadata: map[string]string{
			"tenant": "acme-corp",
			"region": "us-east-1",
			"source": "checkout-service",
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkEventBus_Subscribe is core NATS's current (and, per ADR-0034
// rule 4, only) delivery path: core NATS has no broker acknowledgement or
// retry semantics, so it is not a SubscribeWithOptions candidate the way
// Watermill and RabbitMQ are. This benchmark establishes that baseline for
// comparison against the other adapters' throughput benchmarks.
func BenchmarkEventBus_Subscribe(b *testing.B) {
	s := test.RunRandClientPortServer()
	defer s.Shutdown()
	if !s.ReadyForConnections(5 * time.Second) {
		b.Fatal("embedded NATS server not ready")
	}

	conn, err := nats.Connect(s.ClientURL())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	payload := newBenchPayload(b)
	bus := natsadapter.NewEventBus(conn)
	defer func() { _ = bus.Close(context.Background()) }()

	var wg sync.WaitGroup
	if err := bus.Subscribe(context.Background(), "bench", func(context.Context, []byte) error {
		var out benchPayload
		err := json.Unmarshal(payload, &out)
		wg.Done()
		return err
	}); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	wg.Add(b.N)
	for i := 0; i < b.N; i++ {
		if err := bus.Publish(context.Background(), "bench", payload); err != nil {
			b.Fatal(err)
		}
	}
	wg.Wait()
}

package nats_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex/internal/eventbustest"
	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

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

	payload := eventbustest.NewBenchPayloadJSON(b)
	bus := natsadapter.NewEventBus(conn)
	defer func() { _ = bus.Close(context.Background()) }()

	var wg sync.WaitGroup
	if err := bus.Subscribe(context.Background(), "bench", func(context.Context, []byte) error {
		var out eventbustest.BenchPayload
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

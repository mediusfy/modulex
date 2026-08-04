package rabbitmq_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	rabbitadapter "github.com/mediusfy/modulex/rabbitmq"
	"github.com/mediusfy/modulex/workerpool"
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

func newBenchQueue() string {
	return fmt.Sprintf("bench.queue.%d", time.Now().UnixNano())
}

// BenchmarkEventBus_Subscribe_Sequential is RabbitMQ's default, current
// adapter delivery path: one handler invocation, and one ack, at a time.
func BenchmarkEventBus_Subscribe_Sequential(b *testing.B) {
	_, ch := connectRabbitMQ(b)
	payload := newBenchPayload(b)
	bus := rabbitadapter.NewEventBus(ch)
	defer func() { _ = bus.Close(context.Background()) }()

	var wg sync.WaitGroup
	queue := newBenchQueue()
	if err := bus.Subscribe(context.Background(), queue, func(context.Context, []byte) error {
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
		if err := bus.Publish(context.Background(), queue, payload); err != nil {
			b.Fatal(err)
		}
	}
	wg.Wait()
}

// BenchmarkEventBus_SubscribeWithOptions_Throughput measures bounded
// concurrent processing at several worker counts, for ADR-0034's required
// current-vs-bounded throughput comparison against
// BenchmarkEventBus_Subscribe_Sequential above.
func BenchmarkEventBus_SubscribeWithOptions_Throughput(b *testing.B) {
	for _, workers := range []int{2, 8, 32} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			_, ch := connectRabbitMQ(b)
			payload := newBenchPayload(b)
			bus := rabbitadapter.NewEventBus(ch)
			defer func() { _ = bus.Close(context.Background()) }()

			var wg sync.WaitGroup
			queue := newBenchQueue()
			err := bus.SubscribeWithOptions(context.Background(), queue, func(context.Context, []byte) error {
				var out benchPayload
				err := json.Unmarshal(payload, &out)
				wg.Done()
				return err
			}, workerpool.Options{Workers: workers, QueueCapacity: workers * 4})
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			wg.Add(b.N)
			for i := 0; i < b.N; i++ {
				if err := bus.Publish(context.Background(), queue, payload); err != nil {
					b.Fatal(err)
				}
			}
			wg.Wait()
		})
	}
}

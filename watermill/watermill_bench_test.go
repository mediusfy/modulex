package watermill_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/mediusfy/modulex/internal/eventbustest"
	"github.com/mediusfy/modulex/watermill"
	"github.com/mediusfy/modulex/workerpool"
)

// BenchmarkEventBus_Subscribe_Sequential is the default, current adapter
// delivery path (one handler invocation at a time per subscription),
// against which BenchmarkEventBus_SubscribeWithOptions_Throughput's
// workers=1 case should roughly match, per ADR-0034's requirement that
// default behavior remains unchanged.
func BenchmarkEventBus_Subscribe_Sequential(b *testing.B) {
	payload := eventbustest.NewBenchPayloadJSON(b)
	bus := watermill.NewEventBus(0, false, false)
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

// BenchmarkEventBus_SubscribeWithOptions_Throughput measures bounded
// concurrent processing at several worker counts, for ADR-0034's required
// current-vs-bounded throughput comparison against
// BenchmarkEventBus_Subscribe_Sequential above.
func BenchmarkEventBus_SubscribeWithOptions_Throughput(b *testing.B) {
	for _, workers := range []int{2, 8, 32} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			payload := eventbustest.NewBenchPayloadJSON(b)
			bus := watermill.NewEventBus(0, false, false)
			defer func() { _ = bus.Close(context.Background()) }()

			var wg sync.WaitGroup
			err := bus.SubscribeWithOptions(context.Background(), "bench", func(context.Context, []byte) error {
				var out eventbustest.BenchPayload
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
				if err := bus.Publish(context.Background(), "bench", payload); err != nil {
					b.Fatal(err)
				}
			}
			wg.Wait()
		})
	}
}

package workerpool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mediusfy/modulex/workerpool"
)

// samplePayload is representative of a small event/message body: a handful
// of scalar fields plus one nested object, comparable in size to typical
// modulex EventBus payloads (~150-250 bytes encoded).
type samplePayload struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp"`
	Attempt   int               `json:"attempt"`
	Metadata  map[string]string `json:"metadata"`
}

func newSamplePayload() samplePayload {
	return samplePayload{
		ID:        "01J8Z9K2N4Q7R8S9T0V1W2X3Y4",
		Type:      "order.created",
		Timestamp: 1735689600,
		Attempt:   1,
		Metadata: map[string]string{
			"tenant": "acme-corp",
			"region": "us-east-1",
			"source": "checkout-service",
		},
	}
}

// BenchmarkJSONDecode measures the cost of decoding one message payload in
// isolation, as a baseline for comparing against Processor.Submit's own
// overhead (BenchmarkProcessor_SubmitWait_JSONDecode below). ADR-0034
// requires this comparison before any allocation optimization (sync.Pool,
// etc.) is justified.
func BenchmarkJSONDecode(b *testing.B) {
	payload := newSamplePayload()
	data, err := json.Marshal(payload)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out samplePayload
		if err := json.Unmarshal(data, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONEncode measures the cost of encoding one message payload in
// isolation, as a baseline comparable to BenchmarkJSONDecode.
func BenchmarkJSONEncode(b *testing.B) {
	payload := newSamplePayload()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkProcessor_SubmitWait measures Submit+Wait round-trip latency and
// allocations for a trivial no-op task, isolating the pool's own overhead
// from any handler work.
func BenchmarkProcessor_SubmitWait(b *testing.B) {
	for _, workers := range []int{1, 4, 16} {
		b.Run(workersLabel(workers), func(b *testing.B) {
			p, err := workerpool.New(workerpool.Options{Workers: workers, QueueCapacity: workers})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = p.Close(context.Background()) }()

			ctx := context.Background()
			task := func(context.Context) error { return nil }

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, err := p.Submit(ctx, task)
				if err != nil {
					b.Fatal(err)
				}
				if err := h.Wait(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkProcessor_SubmitWait_JSONDecode measures Submit+Wait for a task
// that decodes samplePayload, the realistic per-message unit of work this
// pool is meant to bound. Compare against BenchmarkJSONDecode to see how
// much of the total cost is pool overhead versus JSON decode itself.
func BenchmarkProcessor_SubmitWait_JSONDecode(b *testing.B) {
	data, err := json.Marshal(newSamplePayload())
	if err != nil {
		b.Fatal(err)
	}

	for _, workers := range []int{1, 4, 16} {
		b.Run(workersLabel(workers), func(b *testing.B) {
			p, err := workerpool.New(workerpool.Options{Workers: workers, QueueCapacity: workers})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = p.Close(context.Background()) }()

			ctx := context.Background()
			task := func(context.Context) error {
				var out samplePayload
				return json.Unmarshal(data, &out)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h, err := p.Submit(ctx, task)
				if err != nil {
					b.Fatal(err)
				}
				if err := h.Wait(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkProcessor_Throughput submits n independent JSON-decode tasks
// back-to-back without waiting for each individually (waiting only at the
// end), approximating sustained throughput at a fixed worker count. This is
// the "current implementation" (bounded standard-library processing) half
// of ADR-0034's required current-vs-bounded-vs-ants comparison; an ants/v2
// adapter benchmark should be added alongside this one before ADR-0034's
// item 5 is decided.
func BenchmarkProcessor_Throughput(b *testing.B) {
	data, err := json.Marshal(newSamplePayload())
	if err != nil {
		b.Fatal(err)
	}

	for _, workers := range []int{1, 4, 16, 64} {
		b.Run(workersLabel(workers), func(b *testing.B) {
			p, err := workerpool.New(workerpool.Options{Workers: workers, QueueCapacity: workers * 4})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = p.Close(context.Background()) }()

			ctx := context.Background()
			task := func(context.Context) error {
				var out samplePayload
				return json.Unmarshal(data, &out)
			}

			b.ReportAllocs()
			b.ResetTimer()
			handles := make([]*workerpool.Handle, 0, b.N)
			for i := 0; i < b.N; i++ {
				h, err := p.Submit(ctx, task)
				if err != nil {
					b.Fatal(err)
				}
				handles = append(handles, h)
			}
			for _, h := range handles {
				if err := h.Wait(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func workersLabel(workers int) string {
	return fmt.Sprintf("workers=%d", workers)
}

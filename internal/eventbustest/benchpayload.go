package eventbustest

import (
	"encoding/json"
	"testing"
)

// BenchPayload is representative of a small event/message body: a handful of
// scalar fields plus one nested object, comparable in size to typical
// modulex EventBus payloads (~150-250 bytes encoded). It is shared by every
// adapter's benchmarks so cross-package throughput and JSON decode
// comparisons (see ADR-0034) measure the same fixed cost.
type BenchPayload struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp"`
	Attempt   int               `json:"attempt"`
	Metadata  map[string]string `json:"metadata"`
}

// NewBenchPayload returns the canonical BenchPayload value used across
// benchmarks.
func NewBenchPayload() BenchPayload {
	return BenchPayload{
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

// NewBenchPayloadJSON marshals NewBenchPayload, for benchmarks that publish
// or decode raw bytes.
func NewBenchPayloadJSON(tb testing.TB) []byte {
	tb.Helper()
	data, err := json.Marshal(NewBenchPayload())
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

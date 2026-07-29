// Package eventbustest provides shared test helpers for modulex.EventBus
// adapter implementations.
package eventbustest

import (
	"bytes"
	"sync"
)

// SyncBuffer is a concurrency-safe io.Writer wrapping a bytes.Buffer. It is
// useful for capturing log output written by adapter goroutines while the test
// polls the buffer from the main goroutine.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

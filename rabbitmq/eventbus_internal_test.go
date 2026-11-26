package rabbitmq

import (
	"context"
	"errors"
	"testing"
)

func TestEventBusCloseIsIdempotent(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "event bus close is idempotent",
			run: func(t *testing.T) {
				eb := &EventBus{closed: true}

				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				if err := eb.Close(ctx); err != nil {
					t.Fatalf("Close() error = %v, want nil for already-closed bus", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestEventBusCloseRetryAfterTimeoutStillWaits locks in the fix for #187:
// Close must mark the bus closed only after its wait has actually
// completed. A Close whose ctx expired while a consume loop was still
// running must not leave the bus looking closed, otherwise the retry that
// a caller makes with a fresh context returns nil immediately while the
// handler is still in flight.
func TestEventBusCloseRetryAfterTimeoutStillWaits(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "retried close keeps waiting while a consumer is still active",
			run: func(t *testing.T) {
				stopped := make(chan struct{})
				eb := &EventBus{active: 1, stopped: stopped}

				timedOut, cancel := context.WithCancel(context.Background())
				cancel()

				if err := eb.Close(timedOut); !errors.Is(err, context.Canceled) {
					t.Fatalf("first Close() error = %v, want context.Canceled", err)
				}

				// The retry must report the same timeout, not nil: the
				// consume loop has not signalled stopped yet.
				if err := eb.Close(timedOut); !errors.Is(err, context.Canceled) {
					t.Fatalf("retried Close() error = %v, want context.Canceled (must not report the bus closed while a handler runs)", err)
				}

				// Once the loop really exits, a retry with a live context
				// succeeds and the bus is then closed for good.
				close(stopped)
				if err := eb.Close(context.Background()); err != nil {
					t.Fatalf("Close() after consumers stopped error = %v, want nil", err)
				}

				eb.mu.Lock()
				closed := eb.closed
				eb.mu.Unlock()
				if !closed {
					t.Fatal("closed = false after a Close that completed its wait, want true")
				}

				if err := eb.Close(timedOut); err != nil {
					t.Fatalf("Close() on a fully closed bus error = %v, want nil", err)
				}
			},
		},
		{
			name: "close rejects new subscriptions from the moment it starts",
			run: func(t *testing.T) {
				stopped := make(chan struct{})
				eb := &EventBus{active: 1, stopped: stopped}

				timedOut, cancel := context.WithCancel(context.Background())
				cancel()
				_ = eb.Close(timedOut)

				if err := eb.checkClosed(); err == nil {
					t.Fatal("checkClosed() = nil after Close started, want an error")
				}
				if _, err := eb.registerConsumer("tag", func() {}); err == nil {
					t.Fatal("registerConsumer() = nil error after Close started, want an error")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestEventBusCloseHonorsContextWhileConsumersAreActive(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "event bus close honors context while consumers are active",
			run: func(t *testing.T) {
				stopped := make(chan struct{})
				eb := &EventBus{
					active:  1,
					stopped: stopped,
				}

				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				if err := eb.Close(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("Close() error = %v, want context.Canceled", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

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

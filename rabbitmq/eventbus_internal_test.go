package rabbitmq

import (
	"context"
	"errors"
	"testing"
)

func TestEventBusCloseIsIdempotent(t *testing.T) {
	eb := &EventBus{closed: true}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := eb.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v, want nil for already-closed bus", err)
	}
}

func TestEventBusCloseHonorsContextWhileConsumersAreActive(t *testing.T) {
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
}

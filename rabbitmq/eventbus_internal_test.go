package rabbitmq

import (
	"context"
	"errors"
	"testing"
)

func TestEventBusCloseHonorsContextWhileConsumersAreActive(t *testing.T) {
	stopped := make(chan struct{})
	eb := &EventBus{
		closed:  true,
		active:  1,
		stopped: stopped,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := eb.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context.Canceled", err)
	}
}

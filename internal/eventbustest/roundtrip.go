package eventbustest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func RunPublishSubscribeTests(t *testing.T, newBus func() modulex.EventBus, topic string, timeout time.Duration) {
	t.Helper()

	tests := []struct {
		name       string
		payloads   [][]byte
		handlerErr error
	}{
		{
			name:     "single message round-trip",
			payloads: [][]byte{[]byte("hello")},
		},
		{
			name:     "multiple messages round-trip",
			payloads: [][]byte{[]byte("first"), []byte("second")},
		},
		{
			name:       "handler error is tolerated",
			payloads:   [][]byte{[]byte("payload")},
			handlerErr: errors.New("handler failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eb := newBus()
			t.Cleanup(func() { _ = eb.Close(context.Background()) })

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			received := make(chan []byte, len(tt.payloads))
			var handlerOnce sync.Once
			handlerInvoked := make(chan struct{})

			err := eb.Subscribe(ctx, topic, func(_ context.Context, data []byte) error {
				handlerOnce.Do(func() { close(handlerInvoked) })
				received <- data
				return tt.handlerErr
			})
			require.NoError(t, err)

			for _, p := range tt.payloads {
				require.NoError(t, eb.Publish(ctx, topic, p))
			}

			select {
			case <-handlerInvoked:
			case <-ctx.Done():
				t.Fatal("timed out waiting for handler invocation")
			}

			for _, want := range tt.payloads {
				select {
				case got := <-received:
					assert.Equal(t, want, got)
				case <-ctx.Done():
					t.Fatal("timed out waiting for message")
				}
			}
		})
	}
}

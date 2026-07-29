package eventbustest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func RunHandlerErrorLoggingTests(t *testing.T, newBus func(logger *slog.Logger) modulex.EventBus, topic string, timeout, logWait time.Duration) {
	t.Helper()

	tests := []struct {
		name       string
		handlerErr error
		wantLogged bool
	}{
		{
			name:       "handler error is logged",
			handlerErr: errors.New("handler failed"),
			wantLogged: true,
		},
		{
			name:       "handler success is not logged as an error",
			handlerErr: nil,
			wantLogged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf SyncBuffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			eb := newBus(logger)
			t.Cleanup(func() { _ = eb.Close(context.Background()) })

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			handlerInvoked := make(chan struct{})
			var once sync.Once

			require.NoError(t, eb.Subscribe(ctx, topic, func(context.Context, []byte) error {
				once.Do(func() { close(handlerInvoked) })
				return tt.handlerErr
			}))

			require.NoError(t, eb.Publish(ctx, topic, []byte("payload")))

			select {
			case <-handlerInvoked:
			case <-ctx.Done():
				t.Fatal("timed out waiting for handler invocation")
			}

			require.Eventually(t, func() bool {
				return tt.wantLogged == strings.Contains(logBuf.String(), "handler error")
			}, logWait, 10*time.Millisecond, "unexpected log output: %q", logBuf.String())

			if tt.wantLogged {
				assert.Contains(t, logBuf.String(), topic)
			}
		})
	}
}

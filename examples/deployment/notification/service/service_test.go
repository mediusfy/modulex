package service_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/mediusfy/modulex/examples/deployment/notification/service"
	"github.com/stretchr/testify/assert"
)

func TestNotificationServiceSend(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name    string
		logger  *slog.Logger
		message string
		wantErr error
	}{
		{name: "send", logger: discard, message: "hello"},
		{name: "empty message", logger: discard, message: "", wantErr: service.ErrEmptyMessage},
		{name: "nil logger", logger: nil, message: "nil-logger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := service.New(tt.logger)
			assert.NotNil(t, svc)

			err := svc.Send(context.Background(), tt.message)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

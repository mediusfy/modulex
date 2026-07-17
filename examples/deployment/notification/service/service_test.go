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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)

	err := svc.Send(context.Background(), "hello")
	assert.NoError(t, err)
}

func TestNotificationServiceSendEmptyMessage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)

	err := svc.Send(context.Background(), "")
	assert.ErrorIs(t, err, service.ErrEmptyMessage)
}

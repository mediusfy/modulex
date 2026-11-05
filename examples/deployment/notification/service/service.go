// Package service contains the core business logic for the notification feature.
package service

import (
	"context"
	"log/slog"

	"github.com/mediusfy/modulex/apperrors"
)

// logKeyMessage is the slog attribute key for the notification message.
const logKeyMessage = "message"

// NotificationService implements ports.Sender with in-process delivery.
type NotificationService struct {
	logger *slog.Logger
}

// ErrEmptyMessage is returned when a notification request contains no message.
const ErrEmptyMessage apperrors.Error = "message must not be empty"

// New creates a NotificationService.
func New(logger *slog.Logger) *NotificationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &NotificationService{logger: logger}
}

// Send implements ports.Sender.
func (s *NotificationService) Send(ctx context.Context, message string) error {
	if message == "" {
		return ErrEmptyMessage
	}
	s.logger.Info("notification sent", slog.String(logKeyMessage, message))
	return nil
}

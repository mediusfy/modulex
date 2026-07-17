// Package service contains the core business logic for the notification feature.
package service

import (
	"context"
	"errors"
	"log/slog"
)

// NotificationService implements ports.Service with in-process delivery.
type NotificationService struct {
	logger *slog.Logger
}

// ErrEmptyMessage is returned when a notification request contains no message.
var ErrEmptyMessage = errors.New("message must not be empty")

// New creates a NotificationService.
func New(logger *slog.Logger) *NotificationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &NotificationService{logger: logger}
}

// Send implements ports.Service.
func (s *NotificationService) Send(ctx context.Context, message string) error {
	if message == "" {
		return ErrEmptyMessage
	}
	s.logger.Info("notification sent", slog.String("message", message))
	return nil
}

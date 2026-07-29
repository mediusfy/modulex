// Package adapters provides infrastructure adapters for the notification feature.
package adapters

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
)

// HTTPServer exposes the notification service over HTTP. It keeps HTTP
// concerns (encoding, status codes) separate from business logic.
type HTTPServer struct {
	svc    ports.Sender
	logger *slog.Logger
}

// NewHTTPServer creates an HTTP adapter for the notification service.
func NewHTTPServer(svc ports.Sender, logger *slog.Logger) *HTTPServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPServer{svc: svc, logger: logger}
}

// SendRequest is the JSON body accepted by the send endpoint.
type SendRequest struct {
	Message string `json:"message"`
}

// SendResponse is the JSON body returned by the send endpoint.
type SendResponse struct {
	Status string `json:"status"`
}

// SendHandler returns an http.HandlerFunc that forwards requests to the
// notification service.
func (s *HTTPServer) SendHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req SendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.logger.Warn("failed to decode send request", slog.Any("error", err))
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if err := s.svc.Send(r.Context(), req.Message); err != nil {
			s.logger.Warn("notification service rejected message", slog.Any("error", err))
			status := http.StatusInternalServerError
			if errors.Is(err, service.ErrEmptyMessage) {
				status = http.StatusBadRequest
			}
			http.Error(w, "notification failed", status)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if err := json.NewEncoder(w).Encode(SendResponse{Status: "accepted"}); err != nil {
			s.logger.Warn("failed to encode response", slog.Any("error", err))
		}
	}
}

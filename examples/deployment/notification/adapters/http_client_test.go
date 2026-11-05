package adapters_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPClientRejectsBadBaseURL(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		wantErrText string
	}{
		{name: "empty base URL", baseURL: "", wantErrText: "baseURL"},
		{name: "whitespace base URL", baseURL: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := adapters.NewHTTPClient(tt.baseURL, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrText)
		})
	}
}

// notifyHandler returns the real notification send handler.
func notifyHandler() http.HandlerFunc {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return adapters.NewHTTPServer(service.New(logger), logger).SendHandler()
}

func TestHTTPClientSend(t *testing.T) {
	tests := []struct {
		name        string
		handler     func() http.HandlerFunc
		httpClient  *http.Client
		message     string
		ctxTimeout  time.Duration
		wantErr     bool
		wantErrText string
		wantErrIs   error
	}{
		{name: "success", handler: notifyHandler, message: "hello"},
		{
			name: "server error",
			handler: func() http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
			},
			message: "hello", wantErr: true, wantErrText: "500",
		},
		{name: "empty message", handler: notifyHandler, message: "", wantErr: true, wantErrText: "400"},
		{
			name: "custom client", handler: notifyHandler,
			httpClient: &http.Client{Timeout: 5 * time.Second}, message: "hello",
		},
		{
			name:       "respects context cancellation",
			httpClient: &http.Client{Timeout: 5 * time.Second},
			message:    "hello",
			ctxTimeout: 50 * time.Millisecond,
			wantErr:    true,
			wantErrIs:  context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := make(chan struct{})
			handler := func(w http.ResponseWriter, r *http.Request) { <-block }
			if tt.handler != nil {
				handler = tt.handler()
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/notify", handler)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			defer close(block)

			client, err := adapters.NewHTTPClient(ts.URL, tt.httpClient)
			require.NoError(t, err)

			ctx := context.Background()
			if tt.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.ctxTimeout)
				defer cancel()
			}

			err = client.Send(ctx, tt.message)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.wantErrText != "" {
				assert.Contains(t, err.Error(), tt.wantErrText)
			}
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
		})
	}
}

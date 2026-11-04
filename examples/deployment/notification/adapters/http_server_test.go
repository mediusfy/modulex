package adapters_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPServerSendHandler(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	jsonBody := func(msg string) []byte {
		b, err := json.Marshal(adapters.SendRequest{Message: msg})
		require.NoError(t, err)
		return b
	}
	tests := []struct {
		name       string
		logger     *slog.Logger
		method     string
		body       []byte
		wantStatus int
		wantAccept bool
	}{
		{name: "accepts message", logger: discard, method: http.MethodPost, body: jsonBody("hello"), wantStatus: http.StatusAccepted, wantAccept: true},
		{name: "nil logger", logger: nil, method: http.MethodPost, body: jsonBody("hello"), wantStatus: http.StatusAccepted},
		{name: "empty message", logger: discard, method: http.MethodPost, body: jsonBody(""), wantStatus: http.StatusBadRequest},
		{name: "invalid method", logger: discard, method: http.MethodGet, wantStatus: http.StatusMethodNotAllowed},
		{name: "invalid body", logger: discard, method: http.MethodPost, body: []byte("not-json"), wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := adapters.NewHTTPServer(service.New(tt.logger), tt.logger)

			var reader io.Reader
			if tt.body != nil {
				reader = bytes.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, "/notify", reader)
			if tt.body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()

			server.SendHandler()(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantAccept {
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
				var resp adapters.SendResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				assert.Equal(t, "accepted", resp.Status)
			}
		})
	}
}

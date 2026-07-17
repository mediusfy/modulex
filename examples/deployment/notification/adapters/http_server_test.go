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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	body, err := json.Marshal(adapters.SendRequest{Message: "hello"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.SendHandler()(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	var resp adapters.SendResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "accepted", resp.Status)
}

func TestHTTPServerSendHandlerEmptyMessage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	body, err := json.Marshal(adapters.SendRequest{Message: ""})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.SendHandler()(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHTTPServerSendHandlerInvalidMethod(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	req := httptest.NewRequest(http.MethodGet, "/notify", nil)
	rec := httptest.NewRecorder()

	server.SendHandler()(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHTTPServerSendHandlerInvalidBody(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.SendHandler()(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

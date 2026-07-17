package adapters_test

import (
	"context"
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

func TestHTTPClientSend(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify", server.SendHandler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := adapters.NewHTTPClient(ts.URL, nil)
	err := client.Send(context.Background(), "hello")
	assert.NoError(t, err)
}

func TestHTTPClientSendServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := adapters.NewHTTPClient(ts.URL, nil)
	err := client.Send(context.Background(), "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestHTTPClientSendEmptyMessage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.New(logger)
	server := adapters.NewHTTPServer(svc, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/notify", server.SendHandler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := adapters.NewHTTPClient(ts.URL, nil)
	err := client.Send(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

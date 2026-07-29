package deployment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	gochi "github.com/go-chi/chi/v5"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/deployment/consumer"
	"github.com/mediusfy/modulex/examples/deployment/notification"
	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonolithComposition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gochi.NewRouter()

	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	require.NoError(t, err)
	require.NoError(t, modulexchi.RegisterRouter(mgr, router))
	require.NoError(t, mgr.RegisterModule(notification.NewModule()))
	require.NoError(t, mgr.RegisterModule(consumer.NewModule()))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))
	require.NoError(t, mgr.StartModules(ctx))
	t.Cleanup(func() { _ = mgr.StopModules(context.Background()) })

	// Exercise the mounted HTTP endpoint directly.
	body, err := json.Marshal(adapters.SendRequest{Message: "monolith"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestRemoteComposition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Standalone notification service with a recorder to verify the remote call.
	recorder := &sendRecorder{svc: service.New(logger)}
	server := adapters.NewHTTPServer(recorder, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/notify", server.SendHandler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Consumer process with a remote notification module that proxies to the
	// standalone service over HTTP.
	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	require.NoError(t, err)
	remoteMod, err := notification.NewRemoteModule(ts.URL, nil)
	require.NoError(t, err)
	require.NoError(t, mgr.RegisterModule(remoteMod))
	require.NoError(t, mgr.RegisterModule(consumer.NewModule()))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))
	require.NoError(t, mgr.StartModules(ctx))
	require.NoError(t, mgr.StopModules(ctx))

	assert.True(t, recorder.called, "remote notification service should have been called")
	assert.Equal(t, "hello from consumer", recorder.lastMessage)
}

func TestNotificationModuleWithoutRouter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	require.NoError(t, err)
	require.NoError(t, mgr.RegisterModule(notification.NewModule()))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))

	svc, err := modulex.Resolve(mgr, ports.ServiceKey)
	require.NoError(t, err)
	assert.NoError(t, svc.Send(ctx, "no-router"))
}

type sendRecorder struct {
	svc         ports.Sender
	called      bool
	lastMessage string
}

func (r *sendRecorder) Send(ctx context.Context, message string) error {
	r.called = true
	r.lastMessage = message
	return r.svc.Send(ctx, message)
}

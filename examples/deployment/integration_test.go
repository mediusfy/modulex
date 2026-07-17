package deployment_test

import (
	"context"
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
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonolithComposition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gochi.NewRouter()

	mgr := modulex.NewManager(nil, logger, nil)
	require.NoError(t, modulexchi.RegisterRouter(mgr, router))
	require.NoError(t, mgr.RegisterModule(notification.NewModule(logger)))
	require.NoError(t, mgr.RegisterModule(consumer.NewModule()))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))
	require.NoError(t, mgr.StartModules(ctx))
	require.NoError(t, mgr.StopModules(ctx))
}

func TestRemoteComposition(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Standalone notification service.
	notificationSvc := service.New(logger)
	server := adapters.NewHTTPServer(notificationSvc, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/notify", server.SendHandler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Consumer process with a remote notification module that proxies to the
	// standalone service over HTTP.
	mgr := modulex.NewManager(nil, logger, nil)
	require.NoError(t, mgr.RegisterModule(notification.NewRemoteModule(ts.URL, nil)))
	require.NoError(t, mgr.RegisterModule(consumer.NewModule()))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))
	require.NoError(t, mgr.StartModules(ctx))
	require.NoError(t, mgr.StopModules(ctx))
}

func TestNotificationModuleWithoutRouter(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mgr := modulex.NewManager(nil, logger, nil)
	require.NoError(t, mgr.RegisterModule(notification.NewModule(logger)))

	ctx := context.Background()
	require.NoError(t, mgr.InitModules(ctx))

	svc, err := modulex.Resolve(mgr, notification.ServiceKey)
	require.NoError(t, err)
	assert.NoError(t, svc.Send(ctx, "no-router"))
}

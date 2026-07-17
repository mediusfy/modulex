// Package main demonstrates a remote Modulex deployment: the consumer module
// runs in its own process and talks to the notification service over HTTP
// through a client adapter that implements the same ports.Service interface.
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/deployment/consumer"
	"github.com/mediusfy/modulex/examples/deployment/notification"
	"github.com/mediusfy/modulex/examples/deployment/notification/adapters"
	"github.com/mediusfy/modulex/examples/deployment/notification/service"
)

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Start a standalone notification service HTTP server.
	notificationSvc := service.New(logger)
	server := adapters.NewHTTPServer(notificationSvc, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/notify", server.SendHandler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Run the consumer module with a remote notification module registered under
	// the same name and typed key as the local module in the monolith example.
	mgr := modulex.NewManager(nil, logger, nil)
	if err := mgr.RegisterModule(notification.NewRemoteModule(ts.URL, nil)); err != nil {
		logger.Error("failed to register remote notification module", slog.Any("error", err))
		return
	}
	if err := mgr.RegisterModule(consumer.NewModule()); err != nil {
		logger.Error("failed to register consumer module", slog.Any("error", err))
		return
	}

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to init modules", slog.Any("error", err))
		return
	}
	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		return
	}
	if err := mgr.StopModules(ctx); err != nil {
		logger.Error("failed to stop modules", slog.Any("error", err))
		return
	}
}

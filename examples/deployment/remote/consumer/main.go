// Package main runs the consumer process in a remote deployment.
// It registers a remote notification client adapter under the same typed key
// the local module uses, so the consumer module itself does not change.
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/deployment/consumer"
	"github.com/mediusfy/modulex/examples/deployment/notification"
)

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var notificationURL string
	flag.StringVar(&notificationURL, "notification-url", os.Getenv("NOTIFICATION_URL"), "URL of the remote notification service")
	flag.Parse()

	if notificationURL == "" {
		logger.Error("notification-url is required")
		os.Exit(1)
	}

	remoteMod, err := notification.NewRemoteModule(notificationURL, nil)
	if err != nil {
		logger.Error("failed to create remote notification module", slog.Any("error", err))
		os.Exit(1)
	}

	mgr, err := modulex.NewManager(nil, logger, nil)
	if err != nil {
		logger.Error("failed to create manager", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.RegisterModule(remoteMod); err != nil {
		logger.Error("failed to register remote notification module", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.RegisterModule(consumer.NewModule()); err != nil {
		logger.Error("failed to register consumer module", slog.Any("error", err))
		os.Exit(1)
	}

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to init modules", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.StopModules(ctx); err != nil {
		logger.Error("failed to stop modules", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("consumer finished")
}

// Package main runs the consumer process against a remote gRPC notification
// service. It registers a remote gRPC client adapter under the same typed
// key the local module uses, so the consumer module itself does not
// change — the gRPC counterpart of remote/consumer/main.go, which does the
// same thing over HTTP.
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

	var target string
	flag.StringVar(&target, "notification-target", os.Getenv("NOTIFICATION_GRPC_TARGET"), "gRPC target of the remote notification service (e.g. localhost:50051)")
	flag.Parse()

	if target == "" {
		logger.Error("notification-target is required")
		os.Exit(1)
	}

	remoteMod, err := notification.NewGRPCRemoteModule(target)
	if err != nil {
		logger.Error("failed to create remote notification grpc module", slog.Any("error", err))
		os.Exit(1)
	}

	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	if err != nil {
		logger.Error("failed to create manager", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.RegisterModule(remoteMod); err != nil {
		logger.Error("failed to register remote notification grpc module", slog.Any("error", err))
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

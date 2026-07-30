// Package main runs the notification service as a standalone gRPC process.
//
// Unlike the HTTP notification-server example (which serves its HTTP server
// outside the Modulex lifecycle, in main itself), this example demonstrates
// the modulex/grpc package's server lifecycle helper: the gRPC listener is
// started and gracefully stopped by mgr.StartModules/mgr.StopModules, via
// notification.GRPCServerModule. The grpc-consumer process connects to this
// server using the gRPC client adapter.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/deployment/notification"
)

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	addr := os.Getenv("GRPC_ADDR")
	if addr == "" {
		addr = ":50051"
	}

	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	if err != nil {
		logger.Error("failed to create manager", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.RegisterModule(notification.NewModule()); err != nil {
		logger.Error("failed to register notification module", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.RegisterModule(notification.NewGRPCServerModule(addr)); err != nil {
		logger.Error("failed to register notification grpc server module", slog.Any("error", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to init modules", slog.Any("error", err))
		os.Exit(1)
	}
	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("notification grpc server running", slog.String("addr", addr))
	<-ctx.Done()

	// Detach from the (now-canceled) signal context so StopModules' own
	// bounded graceful shutdown runs to completion.
	if err := mgr.StopModules(context.WithoutCancel(ctx)); err != nil {
		logger.Error("failed to stop modules", slog.Any("error", err))
		os.Exit(1)
	}
}

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gochi "github.com/go-chi/chi/v5"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/hexagonal/incident"
	watermilladapter "github.com/mediusfy/modulex/watermill"
)

func main() {
	logger := slog.Default()
	router := gochi.NewRouter()

	// Configuration loader stub
	configLoader := func(target interface{}) error {
		return nil
	}

	// Initialize Watermill in-memory (Go Channel)
	watermillBus := watermilladapter.NewEventBus(100, false, false)

	// Create manager
	mgr := modulex.NewManager(watermillBus, logger, configLoader)

	// Register the Chi router as a typed service so modules can resolve it
	if err := modulexchi.RegisterRouter(mgr, router); err != nil {
		logger.Error("failed to register router", slog.Any("error", err))
		os.Exit(1)
	}

	// Register our business module
	if err := mgr.RegisterModule(incident.NewModule()); err != nil {
		logger.Error("failed to register module", slog.Any("error", err))
		os.Exit(1)
	}

	// Boot process
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.Info("bootstrapping modules...")
	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to initialize modules", slog.Any("error", err))
		os.Exit(1)
	}

	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		os.Exit(1)
	}

	// Start HTTP Server
	server := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	go func() {
		logger.Info("HTTP server running on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", slog.Any("error", err))
		}
	}()

	// Graceful shutdown
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	<-stopChan

	logger.Info("shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", slog.Any("error", err))
	}

	if err := mgr.StopModules(shutdownCtx); err != nil {
		logger.Error("error during module teardown", slog.Any("error", err))
	}

	logger.Info("teardown complete.")
}

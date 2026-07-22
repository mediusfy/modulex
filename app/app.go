// Package app provides an opinionated bootstrap helper for modulex-based
// services.
//
// Every modulex service entrypoint otherwise hand-writes the same skeleton:
// construct a Manager, register modules, derive a signal-aware context,
// drive Init -> Start -> wait -> Stop, and report the first failing step.
// Run owns that skeleton so main() can stay limited to composing modules and
// configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mediusfy/modulex"
)

// defaultShutdownTimeout bounds the final StopModules call when the caller
// does not override it via WithShutdownTimeout.
const defaultShutdownTimeout = 15 * time.Second

// options holds the resolved configuration for Run, built up from the
// supplied Option values.
type options struct {
	ctx             context.Context
	signals         []os.Signal
	shutdownTimeout time.Duration
	managerOpts     []modulex.ManagerOption
	setup           func(*modulex.Manager) error
}

// Option configures Run.
type Option func(*options)

// WithContext sets the base context Run derives its signal-aware lifecycle
// context from. Defaults to context.Background(). Tests typically pass a
// cancellable context here instead of relying on OS signals to trigger
// shutdown.
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		o.ctx = ctx
	}
}

// WithSignals overrides the OS signals that trigger shutdown. Defaults to
// os.Interrupt and syscall.SIGTERM.
func WithSignals(sig ...os.Signal) Option {
	return func(o *options) {
		o.signals = sig
	}
}

// WithShutdownTimeout bounds the final StopModules call. Defaults to 15
// seconds.
func WithShutdownTimeout(d time.Duration) Option {
	return func(o *options) {
		o.shutdownTimeout = d
	}
}

// WithManagerOptions passes additional modulex.ManagerOption values through
// to modulex.NewManager, e.g. WithTracer, WithEventBus, or WithPanicPolicy.
func WithManagerOptions(opts ...modulex.ManagerOption) Option {
	return func(o *options) {
		o.managerOpts = append(o.managerOpts, opts...)
	}
}

// WithSetup registers a hook that runs against the constructed Manager after
// NewManager but before modules are registered and initialized. Use this for
// wiring that must happen before Init, such as registering a Chi router via
// modulexchi.RegisterRouter.
func WithSetup(fn func(*modulex.Manager) error) Option {
	return func(o *options) {
		o.setup = fn
	}
}

// Run constructs a modulex.Manager, registers modules, and drives the full
// Init -> Start -> wait-for-shutdown -> Stop lifecycle.
//
// Run blocks until the context derived from WithContext (or
// context.Background() by default) is cancelled or one of the configured
// shutdown signals (os.Interrupt and syscall.SIGTERM by default) is
// received, then stops the manager with a bounded timeout. It returns the
// first error encountered at any step, wrapped with context about which
// step failed; Run itself never calls os.Exit, so callers remain free to
// decide how to report the error:
//
//	if err := app.Run(logger, configLoader, modules); err != nil {
//	    logger.Error("application failed", slog.Any("error", err))
//	    os.Exit(1)
//	}
func Run(logger *slog.Logger, configLoader func(target interface{}) error, modules []modulex.Module, opts ...Option) error {
	cfg := &options{
		ctx:             context.Background(),
		signals:         []os.Signal{os.Interrupt, syscall.SIGTERM},
		shutdownTimeout: defaultShutdownTimeout,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	managerOpts := append([]modulex.ManagerOption{
		modulex.WithLogger(logger),
		modulex.WithConfigLoader(configLoader),
	}, cfg.managerOpts...)

	mgr, err := modulex.NewManager(managerOpts...)
	if err != nil {
		return fmt.Errorf("app: failed to construct manager: %w", err)
	}

	if cfg.setup != nil {
		if err := cfg.setup(mgr); err != nil {
			return fmt.Errorf("app: setup failed: %w", err)
		}
	}

	for _, mod := range modules {
		if err := mgr.RegisterModule(mod); err != nil {
			return fmt.Errorf("app: failed to register module: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(cfg.ctx, cfg.signals...)
	defer stop()

	if err := mgr.InitModules(ctx); err != nil {
		return fmt.Errorf("app: init failed: %w", err)
	}
	if err := mgr.StartModules(ctx); err != nil {
		return fmt.Errorf("app: start failed: %w", err)
	}

	logger.Info("application running")
	<-ctx.Done()
	logger.Info("shutdown signal received, stopping modules")

	// ctx is already cancelled by this point (that's why we're here), so
	// deriving the shutdown deadline from it would produce an
	// already-expired context and make StopModules fail immediately.
	// WithoutCancel keeps ctx's values without inheriting its cancellation
	// (same precedent as httpx.Serve's own shutdown context).
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()

	if err := mgr.StopModules(stopCtx); err != nil {
		return fmt.Errorf("app: stop failed: %w", err)
	}
	return nil
}

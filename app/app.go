package app

import (
	"context"
	"errors"
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
	ctx             func() context.Context
	signals         []os.Signal
	shutdownTimeout time.Duration
	managerOpts     []modulex.ManagerOption
	setup           func(*modulex.Manager) error
	preStop         []func(context.Context) error
	postStop        []func(context.Context) error
}

// Option configures Run.
type Option func(*options)

// WithContext sets the base context Run derives its signal-aware lifecycle
// context from. Defaults to context.Background().
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		o.ctx = func() context.Context { return ctx }
	}
}

// WithSignals overrides the OS signals that trigger shutdown. Defaults to
// os.Interrupt and syscall.SIGTERM.
func WithSignals(sig ...os.Signal) Option {
	return func(o *options) {
		o.signals = sig
	}
}

// WithShutdownTimeout bounds the final StopModules call. Defaults to 15 seconds.
func WithShutdownTimeout(d time.Duration) Option {
	return func(o *options) {
		o.shutdownTimeout = d
	}
}

// WithManagerOptions passes additional modulex.ManagerOption values through
// to modulex.NewManager.
func WithManagerOptions(opts ...modulex.ManagerOption) Option {
	return func(o *options) {
		o.managerOpts = append(o.managerOpts, opts...)
	}
}

// WithSetup registers a hook that runs against the constructed Manager after
// NewManager but before modules are registered and initialized.
func WithSetup(fn func(*modulex.Manager) error) Option {
	return func(o *options) {
		o.setup = fn
	}
}

// WithPreStop registers a hook that runs immediately before
// modulex.Manager.StopModules, sharing its shutdown-timeout context.
// Hooks run in registration order. A caller with domain shutdown logic
// that must complete before modules stop (e.g. draining in-flight
// requests) should register it here rather than hand-rolling a shutdown
// sequence around Run. An error does not stop StopModules or any
// remaining hooks from running; all errors are joined into Run's return
// value.
func WithPreStop(fn func(context.Context) error) Option {
	return func(o *options) {
		o.preStop = append(o.preStop, fn)
	}
}

// WithPostStop registers a hook that runs immediately after
// modulex.Manager.StopModules, sharing its shutdown-timeout context.
// Hooks run in registration order, and run even if StopModules or an
// earlier hook returned an error, so cleanup of resources set up outside
// the Manager (e.g. shutting down a tracer provider) is never skipped. All
// errors are joined into Run's return value.
func WithPostStop(fn func(context.Context) error) Option {
	return func(o *options) {
		o.postStop = append(o.postStop, fn)
	}
}

// Run constructs a modulex.Manager, registers modules, and drives the full
// Init -> Start -> wait-for-shutdown -> Stop lifecycle. A caller with
// shutdown steps of its own to sequence around StopModules (e.g. an
// application-level Shutdown before it, or a tracer provider's Shutdown
// after it) should use WithPreStop/WithPostStop rather than reimplementing
// this lifecycle by hand.
func Run(logger *slog.Logger, configLoader func(target interface{}) error, modules []modulex.Module, opts ...Option) error {
	if logger == nil {
		logger = slog.Default()
	}

	cfg := &options{
		ctx:             context.Background,
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

	for i, mod := range modules {
		if mod == nil {
			return fmt.Errorf("app: module at index %d must not be nil", i)
		}
		if err := mgr.RegisterModule(mod); err != nil {
			return fmt.Errorf("app: failed to register module %q: %w", mod.Name(), err)
		}
	}

	ctx, stop := signal.NotifyContext(cfg.ctx(), cfg.signals...)
	defer stop()

	// Ensure resources are stopped if Init or Start fails midway
	var fullyStarted bool
	defer func() {
		if !fullyStarted {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
			defer cancel()
			_ = mgr.StopModules(stopCtx)
		}
	}()

	if err := mgr.InitModules(ctx); err != nil {
		return fmt.Errorf("app: init failed: %w", err)
	}
	if err := mgr.StartModules(ctx); err != nil {
		return fmt.Errorf("app: start failed: %w", err)
	}

	fullyStarted = true
	logger.Info("application running")

	<-ctx.Done()
	logger.Info("shutdown signal received, stopping modules")

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.shutdownTimeout)
	defer cancel()

	var errs []error
	for _, hook := range cfg.preStop {
		if err := hook(stopCtx); err != nil {
			errs = append(errs, fmt.Errorf("app: pre-stop hook failed: %w", err))
		}
	}
	if err := mgr.StopModules(stopCtx); err != nil {
		errs = append(errs, fmt.Errorf("app: stop failed: %w", err))
	}
	for _, hook := range cfg.postStop {
		if err := hook(stopCtx); err != nil {
			errs = append(errs, fmt.Errorf("app: post-stop hook failed: %w", err))
		}
	}
	return errors.Join(errs...)
}

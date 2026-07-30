package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	googlegrpc "google.golang.org/grpc"

	"github.com/mediusfy/modulex"
)

// DefaultShutdownTimeout bounds how long Stop waits for
// (*google.golang.org/grpc.Server).GracefulStop to finish before forcing an
// immediate (*google.golang.org/grpc.Server).Stop. It is used by NewServer
// when no WithShutdownTimeout option is given.
const DefaultShutdownTimeout = 10 * time.Second

// Server adapts a *googlegrpc.Server into modulex.Starter and modulex.Stopper
// so a modulex.Manager's lifecycle owns starting it and gracefully stopping
// it — the gRPC analogue of what the httpx package's Serve function does for
// a *http.Server.
//
// # Ownership
//
// Server owns exactly the serve loop and its shutdown. It does not own:
//
//   - The *googlegrpc.Server's construction: the caller builds it (with
//     whatever ServerOptions, interceptors, and credentials it needs — see
//     ServerOptions for a reusable bundle) and registers services on it
//     before passing it to NewServer. Server never calls a registration
//     function itself.
//   - The net.Listener's creation: the caller supplies an already-bound
//     listener (typically via net.Listen), so bind failures surface to the
//     caller before NewServer is ever called, not from Start.
//
// # Lifecycle
//
// Start begins Serve in a background goroutine and returns immediately
// (matching how Manager.StartModules expects Starter.Start to behave: it
// starts background work, it does not block for the server's lifetime).
// Stop performs a graceful shutdown bounded by the configured (or ctx's)
// deadline: it calls GracefulStop and waits for in-flight RPCs to finish. If
// that does not complete before the bound is reached, Stop calls Stop on the
// underlying *googlegrpc.Server (which unblocks GracefulStop by closing the
// listener and aborting all pending RPCs) and returns an error explaining
// that the shutdown was forced. Stop always waits for the Serve goroutine to
// fully exit before returning, so a caller can rely on the listener being
// closed and all resources released once Stop returns, regardless of which
// path was taken.
//
// Stop is idempotent: calling it more than once returns the result of the
// first call without repeating the shutdown.
//
// # The forced fallback's real bound
//
// Closing the listener and transport (what the forced Stop call does)
// cancels the context of every in-flight RPC. That bounds shutdown for any
// handler that behaves correctly — i.e. one that returns once its context is
// canceled, per the standard gRPC/Go handler contract (the same assumption
// net/http's Shutdown makes about handlers checking r.Context().Done()). It
// does not, and cannot, bound a handler that blocks on something unrelated
// to its own context and never checks it: Go has no supported way to force a
// running goroutine to stop from the outside. A handler that ignores
// context cancellation entirely can still block Stop indefinitely — that is
// a bug in the handler, not a gap this package (or grpc-go itself) can close
// for it.
type Server struct {
	grpcServer      *googlegrpc.Server
	listener        net.Listener
	shutdownTimeout time.Duration
	logger          *slog.Logger

	mu       sync.Mutex
	serveErr error
	done     chan struct{}

	stopOnce sync.Once
	stopErr  error
}

// ServerOption configures a Server during construction.
type ServerOption func(*Server)

// WithShutdownTimeout overrides DefaultShutdownTimeout for the bound Stop
// waits on GracefulStop before forcing an immediate Stop. A non-positive
// duration disables the internal bound entirely, so Stop then waits only on
// the ctx passed to it (if that ctx has no deadline either, Stop can block
// until GracefulStop completes on its own).
func WithShutdownTimeout(d time.Duration) ServerOption {
	return func(s *Server) {
		s.shutdownTimeout = d
	}
}

// WithServerLogger sets the logger used to report a forced shutdown or a
// Serve error. If not provided, or if nil, slog.Default() is used.
func WithServerLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// NewServer creates a Modulex-managed lifecycle wrapper around grpcServer,
// serving on listener once Start is called.
//
// grpcServer and listener must not be nil. Register all services (and the
// health service, if desired — see NewHealthServer) on grpcServer before
// calling NewServer, since Server never registers services itself.
func NewServer(grpcServer *googlegrpc.Server, listener net.Listener, opts ...ServerOption) (*Server, error) {
	if grpcServer == nil {
		return nil, errors.New("grpc: server must not be nil")
	}
	if listener == nil {
		return nil, errors.New("grpc: listener must not be nil")
	}

	s := &Server{
		grpcServer:      grpcServer,
		listener:        listener,
		shutdownTimeout: DefaultShutdownTimeout,
		logger:          slog.Default(),
		done:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	return s, nil
}

// Start implements modulex.Starter. It begins serving in a background
// goroutine and returns immediately without blocking module initialization.
// A Serve error (other than the expected error returned after Stop closes
// the server) is captured and surfaced from Stop's return value.
func (s *Server) Start(_ context.Context) error {
	go func() {
		err := s.grpcServer.Serve(s.listener)
		if err != nil && !errors.Is(err, googlegrpc.ErrServerStopped) {
			s.mu.Lock()
			s.serveErr = err
			s.mu.Unlock()
		}
		close(s.done)
	}()
	return nil
}

// Stop implements modulex.Stopper. See the Server doc comment for the exact
// graceful-shutdown-with-bounded-fallback behavior.
func (s *Server) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.stopErr = s.stop(ctx)
	})
	return s.stopErr
}

func (s *Server) stop(ctx context.Context) error {
	shutdownCtx := ctx
	if s.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		shutdownCtx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}

	gracefulDone := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(gracefulDone)
	}()

	var forced bool
	select {
	case <-gracefulDone:
	case <-shutdownCtx.Done():
		forced = true
		// Stop unblocks the pending GracefulStop call by closing the
		// listener and aborting in-flight RPCs immediately.
		s.grpcServer.Stop()
		<-gracefulDone
	}

	// Always wait for the Serve goroutine itself to exit, so Stop's caller
	// can rely on the listener being fully closed and Serve's error (if any)
	// having been recorded once Stop returns.
	<-s.done

	s.mu.Lock()
	serveErr := s.serveErr
	s.mu.Unlock()

	if forced {
		s.logger.Warn("grpc: graceful shutdown did not complete in time, forced stop",
			slog.Duration("timeout", s.shutdownTimeout))
		return fmt.Errorf("grpc: graceful shutdown did not complete before the deadline, forced stop: %w", shutdownCtx.Err())
	}
	if serveErr != nil {
		return fmt.Errorf("grpc: server exited with error: %w", serveErr)
	}
	return nil
}

var (
	_ modulex.Starter = (*Server)(nil)
	_ modulex.Stopper = (*Server)(nil)
)

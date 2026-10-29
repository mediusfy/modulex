// Package httpx serves a Modulex application's HTTP endpoints: handlers
// for health and readiness checks, and Serve, which runs an http.Server as
// a supervised, lifecycle-owned task.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mediusfy/modulex"
)

const defaultCheckTimeout = 5 * time.Second

const (
	// contentTypeHeader is the HTTP header name used to declare the response body's media type.
	contentTypeHeader = "Content-Type"
	// contentTypeJSON is the media type written for all health/readiness JSON responses.
	contentTypeJSON = "application/json"
)

// genericCheckFailureMessage is the message written into a failing check's
// JSON entry by default, instead of the check's real error text. A wrapped
// driver/library error commonly embeds hostnames, DSNs, file paths, or SQL
// fragments, and HealthHandler/ReadinessHandler are typically reachable by
// unauthenticated callers (load balancers, orchestrators, scanners), so the
// real detail is logged server-side instead and only exposed in the
// response body when WithDetailedErrors is explicitly set.
const genericCheckFailureMessage = "failed"

// handlerConfig holds the options shared by HealthHandler and
// ReadinessHandler.
type handlerConfig struct {
	detailed bool
	logger   *slog.Logger
}

// HandlerOption configures HealthHandler and ReadinessHandler.
type HandlerOption func(*handlerConfig)

// WithDetailedErrors makes HealthHandler and ReadinessHandler include each
// failing check's real error text (and a panicking check's recovered
// value) in the JSON response body, instead of the generic
// genericCheckFailureMessage used by default. Only enable this for an
// endpoint that is not reachable by unauthenticated or untrusted callers:
// wrapped driver errors (DSNs, hostnames, file paths, SQL fragments)
// commonly surface through a check's error and would then be exposed
// verbatim to whoever can reach the endpoint.
func WithDetailedErrors() HandlerOption {
	return func(c *handlerConfig) { c.detailed = true }
}

// WithHandlerLogger sets the logger HealthHandler and ReadinessHandler use
// to record a failing check's real error text (or recovered panic value)
// when WithDetailedErrors is not set. If not provided, or nil,
// slog.Default() is used.
func WithHandlerLogger(logger *slog.Logger) HandlerOption {
	return func(c *handlerConfig) { c.logger = logger }
}

func newHandlerConfig(opts []HandlerOption) *handlerConfig {
	cfg := &handlerConfig{logger: slog.Default()}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return cfg
}

// checkResponse is the JSON body written by HealthHandler and ReadinessHandler.
type checkResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func runChecks(ctx context.Context, checks map[string]func(context.Context) error, cfg *handlerConfig) (map[string]string, bool) {
	results := make(map[string]string, len(checks))
	allOK := true

	if len(checks) == 0 {
		return results, true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(checks))

	for name, check := range checks {
		go func(name string, check func(context.Context) error) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					result := genericCheckFailureMessage
					if cfg.detailed {
						result = fmt.Sprintf("panic: %v", r)
					} else {
						cfg.logger.Error("health check panicked",
							slog.String("check", name), slog.Any("panic", r))
					}
					mu.Lock()
					results[name] = result
					allOK = false
					mu.Unlock()
				}
			}()

			// WithTimeout derives from ctx and automatically enforces whichever
			// deadline is shorter (the parent ctx deadline or defaultCheckTimeout).
			checkCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
			defer cancel()

			result := "ok"
			passed := true

			if check == nil {
				result = "check function is nil"
				passed = false
			} else if err := check(checkCtx); err != nil {
				if cfg.detailed {
					result = err.Error()
				} else {
					result = genericCheckFailureMessage
					cfg.logger.Error("health check failed",
						slog.String("check", name), slog.Any("error", err))
				}
				passed = false
			}

			mu.Lock()
			results[name] = result
			if !passed {
				allOK = false
			}
			mu.Unlock()
		}(name, check)
	}

	wg.Wait()
	return results, allOK
}

func writeCheckResponse(w http.ResponseWriter, r *http.Request, checks map[string]func(context.Context) error,
	okStatus, failStatus string, cfg *handlerConfig) {
	results, ok := runChecks(r.Context(), checks, cfg)

	status := okStatus
	code := http.StatusOK
	if !ok {
		status = failStatus
		code = http.StatusServiceUnavailable
	}

	w.Header().Set(contentTypeHeader, contentTypeJSON)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(checkResponse{Status: status, Checks: results})
}

// HealthHandler constructs an http.HandlerFunc for liveness checks. By
// default, a failing check's real error is logged and replaced in the JSON
// response with a generic message (see WithDetailedErrors and
// genericCheckFailureMessage) since this handler is commonly reachable by
// unauthenticated callers.
func HealthHandler(p modulex.HealthCheckProvider, opts ...HandlerOption) http.HandlerFunc {
	cfg := newHandlerConfig(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			w.Header().Set(contentTypeHeader, contentTypeJSON)
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(checkResponse{
				Status: "unhealthy",
				Checks: map[string]string{"provider": "health check provider is nil"},
			})
			return
		}
		writeCheckResponse(w, r, p.HealthChecks(), "ok", "unhealthy", cfg)
	}
}

// ReadinessHandler constructs an http.HandlerFunc for readiness checks. By
// default, a failing check's real error is logged and replaced in the JSON
// response with a generic message (see WithDetailedErrors and
// genericCheckFailureMessage) since this handler is commonly reachable by
// unauthenticated callers.
func ReadinessHandler(p modulex.ReadinessProvider, opts ...HandlerOption) http.HandlerFunc {
	cfg := newHandlerConfig(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			w.Header().Set(contentTypeHeader, contentTypeJSON)
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(checkResponse{
				Status: "not-ready",
				Checks: map[string]string{"provider": "readiness provider is nil"},
			})
			return
		}
		writeCheckResponse(w, r, p.ReadinessChecks(), "ready", "not-ready", cfg)
	}
}

// Serve starts an HTTP server within a modulex.TaskSpawner and manages
// graceful shutdown.
//
// Serve binds the listener synchronously, before returning, mirroring the
// grpc package's pattern of requiring an already-bound net.Listener (see
// grpc.NewServer's doc comment): server.ListenAndServe performs its bind
// internally, inside the spawned task's goroutine, so without this a bind
// failure (e.g. "address already in use") would only surface later, as a
// task error collected when modulex.Manager.StopModules drains task
// errors — by which point Manager.StartModules may already have reported
// success even though nothing is actually listening. Binding here instead
// means a bind failure is returned directly from this call.
//
// Shutdown is driven solely by taskCtx, the long-lived context the
// spawner's Go hands to the spawned task (cancelled only when the spawner
// itself shuts the task down, e.g. via Manager.StopModules) — not by ctx,
// the context passed into this call. ctx is typically a short, bounded
// context such as the caller's Init/Start timeout; shutting the server
// down when that context's deadline passes (as a prior version of this
// function did, by also selecting on ctx.Done()) would stop the server
// right after startup completes instead of letting it run for the task's
// full lifetime.
func Serve(ctx context.Context, spawner modulex.TaskSpawner, name string, server *http.Server,
	shutdownTimeout time.Duration) (*modulex.TaskHandle, error) {
	if spawner == nil {
		return nil, fmt.Errorf("httpx: spawner must not be nil")
	}
	if server == nil {
		return nil, fmt.Errorf("httpx: server must not be nil")
	}

	addr := server.Addr
	if addr == "" {
		addr = ":http"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("httpx: listen on %q: %w", addr, err)
	}

	handle, err := spawner.Go(ctx, name, func(taskCtx context.Context) error {
		serveErrCh := make(chan error, 1)
		go func() {
			serveErrCh <- server.Serve(ln)
		}()

		select {
		case err := <-serveErrCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("httpx: server exited: %w", err)
			}
			return nil
		case <-taskCtx.Done():
		}

		// Detach shutdown context from cancellation using context.WithoutCancel,
		// ensuring in-flight requests can finish during the shutdown timeout
		// window. Derive from taskCtx, not the outer ctx: taskCtx is the
		// context modulex.Manager.Go actually runs this task under, and it
		// already carries this task's own tracer span (re-attached from ctx's
		// span context by the manager before this function ever runs — see
		// Manager.Go) plus the manager's task-lifecycle cancellation
		// generation. The outer ctx is a different, unrelated context tree
		// (typically the caller's Init/Start context) with no such span of
		// its own; deriving from it instead would leave server.Shutdown's
		// spans and logs orphaned from this task's span for the whole
		// shutdown window.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(taskCtx), shutdownTimeout)
		defer cancel()

		shutdownErr := server.Shutdown(shutdownCtx)

		if err := <-serveErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("httpx: server exited: %w", err)
		}
		if shutdownErr != nil {
			return fmt.Errorf("httpx: graceful shutdown failed: %w", shutdownErr)
		}
		return nil
	})
	if err != nil {
		// spawner.Go failed (e.g. a duplicate task name or the spawner is
		// already shutting down): nothing will ever call server.Serve(ln) to
		// take ownership of ln, so close it here to avoid leaking the bound
		// socket.
		_ = ln.Close()
		return nil, err
	}
	return handle, nil
}

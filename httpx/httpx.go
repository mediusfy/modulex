package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// checkResponse is the JSON body written by HealthHandler and ReadinessHandler.
type checkResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func runChecks(ctx context.Context, checks map[string]func(context.Context) error) (map[string]string, bool) {
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
					mu.Lock()
					results[name] = fmt.Sprintf("panic: %v", r)
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
				result = err.Error()
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
	okStatus, failStatus string) {
	results, ok := runChecks(r.Context(), checks)

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

// HealthHandler constructs an http.HandlerFunc for liveness checks.
func HealthHandler(p modulex.HealthCheckProvider) http.HandlerFunc {
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
		writeCheckResponse(w, r, p.HealthChecks(), "ok", "unhealthy")
	}
}

// ReadinessHandler constructs an http.HandlerFunc for readiness checks.
func ReadinessHandler(p modulex.ReadinessProvider) http.HandlerFunc {
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
		writeCheckResponse(w, r, p.ReadinessChecks(), "ready", "not-ready")
	}
}

// Serve starts an HTTP server within a modulex.TaskSpawner and manages graceful shutdown.
func Serve(ctx context.Context, spawner modulex.TaskSpawner, name string, server *http.Server,
	shutdownTimeout time.Duration) (*modulex.TaskHandle, error) {
	if spawner == nil {
		return nil, fmt.Errorf("httpx: spawner must not be nil")
	}
	if server == nil {
		return nil, fmt.Errorf("httpx: server must not be nil")
	}

	return spawner.Go(ctx, name, func(taskCtx context.Context) error {
		serveErrCh := make(chan error, 1)
		go func() {
			serveErrCh <- server.ListenAndServe()
		}()

		select {
		case err := <-serveErrCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("httpx: server exited: %w", err)
			}
			return nil
		case <-ctx.Done():
		case <-taskCtx.Done():
		}

		// Detach shutdown context from cancellation using context.WithoutCancel,
		// ensuring in-flight requests can finish during the shutdown timeout window.
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
}

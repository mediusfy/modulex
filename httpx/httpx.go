// Package httpx provides HTTP glue for Modulex health and readiness checks,
// plus a managed net/http.Server lifecycle, without pulling net/http into the
// core modulex package.
//
// Modulex's core Registry abstracts health (liveness) and readiness checks as
// named functions (see modulex.HealthCheckRegistrar and
// modulex.ReadinessRegistrar), but deliberately stays free of HTTP
// dependencies. This package exists for consumers that expose those checks
// over HTTP:
//
//   - HealthHandler serves the aggregated result of every registered health
//     (liveness) check. A failing check means the process should be
//     restarted.
//   - ReadinessHandler serves the aggregated result of every registered
//     readiness check. A failing check means the instance should be pulled
//     from load balancing, not restarted.
//   - Serve spawns a *http.Server via a modulex.TaskSpawner and shuts it down
//     gracefully when the supplied context is cancelled, removing the
//     "ListenAndServe + select + Shutdown" boilerplate every HTTP-serving
//     consumer would otherwise hand-write.
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

// defaultCheckTimeout bounds an individual check when the caller's request
// context carries no deadline of its own.
const defaultCheckTimeout = 5 * time.Second

// checkResponse is the JSON body written by HealthHandler and ReadinessHandler.
type checkResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// runChecks executes every check concurrently, bounding each one with
// defaultCheckTimeout when ctx has no deadline of its own. It returns a
// per-check result ("ok" or the error message) and whether every check
// passed.
func runChecks(ctx context.Context, checks map[string]func(context.Context) error) (map[string]string, bool) {
	results := make(map[string]string, len(checks))
	allOK := true

	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(checks))

	for name, check := range checks {
		go func(name string, check func(context.Context) error) {
			defer wg.Done()

			checkCtx := ctx
			if _, hasDeadline := ctx.Deadline(); !hasDeadline {
				var cancel context.CancelFunc
				checkCtx, cancel = context.WithTimeout(ctx, defaultCheckTimeout)
				defer cancel()
			}

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

// writeCheckResponse runs checks, then writes the aggregated JSON body and
// status code shared by HealthHandler and ReadinessHandler. okStatus and
// failStatus are the "status" field values used for the all-pass and
// any-fail cases, respectively.
func writeCheckResponse(w http.ResponseWriter, r *http.Request, checks map[string]func(context.Context) error, okStatus, failStatus string) {
	results, ok := runChecks(r.Context(), checks)

	status := okStatus
	code := http.StatusOK
	if !ok {
		status = failStatus
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(checkResponse{Status: status, Checks: results})
}

// HealthHandler returns an http.HandlerFunc that runs every health
// (liveness) check registered on p concurrently and reports the aggregate
// result as JSON.
//
// It responds 200 with {"status":"ok","checks":{...}} if every check passes,
// or 503 with {"status":"unhealthy","checks":{...}} if any check fails. The
// checks map always lists every registered check, with a value of "ok" for
// passing checks and the check's error message for failing ones.
//
// A failing health check means the process is broken and should be
// restarted; wire this handler to an orchestrator's liveness probe.
func HealthHandler(p modulex.HealthCheckProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeCheckResponse(w, r, p.HealthChecks(), "ok", "unhealthy")
	}
}

// ReadinessHandler returns an http.HandlerFunc that runs every readiness
// check registered on p concurrently and reports the aggregate result as
// JSON.
//
// It responds 200 with {"status":"ready","checks":{...}} if every check
// passes, or 503 with {"status":"not-ready","checks":{...}} if any check
// fails. The checks map always lists every registered check, with a value of
// "ok" for passing checks and the check's error message for failing ones.
//
// A failing readiness check means the instance should be pulled from load
// balancing, not restarted; wire this handler to an orchestrator's readiness
// probe.
func ReadinessHandler(p modulex.ReadinessProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeCheckResponse(w, r, p.ReadinessChecks(), "ready", "not-ready")
	}
}

// Serve spawns server.ListenAndServe as a supervised background task via
// spawner.Go under the given name, and gracefully shuts the server down with
// shutdownTimeout when ctx is cancelled.
//
// A modulex.TaskSpawner derives the context its task function runs with from
// the manager's own lifecycle, not from ctx, so Serve also watches the
// task's context: shutdown is triggered by whichever of ctx or the manager's
// shutdown happens first. This means Serve responds correctly both to an
// explicit caller cancellation and to modulex.Manager.StopModules.
//
// http.ErrServerClosed is treated as a clean exit, not an error: both a
// natural server exit and a Shutdown-triggered exit surface as a nil error
// from the returned TaskHandle's Wait. Any other error from ListenAndServe,
// or a Shutdown that does not complete within shutdownTimeout, is returned by
// Wait.
//
// Serve exists to remove the repeated "spawn ListenAndServe, select on
// ctx.Done, Shutdown with a timeout" boilerplate that HTTP-serving consumers
// of modulex would otherwise hand-write for every service.
func Serve(ctx context.Context, spawner modulex.TaskSpawner, name string, server *http.Server, shutdownTimeout time.Duration) (*modulex.TaskHandle, error) {
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

		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		shutdownErr := server.Shutdown(shutdownCtx)

		// Shutdown unblocks ListenAndServe; drain its result so the
		// goroutine above always exits.
		if err := <-serveErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("httpx: server exited: %w", err)
		}
		if shutdownErr != nil {
			return fmt.Errorf("httpx: graceful shutdown failed: %w", shutdownErr)
		}
		return nil
	})
}

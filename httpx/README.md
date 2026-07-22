# Modulex httpx HTTP Adapter

This package provides `net/http` glue for Modulex's health (liveness) and
readiness checks, plus a managed `*http.Server` lifecycle. It depends only on
`net/http` and the core `modulex` package — no third-party router — so the
core `modulex` package can stay free of HTTP dependencies (the same pattern
used by [`modulex/chi`](../chi)).

## Usage

```go
import (
    "net/http"
    "time"

    "github.com/mediusfy/modulex/httpx"
)

mux := http.NewServeMux()
mux.HandleFunc("/healthz", httpx.HealthHandler(manager))
mux.HandleFunc("/readyz", httpx.ReadinessHandler(manager))

server := &http.Server{Addr: ":8080", Handler: mux}
handle, err := httpx.Serve(ctx, manager, "http-server", server, 10*time.Second)
if err != nil {
    return err
}
// handle.Wait() blocks until the server has shut down cleanly or failed.
```

`manager` is anything implementing `modulex.HealthCheckProvider` /
`modulex.ReadinessProvider` (a `*modulex.Manager` satisfies both) and
`modulex.TaskSpawner`.

## Behavior

- `HealthHandler(p modulex.HealthCheckProvider)` runs every registered health
  (liveness) check concurrently, each bounded by the incoming request's
  deadline or a 5-second default when the request carries none. It responds
  `200 {"status":"ok","checks":{...}}` if every check passes, or
  `503 {"status":"unhealthy","checks":{...}}` if any fail. Every registered
  check appears in `checks`, with `"ok"` for passes and the check's error
  message for failures.
- `ReadinessHandler(p modulex.ReadinessProvider)` behaves identically, sourced
  from `ReadinessChecks()` instead, using `"ready"` / `"not-ready"` in place
  of `"ok"` / `"unhealthy"`.
- `Serve(ctx, spawner, name, server, shutdownTimeout)` spawns
  `server.ListenAndServe()` as a supervised task via
  `modulex.TaskSpawner.Go`, and gracefully calls `server.Shutdown` with
  `shutdownTimeout` when either `ctx` or the manager's own shutdown fires
  first. `http.ErrServerClosed` is treated as a clean exit, not an error —
  it surfaces as a nil error from the returned `*modulex.TaskHandle`'s
  `Wait()`.

## Why this exists

Every HTTP-serving consumer of Modulex ends up hand-writing the same
boilerplate: run health/readiness checks and marshal them to JSON, and spawn
`ListenAndServe` alongside a `select` on context cancellation that calls
`Shutdown` with a timeout. `httpx` factors that out once so modules only need
to register named check functions and call `Serve`.

## Testing

```bash
go test ./httpx/...
```

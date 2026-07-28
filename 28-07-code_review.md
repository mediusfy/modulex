# Code Review — modulex

Review of the local commits ahead of `origin/main` (`bd04af8..HEAD`), covering the RabbitMQ/NATS/Watermill event bus hardening, the `app.Run` bootstrap helper, `WithTypedConfig`, health/readiness checks, and the OTel provider.

## Summary
Overall this is solid, well-tested work. Build, `go vet`, and `golangci-lint` are all clean. Error wrapping, table-driven tests, and doc comments are consistent with the project's conventions. Below are the notable findings, ordered by severity.

## Findings

### 1. `rabbitmq.EventBus.Close` — double-close is not idempotent (minor bug)
```go
r.mu.Lock()
if !r.closed {
    r.closed = true
}
tags := append([]string(nil), r.tags...)
```
The `if !r.closed` check is a no-op (it always sets `r.closed = true` regardless). More importantly, `Close` doesn't guard against being called twice: a second call re-cancels an already-nil `cancels` slice (harmless) but will hang or return immediately depending on the state of `r.stopped`. Since `r.stopped` is closed once `active` reaches 0, a second `Close` call reads the already-closed channel and returns `nil` immediately — so it's not deadlocking, but the `if !r.closed` dead code should either be removed or turned into an actual short-circuit (`if r.closed { m.mu.Unlock(); return nil }`) for clarity and to avoid redundant `ch.Cancel` calls on tags that are already gone.

### 2. `rabbitmq.EventBus.Subscribe` — TOCTOU between the `closed` check and consumer registration
```go
r.mu.Lock()
closed := r.closed
r.mu.Unlock()
if closed {
    return fmt.Errorf(...)
}
...
msgs, err := r.ch.Consume(...)   // channel call happens without holding r.mu
...
r.mu.Lock()
if r.closed {
    r.mu.Unlock()
    cancel()
    _ = r.ch.Cancel(tag, false)
    return fmt.Errorf(...)
}
```
This is actually handled reasonably well — there's a second `closed` check after `Consume` succeeds, right before appending to `r.tags`/`r.cancels`. Good defensive coding; no action needed, but worth confirming this is intentional (it is, based on the second check).

### 3. `nats.EventBus.Subscribe` — leaks a goroutine per subscription with no `Close` on early return
```go
n.subs = append(n.subs, sub)
n.cancels = append(n.cancels, cancel)
go func() {
    <-subCtx.Done()
    _ = sub.Unsubscribe()
}()
```
Each `Subscribe` call spawns a dedicated goroutine solely to call `Unsubscribe()` on cancellation, whereas `Close()` already iterates `n.subs` and unsubscribes explicitly. This means on `Close()`, `sub.Unsubscribe()` gets called twice per subscription (once from `Close`'s loop, once from this goroutine reacting to `cancel()`). It's likely harmless (NATS `Unsubscribe` is likely idempotent-safe against a no-op), but it's redundant — one mechanism should suffice. Consider removing the per-subscription goroutine since `Close()` already unsubscribes everything and cancels all contexts.

### 4. Inconsistent failure semantics across the three EventBus adapters
- **rabbitmq**: manual ack/nack, nacks-without-requeue on handler error (good, prevents redelivery storms).
- **nats**: core NATS has no ack semantics — errors are just logged (documented, reasonable).
- **watermill**: acks regardless of handler error and logs (documented, reasonable — avoids infinite redelivery in the in-memory bus).

These differing behaviors are each individually justified and documented with comments cross-referencing each other, which is good. However, the `modulex.EventBus` interface itself still returns nothing from `Subscribe`'s handler beyond the initial registration error, so a consumer of `modulex.EventBus` (any of the three) can't assume anything about redelivery/error semantics — only per-adapter doc comments describe it. If broader consistency matters, consider capturing this policy difference in the top-level `EventBus`/`EventHandler` interface doc in `modulex.go`, not just in each adapter's package doc, since callers coding against the abstraction won't necessarily read adapter internals.

### 5. `otel.NewProviderFromEnv` swallows invalid `OTEL_TRACES_SAMPLER_ARG`
```go
func envFloatOrDefault(key string, fallback float64) float64 {
    v := os.Getenv(key)
    if v == "" {
        return fallback
    }
    f, err := strconv.ParseFloat(v, 64)
    if err != nil {
        return fallback
    }
    return f
}
```
A malformed `OTEL_TRACES_SAMPLER_ARG` silently falls back to `1.0` (always-sample) instead of surfacing an error. Given `NewProviderFromEnv` already returns `(..., error)`, silently ignoring a misconfigured env var (which could cause 100% sampling for a mistyped value in production) seems like an easy trap. Consider returning an error instead of silently defaulting, or at minimum logging a warning.

### 6. `app.Run` — signature takes `configLoader func(target interface{}) error` directly instead of leveraging `WithTypedConfig`
```go
func Run(logger *slog.Logger, configLoader func(target interface{}) error, modules []modulex.Module, opts ...Option) error {
```
This is a minor API design nit: `Run`'s second positional parameter duplicates what `modulex.WithConfigLoader`/`modulex.WithTypedConfig` already do via `WithManagerOptions`. Having both a positional `configLoader` param and an options-based `WithManagerOptions(modulex.WithTypedConfig(cfg))` (as used in `examples/bootstrap/main.go`, which passes `nil` for `configLoader`) is a little redundant/confusing — two different paths to configure the same thing, with the example needing to explicitly pass `nil` to skip the positional one. Consider dropping the positional parameter in favor of exclusively using `WithManagerOptions`, or documenting when to use which.

### 7. `typedconfig.go` — error message uses `%T` on the generic value, which may be misleading for pointer configs
```go
return fmt.Errorf("%w: GetConfig target must be *%T, got %T", ErrConfigTypeMismatch, cfg, target)
```
If `T` is itself instantiated as a pointer type (e.g., `WithTypedConfig(&Config{})`), the message reads "target must be **Config" which is correct but easy to get confused by. Not a bug, just a readability note — no action strictly needed.

## Minor / Nitpick Items
- `rabbitmq.go`'s `logKeyQueue`/`logKeyError` and `nats.go`'s `logKeyTopic`/`logKeyError` — the `logKeyError` constant is duplicated verbatim across two packages. Given the DRY preference, if a third adapter needs the same key it may be worth hoisting shared logging key constants into the core `modulex` package, though duplicating two constants across two small adapter packages is a defensible DRY trade-off to keep adapters dependency-free from each other.
- `modulex.go`'s doc-comment reordering (moving comments to sit directly above their functions) is a nice cleanup with no functional impact.
- Good use of `context.WithoutCancel` in `app.Run` for the shutdown context, with a clear comment explaining why — this correctly avoids the common bug of deriving an already-cancelled shutdown context.
- Test coverage for the new `app`, `typedconfig`, and RabbitMQ ack/nack behavior is thorough and follows table-driven conventions.
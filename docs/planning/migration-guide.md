# Modulex v0 Migration Guide

This guide covers breaking changes introduced during the v0 development series
and how to update existing code. v0 releases do not guarantee API stability;
this document is updated with each breaking change to ease upgrades.

## Migrating `Registry.Go` to the error-returning signature

The supervised task API now requires task functions to return an error and
returns a `*TaskHandle` plus an error from `Go`.

### Before

```go
err := manager.Go(ctx, "worker", func(ctx context.Context) {
    // do work until ctx is cancelled
})
```

### After

```go
handle, err := manager.Go(ctx, "worker", func(ctx context.Context) error {
    // do work until ctx is cancelled
    return nil
})
if err != nil {
    return err
}

// Later, await the task explicitly if needed.
if taskErr := handle.Wait(); taskErr != nil {
    // handle task error
}
```

### What changed and why

- Task functions now return `error` so that supervised task failures can be
  observed and propagated during shutdown.
- `Go` returns `(*TaskHandle, error)` to support explicit waiting and to surface
  registration errors such as duplicate task names or lifecycle state mismatches.

## Migrating event-bus adapters to subpackages

The NATS, RabbitMQ, and Watermill `EventBus` adapters moved from the root
package to dedicated subpackages. Import paths and constructor names changed.

### Before

```go
import "github.com/mediusfy/modulex"

natsBus := modulex.NewNATSEventBus(conn)
rabbitBus := modulex.NewRabbitMQEventBus(ch)
watermillBus := modulex.NewWatermillEventBus(pubSub)
```

### After

```go
import (
    natsadapter "github.com/mediusfy/modulex/nats"
    rabbitadapter "github.com/mediusfy/modulex/rabbitmq"
    watermilladapter "github.com/mediusfy/modulex/watermill"
)

natsBus := natsadapter.NewEventBus(conn)
rabbitBus := rabbitadapter.NewEventBus(ch)
watermillBus := watermilladapter.NewEventBus(0, false, false)
```

### What changed and why

- Adapter types are now named `EventBus` in each subpackage.
- Constructors are now `NewEventBus` in each subpackage.
- This keeps the core package focused on lifecycle orchestration and avoids
  forcing optional framework dependencies on consumers that do not use them.

## Migrating Chi router usage

`NewManager` no longer accepts a Chi router, and `Registry.Router()` is removed.
Applications that use Chi register the router as a typed service and modules
resolve it in `Init`.

### Before

```go
import (
    gochi "github.com/go-chi/chi/v5"
    "github.com/mediusfy/modulex"
)

router := gochi.NewRouter()
mgr := modulex.NewManager(modulex.WithEventBus(router), modulex.WithLogger(eb), modulex.WithConfigLoader(logger), configLoader)

func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
    reg.Router().Get("/api/incidents", m.listIncidents)
    return nil
}
```

### After

```go
import (
    gochi "github.com/go-chi/chi/v5"
    "github.com/mediusfy/modulex"
    modulexchi "github.com/mediusfy/modulex/chi"
)

router := gochi.NewRouter()
mgr, err := modulex.NewManager(modulex.WithEventBus(eb), modulex.WithLogger(logger), modulex.WithConfigLoader(configLoader))
if err != nil {
    // handle error
}
if err := modulexchi.RegisterRouter(mgr, router); err != nil {
    // handle error
}

func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
    r, err := modulexchi.ResolveRouter(reg)
    if err != nil {
        return err
    }
    r.Get("/api/incidents", m.listIncidents)
    return nil
}
```

### What changed and why

- The core package no longer depends on Chi, so consumers that do not serve
  HTTP do not pull in `github.com/go-chi/chi/v5`.
- Chi integration lives in `modulex/chi` and uses the existing typed service
  registry for clean, type-safe wiring.

## Migrating OpenTelemetry tracing

The core package no longer exposes `Registry.Tracer()` or uses the global
OpenTelemetry tracer. Tracing is opt-in via `modulex.WithTracer`.

### Before

```go
import "github.com/mediusfy/modulex"

mgr := modulex.NewManager(modulex.WithEventBus(router), modulex.WithLogger(eb), modulex.WithConfigLoader(logger), configLoader)
// Spans were created using the global OTel tracer from Registry.Tracer().
```

### After

```go
import (
    "github.com/mediusfy/modulex"
    modulexotel "github.com/mediusfy/modulex/otel"
)

mgr, err := modulex.NewManager(eb, logger, configLoader,
    modulex.WithTracer(modulexotel.NewTracer(tp)),
)
if err != nil {
    // handle error
}
```

### What changed and why

- Tracing is now optional. Consumers that do not need it use the built-in
  no-op tracer with zero extra dependencies.
- The OpenTelemetry adapter lives in `modulex/otel` and accepts a
  `TracerProvider`, avoiding reliance on the global tracer provider.

## Migrating to optional `Start` and `Stop` lifecycle capabilities

The `Module` interface no longer requires `Start` and `Stop`. These are now
optional capabilities defined by `modulex.Startable` and `modulex.Stoppable`.

### Before

```go
func (m *MyModule) Name() string        { return "my-module" }
func (m *MyModule) DependsOn() []string { return nil }
func (m *MyModule) Init(ctx context.Context, reg modulex.Registry) error {
    return nil
}
func (m *MyModule) Start(ctx context.Context) error { return nil }
func (m *MyModule) Stop(ctx context.Context) error  { return nil }
```

### After

```go
func (m *MyModule) Name() string        { return "my-module" }
func (m *MyModule) DependsOn() []string { return nil }
func (m *MyModule) Init(ctx context.Context, reg modulex.Registry) error {
    return nil
}
```

If the module needs to start background work, add `Start`:

```go
func (m *MyModule) Start(ctx context.Context) error {
    // start listeners, background tasks, etc.
    return nil
}
```

If the module needs to release resources, add `Stop`:

```go
func (m *MyModule) Stop(ctx context.Context) error {
    // close connections, flush state, etc.
    return nil
}
```

### What changed and why

- Simple modules no longer need no-op `Start` and `Stop` methods.
- Lifecycle capabilities are explicit: `modulex.Startable` for startup work and
  `modulex.Stoppable` for shutdown cleanup.
- The manager skips modules that do not implement these interfaces.

## Migrating to the error-returning `NewManager` constructor

`NewManager` now returns `(*Manager, error)` instead of `*Manager`.
Construction fails if `WithPanicPolicy` is given a value outside the defined
`PanicPolicy` enum. A `nil` event bus is still accepted and defaults to a
no-op implementation, so passing `nil` for local development remains valid.

### Before

```go
mgr := modulex.NewManager(modulex.WithEventBus(eb), modulex.WithLogger(logger), modulex.WithConfigLoader(configLoader))
```

### After

```go
mgr, err := modulex.NewManager(modulex.WithEventBus(eb), modulex.WithLogger(logger), modulex.WithConfigLoader(configLoader))
if err != nil {
    // handle error
}
```

### What changed and why

- Construction can now fail fast on invalid configuration instead of storing
  it silently and surfacing confusing behavior later.
- This is a breaking signature change; all call sites must be updated to
  handle the returned error.

## General upgrade checklist

1. Update imports for any moved adapters.
2. Replace `Registry.Go` call sites with the new signature.
3. Replace `Registry.Router()` usage with `modulex/chi.ResolveRouter` after
   registering the router via `modulex/chi.RegisterRouter`.
4. Replace `Registry.Tracer()` usage with a tracer injected via
   `modulex.WithTracer`.
5. Review task functions to return meaningful errors instead of ignoring them.
6. Remove no-op `Start` and `Stop` methods from modules that do not need them;
   add `Start` only for modules that implement `modulex.Startable` and `Stop`
   only for modules that implement `modulex.Stoppable`.
7. Run `make test-arch` to verify race and lifecycle behavior.

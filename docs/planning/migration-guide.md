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
mgr := modulex.NewManager(router, eb, logger, configLoader)

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
mgr := modulex.NewManager(eb, logger, configLoader)
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

mgr := modulex.NewManager(router, eb, logger, configLoader)
// Spans were created using the global OTel tracer from Registry.Tracer().
```

### After

```go
import (
    "github.com/mediusfy/modulex"
    modulexotel "github.com/mediusfy/modulex/otel"
)

mgr := modulex.NewManager(eb, logger, configLoader,
    modulex.WithTracer(modulexotel.NewTracer(tp)),
)
```

### What changed and why

- Tracing is now optional. Consumers that do not need it use the built-in
  no-op tracer with zero extra dependencies.
- The OpenTelemetry adapter lives in `modulex/otel` and accepts a
  `TracerProvider`, avoiding reliance on the global tracer provider.

## General upgrade checklist

1. Update imports for any moved adapters.
2. Replace `Registry.Go` call sites with the new signature.
3. Replace `Registry.Router()` usage with `modulex/chi.ResolveRouter` after
   registering the router via `modulex/chi.RegisterRouter`.
4. Replace `Registry.Tracer()` usage with a tracer injected via
   `modulex.WithTracer`.
5. Review task functions to return meaningful errors instead of ignoring them.
6. Run `make test-arch` to verify race and lifecycle behavior.

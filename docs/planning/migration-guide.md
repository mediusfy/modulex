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

## General upgrade checklist

1. Update imports for any moved adapters.
2. Replace `Registry.Go` call sites with the new signature.
3. Review task functions to return meaningful errors instead of ignoring them.
4. Run `make test-arch` to verify race and lifecycle behavior.

# Modulex Lifecycle Guide

This guide describes the Modulex manager lifecycle, how transitions are
enforced, and how failures are handled at each stage.

## Lifecycle states

The manager moves through the following states:

```text
configuring -> initializing -> initialized -> starting -> running -> stopping -> stopped
```

`InitModules` and `StartModules` failures transition the manager directly to
`stopped` after rolling back successfully initialized/started modules.

`Start` and `Stop` are optional lifecycle capabilities. Modules that need to run
background work implement `modulex.Startable`; modules that own resources that
must be released implement `modulex.Stoppable`. The manager skips modules that
do not implement these interfaces, so simple modules do not require no-op
methods.

- **Configuring**: modules and services can be registered. No lifecycle
  operations may run.
- **Initializing**: `InitModules` is running. Modules are initialized in
  topological order.
- **Initialized**: all modules initialized successfully. `StartModules` may now
  be called.
- **Starting**: `StartModules` is running. Modules are started in topological
  order.
- **Running**: all modules started successfully. The application is running.
- **Stopping**: `StopModules` is running. Modules are stopped in reverse
  topological order.
- **Stopped**: the manager has shut down. No further lifecycle operations may
  run except idempotent `StopModules` calls.

Invalid transitions return `ErrInvalidLifecycleState`.

## Initialization

`InitModules` performs these steps:

1. Validate the dependency graph (detect cycles, missing dependencies, self
   dependencies, and invalid dependency names).
2. Sort modules topologically.
3. Call `Init` on each module in order.
4. If any module fails, roll back successfully initialized modules by calling
   `Stop` (for modules that implement `modulex.Stoppable`) in reverse order.

### Example

```go
manager, err := modulex.NewManager(modulex.WithEventBus(eb), modulex.WithLogger(logger))
if err != nil {
    return err
}

if err := manager.RegisterModule(modA); err != nil {
    return err
}
if err := manager.RegisterModule(modB); err != nil {
    return err
}

if err := manager.InitModules(context.Background()); err != nil {
    return err
}
```

### Rollback on init failure

If `module-b` fails to initialize after `module-a` succeeded, `module-a.Stop`
runs before the error is returned for modules that implement
`modulex.Stoppable`. Modules that do not implement `Stoppable` are skipped.
Errors from the failed init and from rollback stops are joined.

## Startup

`StartModules` performs these steps:

1. Verify the manager is in the `initialized` state.
2. Call `Start` on each module that implements `modulex.Startable` in
   topological order.
3. If any module fails, stop modules that already started (and implement
   `modulex.Stoppable`) in reverse order.

### Rollback on start failure

If `module-b` fails to start after `module-a` succeeded, `module-a.Stop` runs
before the error is returned. The original start error and any rollback stop
errors are joined.

Modules that were never reached are not started and are not stopped.

## Shutdown

`StopModules` performs these steps:

1. Cancel and await supervised background tasks.
2. Call `Stop` on each started module that implements `modulex.Stoppable` in
   reverse topological order.
3. Close the configured `EventBus`.

The `EventBus` is closed last so that a module's `Stop` can still publish or
rely on the bus while it runs. The manager only owns and closes the
`EventBus` itself — the `nats` and `rabbitmq` adapters wrap a caller-supplied
connection or channel and leave it open after `Close`, since the caller
created it and may share it outside the module lifecycle. The `watermill`
adapter is the exception: it creates its own in-memory `GoChannel` and shuts
it down in `Close`. Modules that create their own resources should release
them in `Stop`, implementing `modulex.Stoppable`.

### Idempotency

`StopModules` is idempotent. Calling it multiple times returns `nil` after the
first successful call.

### Context cancellation

If the caller's context is cancelled during `StopModules`, the manager still
attempts to finish shutdown but returns an error wrapping `context.Canceled`,
joined with any other errors.

## Task supervision

Modules can start background tasks during `Init` or `Start` using
`Registry.Go`. Tasks receive a lifecycle-owned context that is cancelled when
shutdown begins.

```go
func (m *MyModule) Init(ctx context.Context, reg modulex.Registry) error {
    _, err := reg.Go(ctx, "heartbeat", func(ctx context.Context) error {
        ticker := time.NewTicker(time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-ticker.C:
                // do work
            }
        }
    })
    return err
}
```

Tasks are awaited before modules are stopped. Task errors are joined into the
`StopModules` error.

## Error handling conventions

- Use `errors.Is` with the exported sentinel errors (`ErrCircularDependency`,
  `ErrDependencyNotFound`, etc.).
- Lifecycle errors may wrap multiple failures with `errors.Join`; inspect them
  with `errors.Is` or iterate with `errors.Unwrap`.
- Modules should return actionable errors from `Init`, `Start`, and `Stop`.
- Implement `modulex.Startable` only when the module begins background work or
  listeners during startup.
- Implement `modulex.Stoppable` only when the module owns resources that must be
  released during shutdown.

## Checklist for module authors

1. Declare dependencies in `DependsOn` using stable names.
2. Do not register new services after `Init` returns; service registration is
   only allowed during the `Configuring` and `Initializing` states.
3. Start background work only through `Registry.Go` so shutdown can await it.
4. Return promptly from `Stop` when the context is cancelled.
5. Do not call lifecycle methods directly on the manager from inside a module.

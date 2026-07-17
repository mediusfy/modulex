# Modulex Task Supervision Guide

This guide covers how to run background work inside a Modulex-managed module,
how the manager supervises those tasks, and how task failures and panics are
handled.

## When to use supervised tasks

Use `Registry.Go` when a module needs long-running background work, such as:

- A heartbeat or keep-alive loop.
- A background processor that consumes from a queue.
- A periodic cleanup job.
- Any goroutine that should not outlive the module lifecycle.

Avoid using raw `go` statements inside modules; the manager cannot await,
cancel, or recover them during shutdown.

## Starting a task

Tasks are started from within `Init` or `Start` using the module's
`Registry`:

```go
func (m *MyModule) Init(ctx context.Context, reg modulex.Registry) error {
    handle, err := reg.Go(ctx, "worker", func(ctx context.Context) error {
        for {
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-m.work:
                if err := m.process(); err != nil {
                    return err
                }
            }
        }
    })
    if err != nil {
        return err
    }
    m.workerHandle = handle
    return nil
}
```

`Go` returns a `*TaskHandle` and an error. The handle can be used to wait for
the task, inspect its final error, or read its name.

## Lifecycle-owned context

The context passed to the task function is cancelled when the manager begins
shutdown. Tasks should listen to `ctx.Done()` and return promptly. The manager
waits for all tasks to finish before stopping modules. OpenTelemetry trace
context from the caller is propagated into the task goroutine.

If a task does not respect cancellation, `StopModules` still proceeds after a
timeout and reports the timeout error.

## Waiting for a task

Use `TaskHandle.Wait` to block until the task exits:

```go
if err := m.workerHandle.Wait(); err != nil {
    // task exited with an error
}
```

## Task errors

If a task function returns a non-nil error, the error is stored in the task
handle and joined into the error returned by `StopModules`. This ensures that
background failures are not lost during shutdown.

## Panic policy

By default, the manager recovers from panics in task functions, logs them,
records them as task errors, and continues shutdown. You can configure the
behavior with a panic policy when constructing the manager:

```go
manager := modulex.NewManager(eb, logger, nil,
    modulex.WithPanicPolicy(modulex.PanicPolicyPropagate),
)
```

Available policies:

- `PanicPolicyLog` (default): recover the panic, log it, and record it as a
  task error.
- `PanicPolicyPropagate`: re-panic after cleanup, useful for crash-only testing.

## Restrictions

- Task names must be unique within a manager. Duplicate names return
  `ErrDuplicateTask`.
- Task names must not be empty. Empty names return `ErrInvalidTaskName`.
- New tasks cannot be started after `StopModules` has begun; such calls return
  `ErrRegistryLocked`.
- The task function must not be nil.

## Best practices

1. Return `ctx.Err()` when the task context is cancelled.
2. Do not leak goroutines that are not tied to the task context.
3. Keep task functions focused on a single responsibility.
4. Log meaningful errors before returning them so they appear in module logs.
5. Avoid blocking indefinitely inside a task; use timeouts or the lifecycle
   context for cancellation.

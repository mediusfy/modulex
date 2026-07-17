# Modulex Error Handling Guide

This guide describes how Modulex surfaces errors, how to interpret them, and
how to write modules that participate cleanly in the lifecycle error model.

## Sentinel errors

Modulex exports named sentinel errors for common failure modes. Use
`errors.Is` to detect them:

| Sentinel | Meaning |
| -------- | ------- |
| `ErrModuleNil` | A `nil` module was passed to `RegisterModule`. |
| `ErrInvalidModuleName` | A module name is empty or whitespace-only. |
| `ErrCircularDependency` | Module dependencies contain a cycle. |
| `ErrDependencyNotFound` | A module depends on another module that was not registered. |
| `ErrSelfDependency` | A module declares itself as a dependency. |
| `ErrInvalidDependencyName` | A dependency name is empty or whitespace-only. |
| `ErrDuplicateModule` | Two modules share the same name. |
| `ErrDuplicateService` | Two services share the same key. |
| `ErrInvalidServiceName` | A service key is empty or whitespace-only. |
| `ErrServiceNotFound` | A requested service is not registered. |
| `ErrServiceTypeMismatch` | A typed service key does not match the registered value. |
| `ErrRegistryLocked` | Registration or task creation happened at an invalid lifecycle state (e.g., after initialization completed or while stopping). |
| `ErrInvalidTaskName` | A task name is empty or whitespace-only. |
| `ErrDuplicateTask` | Two tasks share the same name. |
| `ErrInvalidLifecycleState` | A lifecycle method was called in the wrong state. |

## Lifecycle errors

`InitModules`, `StartModules`, and `StopModules` may return joined errors.
Inspect them with `errors.Is` for sentinel detection or iterate with
`errors.Unwrap` for full detail.

```go
err := manager.StartModules(ctx)
if err != nil {
    if errors.Is(err, modulex.ErrInvalidLifecycleState) {
        // StartModules was called before InitModules succeeded
    }
    // log the full error; it may contain multiple causes
}
```

## Rollback and joined errors

When `InitModules` or `StartModules` fails, successfully initialized/started
modules are stopped in reverse order. The returned error joins:

1. The original init/start failure.
2. Any errors returned by module `Stop` methods during rollback.

Always check the full joined error before deciding whether the application can
continue.

## Module-authored errors

Modules should return concise, actionable errors from `Init`, `Start`, and
`Stop`. Include the module name and the operation that failed:

```go
func (m *MyModule) Init(ctx context.Context, reg modulex.Registry) error {
    if err := m.db.Ping(ctx); err != nil {
        return fmt.Errorf("%s: failed to ping database: %w", m.Name(), err)
    }
    return nil
}
```

Do not swallow errors silently. If an error is non-fatal for the module but
might matter to the operator, log it and return it.

## Task errors

Background tasks return errors through their `TaskHandle`. The manager collects
task errors when awaiting tasks during shutdown and joins them into the
`StopModules` error. Task errors are also collected if `InitModules` or
`StartModules` fails and the manager waits for running tasks during rollback.
A task that returns `ctx.Err()` when cancelled is normal shutdown behavior;
other errors indicate a problem worth investigating.

## Context cancellation

Lifecycle methods accept a `context.Context`. If the context is cancelled, the
manager stops work as soon as possible and returns an error wrapping
`context.Canceled`. Do not retry lifecycle operations after cancellation;
inspect the manager state and decide whether to restart the process.

## Recommendations

1. Use `errors.Is` for sentinel detection, not string comparison.
2. Wrap module errors with `fmt.Errorf("...: %w", err)` to preserve cause chains.
3. Log the full error at the composition root; individual modules should return
   errors rather than log and discard them.
4. Treat `StopModules` errors as serious: they may indicate incomplete
   cleanup or data loss.
5. Use `errors.Join` explicitly when aggregating errors in your own code to
   match Modulex's behavior.

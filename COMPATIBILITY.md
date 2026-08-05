# Compatibility Policy

This document describes the compatibility and support commitments for Modulex.

## Go versions

Modulex supports the two most recent stable Go releases at the time of each
tag. The `go.mod` file states the minimum Go version required to build Modulex.
Currently CI tests against Go 1.26; a version matrix covering the minimum
supported and latest stable Go releases will be added before v1.

Bumping the minimum Go version is considered a minor change during v0 and a
breaking change after v1.

## Module version compatibility

### v0.x releases

Modulex is currently in v0. While we try to keep the public API stable within
minor v0 releases, **v0 releases carry no backward-compatibility guarantee**.
Patch releases fix bugs without changing the public API. Minor releases may
introduce API changes. Consumers should pin to an exact v0 version or update
with care.

### v1.x releases

Once Modulex reaches v1, it will follow [Semantic Versioning](https://semver.org/):

- **Major releases** may introduce breaking API changes.
- **Minor releases** add functionality in a backward-compatible manner.
- **Patch releases** fix bugs without changing the public API.

The public API surface is defined by the exported identifiers in the
`github.com/mediusfy/modulex` module and its sub-packages.

## Sub-package stability

Framework adapter sub-packages (for example `modulex/nats`, `modulex/rabbitmq`,
and `modulex/watermill`) are part of the same Go module and therefore share
the module version. They follow the same compatibility policy as the core
package and are released together.

## Lifecycle and Typed-Wiring API Guarantees

This section makes the policy above concrete for the two APIs at the heart of
every Modulex consumer: the `Manager` lifecycle state machine (`modulex.go`)
and the typed-wiring helpers (`wire.go`: `Key[T]`, `Provide`, `Resolve`). It
is the reference for deciding whether a proposed change to these APIs is a
patch, minor, or major change once Modulex reaches v1.

`make check-api-compat` (backed by `scripts/check-api-compat.sh`, using
`golang.org/x/exp/cmd/apidiff`) is already wired into CI
(`.github/workflows/ci.yml`) and runs on every PR, diffing the working
tree's exported API against the latest git tag for the core package and
every public sub-package (`app`, `chi`, `nats`, `rabbitmq`, `watermill`,
`otel`, `workerpool`).
It is advisory during v0 (reports incompatibilities but does not fail the
build, per the v0 policy above); it is intended to be run with `-strict`
once the project reaches v1, at which point an incompatible diff against the
latest tag fails CI for anything but a major release. apidiff only inspects
exported signatures and doc comments, so it mechanically enforces the
*signature* half of the guarantees below. The *behavioral* half (error
sentinel stability, state-transition rules) is enforced by the regression
test suite (`modulex_test.go`, `wire_test.go`), including under `go test
-race` — apidiff would not catch a change that kept a function's signature
identical but altered what it returns in a given state.

### `LifecycleState`

The state values and their only valid transition graph are:

```
StateConfiguring -> StateInitializing -> StateInitialized -> StateStarting -> StateRunning -> StateStopping -> StateStopped
```

`StateInitializing` and `StateStarting` also have a failure exit: if a
module fails to `Init` or `Start`, `InitModules`/`StartModules` roll back
already-initialized/started modules and the manager moves directly to
`StateStopped` (the current implementation does not pass through
`StateStopping` on this path). `StopModules` is idempotent from
`StateStopped`/`StateStopping` (a second call returns `nil` without
re-running shutdown logic) and can also be called directly from
`StateConfiguring` or `StateInitialized` (states in which no module is
currently running) to move straight to `StateStopped`.

`StopModules` rejects — with `ErrInvalidLifecycleState` — a call that lands
while `InitModules` or `StartModules` is concurrently running on another
goroutine (i.e. the manager is in `StateInitializing` or `StateStarting`).
Those two phases iterate modules without holding the manager's state lock for
the duration of the phase, so a concurrent `StopModules` cannot safely tear
down tasks and the event bus mid-phase; callers that want to abort an
in-flight `InitModules`/`StartModules` call must cancel the `context.Context`
passed to it and call `StopModules` once it returns (see the MOD-58 fix in
`CHANGELOG.md` for the regression this closed).

Compatibility rules for this enum, given `LifecycleState.String()` already
has a `default: "unknown"` branch and callers are expected to use a `switch`
rather than ordinal comparison:

- **Minor-compatible:** appending a new value after `StateStopped` in the
  `const` block. Existing values keep their numeric identity.
- **Breaking (major-only):** inserting a new value in the middle of the
  sequence, or reordering/renumbering existing values — either changes the
  underlying `int` of an existing constant, which breaks any caller doing
  ordinal comparison (`state < StateRunning`) or persisting the numeric
  value.
- **Breaking (major-only):** removing or renaming an existing state.

### Lifecycle methods covered

`InitModules`, `StartModules`, `StopModules`, `RegisterModule`,
`RegisterService`, `ResolveService`, `Go`, `RegisterHealthCheck`,
`RegisterReadinessCheck`, and `State` are covered by this policy. "Covered"
means:

- **Signature stability**, per the usual Go API-compatibility rules apidiff
  checks (parameter types, return types, added/removed methods).
- **Error-sentinel stability.** Each of these methods returns one of the
  package-level sentinel errors (`ErrInvalidLifecycleState`,
  `ErrRegistryLocked`, `ErrServiceNotFound`, `ErrDuplicateModule`,
  `ErrDuplicateService`, `ErrDuplicateTask`, `ErrModuleNil`,
  `ErrInvalidModuleName`, `ErrInvalidServiceName`, `ErrInvalidTaskName`,
  `ErrCircularDependency`, `ErrDependencyNotFound`, `ErrSelfDependency`,
  `ErrInvalidDependencyName`, `ErrInvalidHealthCheckName`,
  `ErrInvalidReadinessCheckName`) for a given failure, always wrapped with
  `%w` so `errors.Is` against the sentinel keeps working. Which sentinel is
  returned for which failure is part of the guarantee, not an implementation
  detail.
- **Documented state-transition behavior**, concretely: `RegisterModule`
  only succeeds in `StateConfiguring`; `RegisterService` succeeds in
  `StateConfiguring` or `StateInitializing` (so a module's `Init` may
  register services); `InitModules` only succeeds once, from
  `StateConfiguring` (a second call returns `ErrInvalidLifecycleState`
  rather than re-running); `StartModules` only succeeds from
  `StateInitialized`; `Go` is rejected once the manager has entered
  `StateStopping`/`StateStopped`; and `StopModules`'s idempotency and
  rejection rules are as described under `LifecycleState` above.

### `wire.go` typed-wiring API (`Key[T]`, `Provide`, `Resolve`)

- `Key[T]`'s internal representation (currently a single unexported `name
  string` field) is not part of the compatibility guarantee. `Key[T]` has no
  exported fields and is only ever constructed via `NewKey`, so adding
  unexported fields to it in the future is a compatible change as long as
  `NewKey`, `Key[T].Name()`, and the meaning of the type parameter `T` are
  unchanged.
- `Provide`/`Resolve`'s signatures are covered by the same signature-stability
  guarantee as any other exported generic function.
- Error-sentinel behavior is part of the guarantee: `Provide` returns
  `ErrInvalidServiceName` for an empty/whitespace key and otherwise forwards
  whatever `Registry.RegisterService` returns (e.g. `ErrRegistryLocked`,
  `ErrDuplicateService`); `Resolve` returns `ErrInvalidServiceName` for an
  empty key, `ErrServiceNotFound` if the key was never registered, and
  `ErrServiceTypeMismatch` if the registered value cannot be type-asserted to
  `T`. Callers using `errors.Is` against these sentinels can rely on that
  continuing to hold.

## Deprecations

After v1, deprecated APIs will be marked with a `// Deprecated:` comment at
least one minor release before they are removed. Removal only happens in a
major release. During v0 the API may evolve more quickly; deprecations will
still be noted in the changelog when practical, but the formal deprecation
window does not apply.

## Reporting compatibility issues

If an upgrade breaks your build in a way that contradicts this policy, please
open a [GitHub issue](https://github.com/mediusfy/modulex/issues) with a
minimal reproduction. See [CONTRIBUTING.md](CONTRIBUTING.md) for issue and
pull-request guidelines.

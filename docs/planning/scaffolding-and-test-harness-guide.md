# Scaffolding and Test Harness Guide

This guide documents MOD-56, the fifth and final roadmap item from
[ADR-0031](../adr/adr-0031-modulex-value-and-specialization-roadmap.md): a
small scaffolding tool that generates the recommended
domain/ports/service/adapters/module.go layout, and a reusable test harness
(`modtest`) that verifies a module against Modulex's lifecycle contract.

It covers:

- [The scaffolding tool](#the-scaffolding-tool) — CLI usage, generated
  layout, and why constructor injection is the default.
- [The `modtest` harness](#the-modtest-harness) — every helper's signature,
  exactly what it verifies, and how generic each one is.
- [Worked example](#worked-example) — generating a module and running the
  harness against it end to end.

## The scaffolding tool

`tools/scaffold` is a standalone Go module (its own `go.mod`, following the
same pattern as `tools/modboundary`) that generates a new feature module
using `text/template` and `go:embed` — stdlib only, no code-generation
framework or third-party templating dependency. It is intentionally small:
a fixed set of templates rendered once, not an interactive wizard.

### CLI usage

```console
$ go run ./tools/scaffold/cmd/scaffold -name billing -out examples
generated examples/billing:
  examples/billing/domain/billing.go
  examples/billing/ports/repo.go
  examples/billing/ports/service.go
  examples/billing/service/service.go
  examples/billing/service/service_test.go
  examples/billing/adapters/inmemory_repo.go
  examples/billing/module.go
  examples/billing/module_test.go
  examples/billing/README.md
```

Because `tools/scaffold` is its own Go module, it cannot be run with `go
run ./tools/scaffold/...` from the repository root (that would try to
resolve the command inside the root module). Either build it once and run
the binary, or `cd tools/scaffold` first:

```console
$ cd tools/scaffold && go build -o /tmp/modulex-scaffold ./cmd/scaffold
$ cd ../.. && /tmp/modulex-scaffold -name billing -out "$(pwd)/examples"
```

Flags:

| Flag       | Required | Description                                                                                                    |
| ---------- | -------- | ---------------------------------------------------------------------------------------------------------------- |
| `-name`    | yes      | The feature name, e.g. `billing`. Normalized into a kebab-case module name, a Go package identifier, and a PascalCase domain type name (see [Name normalization](#name-normalization)). |
| `-out`     | yes      | The parent directory to generate into. A new directory named after the normalized feature name is created under it (e.g. `-out examples -name billing` creates `examples/billing`). |
| `-module`  | no       | Overrides auto-detection of the Go import path corresponding to `-out`. By default, the tool walks up from `-out` looking for the nearest `go.mod` and computes the import path from its `module` directive; use `-module` when generating into a directory that doesn't yet sit under a `go.mod` on disk, or when auto-detection would guess wrong. |
| `-force`   | no       | Overwrites the target directory if it already exists and is non-empty. Without it, `Generate` refuses to touch a non-empty existing directory. |

### Name normalization

`-name` is split into words on any run of non-alphanumeric characters (so
`"billing thing"`, `"billing-thing"`, and `"billing_thing"` all normalize
the same way; a name given in camelCase is treated as a single word — this
is a small generator, not a full identifier-casing library). From those
words:

- The **kebab-case module name** (e.g. `billing-thing`) becomes the
  directory name under `-out` and the string `Module.Name()` returns.
- The **Go package identifier** (e.g. `billingthing`) is the kebab name
  with separators removed, since Go package names cannot contain `-` or
  `_`. It names `module.go`'s own package.
- The **PascalCase domain type name** (e.g. `BillingThing`) names the
  struct generated in `domain/`.

### Generated layout

The generated layout mirrors `examples/hexagonal/incident`, this
repository's hand-written exemplar of the recommended layout:

```text
<feature>/
  domain/<pkg>.go        - the <TypeName> value type
  ports/repo.go          - the outbound Repository port
  ports/service.go       - the inbound Service port, plus its typed ServiceKey
  service/service.go     - the Service implementation (constructor-injected)
  service/service_test.go
  adapters/inmemory_repo.go - an in-memory Repository, exposing Closed() for tests
  module.go              - the modulex.Module composition root
  module_test.go         - runs the module against modtest
  README.md              - marks the directory as generated, documents regeneration
```

### Constructor injection is the default; typed service location is opt-in

The generated `service.New(repo ports.Repository) ports.Service` takes its
dependency as a constructor parameter — this is the default and the
recommended pattern. The generated `module.go`'s `Init` wires it exactly
this way:

```go
func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
	m.repo = adapters.NewInMemoryRepository()
	m.svc = service.New(m.repo) // constructor injection: the generated default.

	return modulex.Provide(reg, ports.ServiceKey, m.svc)
}
```

The `modulex.Provide(reg, ports.ServiceKey, m.svc)` call is the **optional**
alternative: it additionally registers the service under a typed
`modulex.Key` (`ports.ServiceKey`) so a different module that cannot import
this one's `service` package directly can still reach it:

```go
svc, err := modulex.Resolve(reg, ports.ServiceKey)
```

Generated code and its README both document this as an opt-in escape
hatch, not the default — prefer passing dependencies through constructors
the way `module.go`'s own `Init` does, and reach for `modulex.Resolve` only
when you have a concrete reason to.

### Regenerating

Regeneration overwrites every file `Generate` writes (pass `-force`). Keep
any custom logic in files this tool does not own, or expect it to be
clobbered on the next regeneration — the generated `README.md` repeats this
warning in the directory itself.

## The `modtest` harness

`modtest` (package `github.com/mediusfy/modulex/modtest`) provides
composable, `testing.T`-based helpers a module author calls from their own
`_test.go` file to verify a module against Modulex's lifecycle contract. It
is not a custom test runner or DSL — every helper is an ordinary Go
function taking a `modtest.TB` (a small interface every `*testing.T`
already satisfies) as its first argument, called like any other test
helper:

```go
func TestBillingModuleLifecycle(t *testing.T) {
	modtest.AssertLifecycleOrder(t, billing.NewModule())
}
```

Every helper constructs its own private `*modulex.Manager` (or drives the
module directly); none of them share state, so they can be called from
independent test functions or subtests without interfering with each
other.

### Genericity summary

| Property             | Helper(s)                                                        | Fully generic? |
| --------------------- | ----------------------------------------------------------------- | -------------- |
| Lifecycle ordering    | `AssertLifecycleOrder`                                             | Yes |
| Rollback              | `AssertRollbackOnInitFailure`, `AssertRollbackOnStartFailure`      | Yes |
| Cancellation          | `AssertRespectsCancellation`                                       | Yes, with a caveat (see below) |
| Deadlines             | `AssertRespectsDeadline`                                            | Yes, with the same caveat |
| Health/readiness      | `AssertHealthCheck`, `AssertReadinessCheck`                         | Yes |
| Resource ownership    | `AssertResourceOwnership`                                           | **No** — requires cooperation |

"Fully generic" means the helper works against any `modulex.Module` without
requiring anything beyond the standard `Module`/`Starter`/`Stopper`
interfaces. Resource ownership is the one exception, documented in detail
below.

### `modtest.Boot`

```go
func Boot(t TB, mods ...modulex.Module) *modulex.Manager
```

Registers `mods` on a fresh `*modulex.Manager`, drives `InitModules` then
`StartModules` with `context.Background()`, fails the test immediately (via
`t.Fatalf`) if either phase errors, and registers a `t.Cleanup` that calls
`StopModules`. Returns the running `*modulex.Manager` for ad hoc inspection
(`HealthChecks`, `ReadinessChecks`, `ExportDAG`, `ModuleContract`,
`ResolveService`, ...) beyond what the `Assert*` helpers below cover.

### Lifecycle ordering

```go
func AssertLifecycleOrder(t TB, mods ...modulex.Module)
```

Wraps each of `mods` with an `OrderRecorder` (see below), registers them on
a fresh `*modulex.Manager`, drives `InitModules` → `StartModules` →
`StopModules` with `context.Background()`, and asserts:

- Every module's `Init` was recorded.
- A module's `Start` (if it implements `modulex.Starter`) happened after
  its own `Init`.
- A module's `Stop` (if it implements `modulex.Stopper`) happened after its
  own `Start` (or `Init`, if it has no `Start`).
- For every `DependsOn` edge, the dependency's `Init` happened before the
  dependent's `Init`, and the dependency's `Stop` happened after the
  dependent's `Stop` (reverse teardown order) — whenever both sides
  recorded that phase.

This detects both startup-ordering regressions (a module starting before
its dependencies finished initializing) and shutdown-ordering regressions
(a dependency torn down before the modules that depend on it) — the two
kinds of regression the harness is explicitly required to catch.

`modtest.OrderRecorder` and `modtest.Wrap(mod modulex.Module, rec
*OrderRecorder) modulex.Module` are exported separately for module authors
who want custom ordering assertions beyond what `AssertLifecycleOrder`
checks. `Wrap` returns a module that implements exactly the optional
lifecycle interfaces (`modulex.Starter`, `modulex.Stopper`) the wrapped
module itself implements, so wrapping never changes how a `Manager` treats
it.

### Rollback

```go
func AssertRollbackOnInitFailure(t TB, modUnderTest modulex.Module)
func AssertRollbackOnStartFailure(t TB, modUnderTest modulex.Module)
```

Each registers `modUnderTest` alongside a harness-provided module
(`modtest.NewFailingModule`) that depends on it and always fails `Init` (or
`Start`, respectively), drives the corresponding `Manager` phase (expected
to fail), and asserts:

- The phase's error wraps the induced failure (`errors.Is`).
- If `modUnderTest` implements `modulex.Stopper`, its `Stop` ran during the
  resulting rollback. If it does not implement `Stopper`, there's nothing
  to verify and the helper logs that instead of failing.

`modtest.NewFailingModule(name string, deps []string, phase Phase, err
error) modulex.Module` is exported for custom rollback scenarios beyond
what the two `Assert*` helpers construct automatically.

### Cancellation and deadlines

```go
func AssertRespectsCancellation(t TB, mod modulex.Module, phase Phase, grace time.Duration)
func AssertRespectsDeadline(t TB, mod modulex.Module, phase Phase, deadline, grace time.Duration)
```

`Phase` is one of `modtest.PhaseInit`, `modtest.PhaseStart`, or
`modtest.PhaseStop`. Each helper drives whatever earlier phases are needed
to reach the state `phase` requires (e.g. `Init` before testing `Start`)
with an uncancellable context, then invokes the phase under test in a
goroutine with a context that is cancelled immediately (for
`AssertRespectsCancellation`) or given a fixed deadline (for
`AssertRespectsDeadline`). If the call has not returned within `grace`
afterward, the helper fails the test, reporting a likely cancellation or
deadline regression.

Both helpers drive `mod`'s lifecycle method **directly** rather than
through `Manager.InitModules`/`StartModules`/`StopModules`. This is
deliberate: those `Manager` methods only check `ctx.Err()` *before*
invoking each module in sequence, so calling them with an already-cancelled
context would abort before ever calling the module under test's method — a
false pass that would never exercise the module's own cancellation
handling. Calling the method directly is the only way to genuinely probe
whether a specific module's `Init`/`Start`/`Stop` observes `ctx.Done()`
while it is running.

**Caveat:** the module must actually block on something for cancellation to
have anything to interrupt. A module whose `Init`/`Start`/`Stop` always
returns quickly regardless of `ctx` trivially "passes" without ever being
meaningfully exercised — this is a property of testing blocking behavior in
general, not something the harness can work around.

### Health and readiness

```go
func AssertHealthCheck(t TB, mod modulex.Module, name string, wantErr bool)
func AssertReadinessCheck(t TB, mod modulex.Module, name string, wantErr bool)
```

Registers `mod`, drives `InitModules` (where a well-behaved module
registers its checks via `reg.RegisterHealthCheck`/
`reg.RegisterReadinessCheck`), looks up the check named `name`, and runs it
with `context.Background()`. Fails via `t.Fatalf` if no check with that
name was registered; fails via `t.Errorf` if the check's result does not
match `wantErr` (`true` expects a non-nil error — an induced-unhealthy or
induced-not-ready scenario; `false` expects `nil`).

### Resource ownership — the one non-generic helper

```go
type ResourceOwner interface {
	Closed() bool
}

func AssertResourceOwnership(t TB, mod modulex.Module, owner func() ResourceOwner)
```

Modulex's `modulex.Module` interface has no generic way to introspect what
a module acquired during `Init`/`Start` — there is no `Resources()` method
or similar to walk. **This is the one property in this list that requires
the module author to expose something specific**: a `Closed() bool` method
on whatever object owns the resource being verified (typically the
concrete adapter backing the module, e.g. an in-memory repository or a
database handle).

`owner` is a function rather than a plain value because a module very
commonly constructs its own adapter lazily inside `Init`, rather than the
caller already holding one to inject — the scaffolded `module.go` does
exactly this (`Init` calls `adapters.NewInMemoryRepository()` internally).
`owner` supports both styles:

- **An adapter the caller constructs and injects**: `owner` just closes
  over that pre-existing value, e.g. `func() modtest.ResourceOwner { return
  repo }`.
- **An adapter the module constructs internally**, retrieved via an
  accessor the module exposes for exactly this purpose, e.g. `func()
  modtest.ResourceOwner { return mod.Repository() }`. `owner()` naturally
  returns `nil` before `Init` runs, which `AssertResourceOwnership`
  accounts for — it skips the "before `Init`" sanity check in that case
  rather than failing.

`AssertResourceOwnership` drives `mod` through `Init`, `Start` (if `mod`
implements `modulex.Starter`), and `Stop`, calling `owner()` after each
phase, and asserts:

- If `owner()` already returns non-nil before `Init` runs, it must report
  `Closed() == false` (a sanity check on the caller's setup).
- `owner()` must return non-nil and report `Closed() == false` immediately
  after `Init`, and again after `Start` if `mod` implements
  `modulex.Starter` — the resource must stay open while the module is
  running.
- `mod` must implement `modulex.Stopper` (fails via `t.Fatalf` if not,
  since there would be no `Stop` call to verify released anything).
- `owner()` must return non-nil and report `Closed() == true` after `Stop`
  returns.

**What a module author must do for this to work:** expose a `Closed() bool`
method on the resource-owning object (the scaffolded adapters do this
already), and pass `AssertResourceOwnership` a closure that returns it —
directly if you already hold the adapter, or via an accessor method on your
module (like the generated `Module.Repository()`) if the module builds it
internally. There is no way to use this helper without that cooperation.

## Worked example

Generate a module, then run it against the harness, end to end:

```console
$ cd tools/scaffold && go build -o /tmp/modulex-scaffold ./cmd/scaffold && cd ../..
$ /tmp/modulex-scaffold -name billing -out "$(pwd)/examples"
generated /path/to/modulex/examples/billing:
  examples/billing/domain/billing.go
  examples/billing/ports/repo.go
  examples/billing/ports/service.go
  examples/billing/service/service.go
  examples/billing/service/service_test.go
  examples/billing/adapters/inmemory_repo.go
  examples/billing/module.go
  examples/billing/module_test.go
  examples/billing/README.md

$ go build ./examples/billing/...
$ go test ./examples/billing/... -v
=== RUN   TestModuleLifecycleOrder
--- PASS: TestModuleLifecycleOrder (0.00s)
=== RUN   TestModuleRollbackOnInitFailure
--- PASS: TestModuleRollbackOnInitFailure (0.00s)
=== RUN   TestModuleResourceOwnership
--- PASS: TestModuleResourceOwnership (0.00s)
=== RUN   TestModuleRespectsCancellation
--- PASS: TestModuleRespectsCancellation (0.00s)
=== RUN   TestServiceCreateAndList
--- PASS: TestServiceCreateAndList (0.00s)
PASS
```

`examples/scaffolded-sample/` in this repository is exactly this: a
generated module, committed as a concrete example, generated with:

```console
$ /tmp/modulex-scaffold -name scaffolded-sample -out "$(pwd)/examples"
```

Its `module_test.go` is the generated one shown above, unmodified — it
compiles and passes as part of this repository's own `make test` and `make
test-arch`.

## See also

- [Lifecycle Guide](./lifecycle-guide.md) — the underlying lifecycle
  contract `modtest` verifies against.
- [Error Handling](./error-handling-guide.md)
- `examples/hexagonal/incident/` — the hand-written exemplar of the layout
  `tools/scaffold` generates.

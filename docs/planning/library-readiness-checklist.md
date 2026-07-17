# Modulex Library Readiness Checklist

## Objective

Build Modulex into a small, deterministic, production-ready lifecycle orchestrator for modular Go applications,
with optional typed dependency wiring and framework integrations. The core library must remain portable and must
not claim compile-time architectural enforcement that it does not provide.

## Requirements and acceptance criteria

### Lifecycle contract

- [x] Document the valid lifecycle states and transitions:
  `configuring -> initializing -> initialized -> starting -> running -> stopping -> stopped`.
- [x] Reject concurrent or invalid lifecycle transitions with errors that support `errors.Is` or `errors.As`.
- [x] Make initialization, startup, and shutdown ordering deterministic.
- [x] Roll back successfully initialized modules in reverse order when a later initialization fails.
- [x] Stop successfully started modules in reverse order when a later startup fails.
- [x] Define and test whether `Stop` is idempotent.
- [x] Preserve all shutdown failures with module context, using `errors.Join` where appropriate.
- [x] Check context cancellation during lifecycle operations and return contextual errors.

### Registration and graph validation

- [x] Reject nil modules.
- [x] Reject empty or invalid module names.
- [x] Reject duplicate module names instead of silently replacing registrations.
- [x] Reject duplicate service keys instead of silently replacing services.
- [x] Reject unknown dependencies with a typed or sentinel error.
- [x] Detect self-dependencies and multi-module dependency cycles.
- [x] Include the useful dependency path in cycle and missing-dependency errors.
- [x] Preserve registration order, or document and implement a deterministic tie-break rule for independent modules.
- [x] Prevent module and service registration after configuration closes.

### Supervised background tasks

- [x] Replace unmanaged goroutines with lifecycle-owned, supervised tasks.
- [x] Preserve context values and OpenTelemetry trace context.
- [x] Give tasks a manager-owned cancellation context.
- [x] Cancel and await tasks during shutdown, respecting the caller's deadline.
- [x] Reject new tasks after shutdown begins.
- [x] Define how task errors affect the manager and application.
- [x] Define a configurable panic policy; never silently hide task failure.
- [x] Add race tests for task creation concurrent with shutdown.

### Core API and dependency boundaries

- [x] Keep the core package focused on graph validation and lifecycle orchestration.
- [x] Avoid mandatory Chi, NATS, Prometheus, and OpenTelemetry dependencies in the core package.
  - NATS, RabbitMQ, Watermill, Chi, and OpenTelemetry adapters are extracted into sub-packages.
- [x] Prefer small capability interfaces over one broad `Registry` interface.
  - `Registry` is now composed of `ServiceRegistry`, `EventBusProvider`,
    `ConfigProvider`, `LoggerProvider`, and `TaskSpawner`. The smaller
    interfaces are available for modules that do not need the full registry.
- [x] Make `Start` and `Stop` optional lifecycle capabilities so simple modules do not require no-op methods.
- [x] Validate constructor dependencies and return `(*Manager, error)` where construction can fail.
  - `NewManager` returns `(*Manager, error)`; construction fails with
    `ErrInvalidPanicPolicy` on an invalid `WithPanicPolicy` value. A `nil`
    event bus defaults to a no-op implementation rather than failing.
- [x] Avoid global state; inject logging, tracing, metrics, and integrations.
  - Logging is injected, the router is resolved as a typed service, and the tracer is injected via `WithTracer`.
- [x] Use typed service keys and package-level generic `Provide` and `Resolve` helpers if service location is retained.
- [x] Document constructor injection as the default and service location as an optional topology tool.
- [ ] Keep public APIs compatible once v1 is released.

### Optional integrations

- [x] Provide Chi integration in a separate package.
- [x] Provide NATS integration in a separate package.
- [x] Provide RabbitMQ integration in a separate package.
- [x] Provide Watermill integration in a separate package.
- [x] Provide OpenTelemetry integration in a separate package.
- [x] Ensure consumers compile without unused integration dependencies.
  - `examples/external-consumer` is a standalone module (its own `go.mod`,
    replacing `github.com/mediusfy/modulex` with the local checkout) that
    imports only the core package. `make check-consumer-boundary` builds it
    and fails if any adapter dependency (chi, nats, rabbitmq, watermill,
    otel) appears in its compiled dependency graph. Wired into CI as the
    `consumer-boundary` job.
- [ ] Scope integration resources to the owning module and clean them up in lifecycle order.
- [x] Inject the tracer provider rather than capturing mutable global state.
- [x] Record errors and set appropriate span status in the OpenTelemetry integration.
- [x] Add isolated integration tests for each adapter.
  - Watermill has in-memory tests; NATS uses an embedded test server; RabbitMQ uses a skip-when-unavailable test against a live broker.

### Architectural enforcement

- [x] Describe Modulex accurately as a lifecycle and composition library.
- [x] Remove claims that the runtime registry prevents imports or enforces directory structure at compile time.
- [ ] Treat static boundary enforcement as a separate optional `go/analysis` tool.
- [ ] If an analyzer is built, test allowed and forbidden import graphs with `analysistest`.

### Documentation and examples

- [x] Add a five-minute quickstart that compiles as part of CI.
- [x] Add lifecycle, rollback, shutdown, task supervision, and error-handling guides.
- [x] Add package examples rendered by pkg.go.dev.
- [x] Show both direct constructor injection and typed registry wiring.
- [x] Provide monolith and remote-adapter examples using the same domain interfaces.
- [x] Keep HTTP handlers thin and avoid raw internal error disclosure.
- [x] Use typed context keys where context values are necessary.
  - The codebase does not store values in `context.Context`, so typed context keys are not required.
- [x] Add an honest comparison with plain constructor injection, Wire, Fx, and Dig.
- [x] Add a migration guide for each breaking v0 API change.
- [x] Add `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, and a compatibility policy.

### Go compatibility and release engineering

- [x] Select the oldest Go version required by actual language features and dependencies.
- [x] Test the minimum supported Go version and current supported Go releases in CI.
- [x] Run `gofmt`, `go vet`, `golangci-lint`, `go test -race`, and build verification in CI.
- [x] Add vulnerability scanning.
- [ ] Add API compatibility checks.
- [x] Add fuzz tests for dependency graph validation.
- [x] Add failure-injection tests for the main lifecycle transition and rollback paths.
- [x] Avoid TCP listeners in unit tests where `httptest.NewRecorder` is sufficient.
- [ ] Publish v0 prereleases for API feedback before committing to v1 compatibility.
- [x] Automate tagged releases and GitHub release notes.
- [ ] Maintain `CHANGELOG.md` automatically or enforce updates in CI.
- [x] Enable OpenSSF Scorecard or an equivalent supply-chain health check.

## Coding constraints

- [x] Follow `AGENTS.md` and `CODING_STANDARDS.md` for all implementation work.
- [x] Use `log/slog` and named log-key constants for structured logging.
- [x] Use typed sentinel errors and contextual wrapping that preserves `errors.Is` behavior.
- [x] Use constructor injection and validate required dependencies.
- [x] Use `errgroup` or an equivalent structured-concurrency mechanism for supervised tasks.
  - `Manager.waitForTasks` uses `golang.org/x/sync/errgroup` to await supervised
    tasks concurrently while respecting the caller's deadline.
- [x] Add spans to public operations when tracing is enabled, without forcing tracing into the core.
- [x] Use table-driven tests and consumer-side interface segregation.
- [x] Preserve unrelated working-tree changes.

## Delivery phases

### Phase 1: Contract and core lifecycle

- [x] Finalize the lifecycle design and non-goals.
- [x] Implement state management, deterministic graph validation, registration validation, and rollback.
- [x] Add complete lifecycle and concurrency tests.

### Phase 2: Task supervision

- [x] Implement lifecycle-owned task execution, cancellation, waiting, error propagation, and panic handling.
- [x] Add race, timeout, cancellation, and shutdown tests.

### Phase 3: Typed service wiring

- [x] Implement optional typed service keys and resolution.
- [x] Add duplicate, missing, and type-safety tests.

### Phase 4: Integrations

- [ ] Extract and implement Chi, NATS, RabbitMQ, Watermill, and OpenTelemetry adapters.
  - NATS, RabbitMQ, and Watermill are extracted; Chi and OpenTelemetry remain in the core package.
- [x] Add adapter-specific tests and README files.
  - Watermill has in-memory tests; NATS has embedded-server tests and a README; RabbitMQ has skip-when-unavailable tests and a README.

### Phase 5: Adoption readiness

- [x] Rewrite claims and examples around the stable product position.
- [x] Add community, compatibility, security, and release documentation.
  - `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, `COMPATIBILITY.md`, `SUPPORT.md`, issue templates, PR templates, lifecycle/rollback/shutdown/task-supervision/error-handling guides, migration guide, comparison document, and OpenSSF Scorecard workflow are in place.
- [ ] Publish and validate v0 prereleases with external example applications.

## Required verification before completion

- [x] `make test-arch`
- [x] `make build`
- [x] `make lint`
- [x] `make test`
- [x] `go test ./... -count=1 -race`
- [x] Confirm the example application imports Modulex as an external consumer would.
  - `examples/external-consumer` is a genuinely separate module (own
    `go.mod` with a `replace` directive) that imports only the core
    package, verified by `make check-consumer-boundary` in CI.
- [x] Review the delivered work against every requirement and record deferred items explicitly.

## Deferred items summary

The following work is intentionally deferred and should be completed before a
v1 release:

1. ~~Extract remaining framework adapters from the core package and narrow the
   `Registry` interface.~~ Chi routing and OpenTelemetry tracing now live in
   `modulex/chi` and `modulex/otel` sub-packages. The core package depends only
   on capability interfaces, `NewManager` no longer accepts a concrete
   `chi.Router`, and the `Registry` interface has been narrowed.
2. ~~Inject the tracer provider.~~ The tracer is now injected via
   `modulex.WithTracer`, defaulting to a no-op tracer.
3. ~~Add Go version matrix testing.~~ CI now tests `1.26.x` and `stable` on
   Ubuntu and macOS runners.
4. ~~Add isolated adapter tests for NATS and RabbitMQ.~~ NATS uses an embedded
   server; RabbitMQ uses a skip-when-unavailable live-broker test.
5. ~~Add fuzz and failure-injection tests.~~ `FuzzGraphValidation` covers the
   dependency graph validator; failure-injection tests cover init and start
   rollback with stop errors.
6. ~~Add migration guides and detailed documentation.~~ Lifecycle, rollback,
   shutdown, task supervision, and error-handling guides are in place, along
   with a comparison document and migration guide.
7. **Publish v0 prereleases.** Tag and release v0 versions to gather API
   feedback before committing to v1 compatibility.

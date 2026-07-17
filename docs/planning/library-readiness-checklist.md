# Modulex Library Readiness Checklist

## Objective

Build Modulex into a small, deterministic, production-ready lifecycle orchestrator for modular Go applications,
with optional typed dependency wiring and framework integrations. The core library must remain portable and must
not claim compile-time architectural enforcement that it does not provide.

## Requirements and acceptance criteria

### Lifecycle contract

- [ ] Document the valid lifecycle states and transitions:
  `configuring -> initializing -> initialized -> starting -> running -> stopping -> stopped`.
- [ ] Reject concurrent or invalid lifecycle transitions with errors that support `errors.Is` or `errors.As`.
- [ ] Make initialization, startup, and shutdown ordering deterministic.
- [ ] Roll back successfully initialized modules in reverse order when a later initialization fails.
- [ ] Stop successfully started modules in reverse order when a later startup fails.
- [ ] Define and test whether `Stop` is idempotent.
- [ ] Preserve all shutdown failures with module context, using `errors.Join` where appropriate.
- [ ] Check context cancellation during lifecycle operations and return contextual errors.

### Registration and graph validation

- [ ] Reject nil modules.
- [ ] Reject empty or invalid module names.
- [ ] Reject duplicate module names instead of silently replacing registrations.
- [ ] Reject duplicate service keys instead of silently replacing services.
- [ ] Reject unknown dependencies with a typed or sentinel error.
- [ ] Detect self-dependencies and multi-module dependency cycles.
- [ ] Include the useful dependency path in cycle and missing-dependency errors.
- [ ] Preserve registration order, or document and implement a deterministic tie-break rule for independent modules.
- [ ] Prevent module and service registration after configuration closes.

### Supervised background tasks

- [ ] Replace unmanaged goroutines with lifecycle-owned, supervised tasks.
- [ ] Preserve context values and OpenTelemetry trace context.
- [ ] Give tasks a manager-owned cancellation context.
- [ ] Cancel and await tasks during shutdown, respecting the caller's deadline.
- [ ] Reject new tasks after shutdown begins.
- [ ] Define how task errors affect the manager and application.
- [ ] Define a configurable panic policy; never silently hide task failure.
- [ ] Add race tests for task creation concurrent with shutdown.

### Core API and dependency boundaries

- [ ] Keep the core package focused on graph validation and lifecycle orchestration.
- [ ] Avoid mandatory Chi, NATS, Prometheus, and OpenTelemetry dependencies in the core package.
- [ ] Prefer small capability interfaces over one broad `Registry` interface.
- [ ] Make `Start` and `Stop` optional lifecycle capabilities so simple modules do not require no-op methods.
- [ ] Validate constructor dependencies and return `(*Manager, error)` where construction can fail.
- [ ] Avoid global state; inject logging, tracing, metrics, and integrations.
- [ ] Use typed service keys and package-level generic `Provide` and `Resolve` helpers if service location is retained.
- [ ] Document constructor injection as the default and service location as an optional topology tool.
- [ ] Keep public APIs compatible once v1 is released.

### Optional integrations

- [ ] Provide Chi integration in a separate package.
- [ ] Provide NATS integration in a separate package.
- [ ] Provide OpenTelemetry integration in a separate package.
- [ ] Ensure consumers compile without unused integration dependencies.
- [ ] Scope integration resources to the owning module and clean them up in lifecycle order.
- [ ] Inject the tracer provider rather than capturing mutable global state.
- [ ] Record errors and set appropriate span status in the OpenTelemetry integration.
- [ ] Add isolated integration tests for each adapter.

### Architectural enforcement

- [ ] Describe Modulex accurately as a lifecycle and composition library.
- [ ] Remove claims that the runtime registry prevents imports or enforces directory structure at compile time.
- [ ] Treat static boundary enforcement as a separate optional `go/analysis` tool.
- [ ] If an analyzer is built, test allowed and forbidden import graphs with `analysistest`.

### Documentation and examples

- [ ] Add a five-minute quickstart that compiles as part of CI.
- [ ] Add lifecycle, rollback, shutdown, task supervision, and error-handling guides.
- [ ] Add package examples rendered by pkg.go.dev.
- [ ] Show both direct constructor injection and typed registry wiring.
- [ ] Provide monolith and remote-adapter examples using the same domain interfaces.
- [ ] Keep HTTP handlers thin and avoid raw internal error disclosure.
- [ ] Use typed context keys where context values are necessary.
- [ ] Add an honest comparison with plain constructor injection, Wire, Fx, and Dig.
- [ ] Add a migration guide for each breaking v0 API change.
- [ ] Add `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, and a compatibility policy.

### Go compatibility and release engineering

- [ ] Select the oldest Go version required by actual language features and dependencies.
- [ ] Test the minimum supported Go version and current supported Go releases in CI.
- [ ] Run `gofmt`, `go vet`, `golangci-lint`, `go test -race`, and build verification in CI.
- [ ] Add vulnerability scanning and API compatibility checks.
- [ ] Add fuzz tests for dependency graph validation.
- [ ] Add failure-injection tests for every lifecycle transition and rollback path.
- [ ] Avoid TCP listeners in unit tests where `httptest.NewRecorder` is sufficient.
- [ ] Publish v0 prereleases for API feedback before committing to v1 compatibility.
- [ ] Automate tagged releases and changelog generation.
- [ ] Enable OpenSSF Scorecard or an equivalent supply-chain health check.

## Coding constraints

- [ ] Follow `AGENTS.md` and `CODING_STANDARDS.md` for all implementation work.
- [ ] Use `log/slog` and named log-key constants for structured logging.
- [ ] Use typed sentinel errors and contextual wrapping that preserves `errors.Is` behavior.
- [ ] Use constructor injection and validate required dependencies.
- [ ] Use `errgroup` or an equivalent structured-concurrency mechanism for supervised tasks.
- [ ] Add spans to public operations when tracing is enabled, without forcing tracing into the core.
- [ ] Use table-driven tests and consumer-side interface segregation.
- [ ] Preserve unrelated working-tree changes.

## Delivery phases

### Phase 1: Contract and core lifecycle

- [ ] Finalize the lifecycle design and non-goals.
- [ ] Implement state management, deterministic graph validation, registration validation, and rollback.
- [ ] Add complete lifecycle and concurrency tests.

### Phase 2: Task supervision

- [ ] Implement lifecycle-owned task execution, cancellation, waiting, error propagation, and panic handling.
- [ ] Add race, timeout, cancellation, and shutdown tests.

### Phase 3: Typed service wiring

- [ ] Implement optional typed service keys and resolution.
- [ ] Add duplicate, missing, and type-safety tests.

### Phase 4: Integrations

- [ ] Extract and implement Chi, NATS, and OpenTelemetry adapters.
- [ ] Add adapter-specific documentation and tests.

### Phase 5: Adoption readiness

- [ ] Rewrite claims and examples around the stable product position.
- [ ] Add community, compatibility, security, and release documentation.
- [ ] Publish and validate v0 prereleases with external example applications.

## Required verification before completion

- [ ] `make test-arch`
- [ ] `make build`
- [ ] `make lint`
- [ ] `make test`
- [ ] `go test ./... -count=1 -race`
- [ ] Confirm the example application imports Modulex as an external consumer would.
- [ ] Review the delivered work against every requirement and record deferred items explicitly.


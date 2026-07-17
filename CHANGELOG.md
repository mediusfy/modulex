# Changelog

All notable changes to Modulex are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Typed service keys and generic `Provide`/`Resolve` helpers (`Key[T]`) for
  type-safe service wiring.
- `ErrServiceTypeMismatch` sentinel for typed resolution failures.
- Supervised background task execution with `TaskHandle`, `PanicPolicy`, and
  lifecycle-owned cancellation.
- `Registry.Go` now propagates OpenTelemetry trace context, recovers panics,
  returns a handle, and awaits tasks during shutdown.
- `CONTRIBUTING.md`, `SECURITY.md`, and this changelog.
- `make test-arch` target for race-detector tests.
- GitHub Actions CI workflow (`ci.yml`) running build, vet, race tests, format
  checks, `go mod tidy` verification, and `golangci-lint` on pull requests and
  `main` pushes, with the build-and-test job running on Ubuntu and macOS
  runners.
- Dependabot configuration for weekly Go module and GitHub Actions updates.
- GitHub Actions release workflow (`release.yml`) that gates releases on
  formatting, `go mod tidy`, vet, lint, vulnerability scanning, build, and
  tests before creating auto-generated releases for version tags.
- `make vuln` target and GitHub Actions vulnerability scanning job using
  `govulncheck`.
- Explicit `.golangci.yml` configuration and CI pin to `golangci-lint` v2.12.2.
- Public subpackages for framework adapters: `modulex/nats`, `modulex/rabbitmq`,
  `modulex/watermill`, `modulex/chi`, and `modulex/otel`.
- GitHub issue templates (bug report and feature request) and pull request
  template.
- README CI status badge.
- `COMPATIBILITY.md` documenting the project’s compatibility policy.
- Package-level `doc.go` for root package documentation.
- Table-driven tests for the Watermill `EventBus` adapter.
- Library readiness checklist under `docs/planning/library-readiness-checklist.md`.
- `SUPPORT.md` documenting how to get help, report bugs, and ask questions.
- `docs/planning/comparison-with-alternatives.md` comparing Modulex with plain
  constructor injection, Wire, Fx, and Dig.
- `docs/planning/migration-guide.md` documenting breaking v0 changes.
- Go version matrix in CI (`1.26.x` and `stable`) for Ubuntu and macOS runners.
- `.github/CODEOWNERS` for default review routing.
- Fuzz test for dependency graph validation (`FuzzGraphValidation`).
- Failure-injection test asserting start rollback joins stop errors.
- Isolated table-driven tests for the NATS `EventBus` adapter using an
  embedded NATS server.
- Table-driven tests for the RabbitMQ `EventBus` adapter that skip gracefully
  when no broker is available.
- Adapter-specific README files for `modulex/nats` and `modulex/rabbitmq`.
- OpenSSF Scorecard workflow and README badge for supply-chain health checks.
- README links to compatibility, comparison, migration, support, security, and
  contributing documentation.
- `docs/planning/lifecycle-guide.md` documenting lifecycle states,
  transitions, rollback, shutdown, and task supervision.
- `docs/planning/task-supervision-guide.md` documenting supervised background
  tasks, panic policies, and task error handling.
- `docs/planning/error-handling-guide.md` documenting sentinel errors,
  lifecycle errors, rollback errors, and task errors.
- Pluggable `modulex.Tracer` interface with a built-in no-op implementation.
- `WithTracer` manager option to inject a custom tracer implementation.
- `modulex/otel` package adapting an OpenTelemetry `TracerProvider` to
  `modulex.Tracer`, including span status and error recording.
- `modulex/chi` package providing typed service-key registration and resolution
  for Chi routers so the core package does not depend on Chi.
- Monolith and remote-adapter deployment examples under `examples/deployment`
  showing how the same domain interfaces can be wired locally or remotely.
- `golang.org/x/sync/errgroup` dependency for structured concurrent awaiting of
  supervised background tasks during shutdown.
- `notification.RemoteModule` example demonstrating a remote client adapter
  registered under the same typed service key as the local implementation.
- Optional `modulex.Startable` and `modulex.Stoppable` lifecycle capability
  interfaces so modules can opt into startup and shutdown hooks.
- Smaller registry capability interfaces (`ServiceRegistry`, `ServiceRegistrar`,
  `ServiceResolver`, `EventBusProvider`, `ConfigProvider`, `LoggerProvider`,
  `TaskSpawner`) so modules can depend on only the operations they need.
- Tests verifying that modules without `Startable`/`Stoppable` are skipped
  during startup, shutdown, and rollback.

### Changed

- **BREAKING:** `Registry.Go` signature changed from
  `Go(ctx, name, func(ctx context.Context))` to
  `Go(ctx, name, func(ctx context.Context) error) (*TaskHandle, error)`.
- **BREAKING:** `NewManager` no longer accepts a Chi router. Applications that
  use Chi register the router via `modulex/chi.RegisterRouter` and modules
  resolve it via `modulex/chi.ResolveRouter`.
- **BREAKING:** `Registry.Router()` and `Registry.Tracer()` are removed. The
  router is resolved as a typed service and the tracer is injected via
  `WithTracer`.
- Incident example updated to register its service via a typed key and to start
  a supervised heartbeat task.
- README expanded with typed wiring documentation, non-goals, and an honest
  comparison with plain constructor injection.
- **BREAKING:** NATS, RabbitMQ, and Watermill event-bus constructors moved from
  the root package to `modulex/nats`, `modulex/rabbitmq`, and `modulex/watermill`.
  Adapter type names are now `EventBus` and constructors are `NewEventBus` in
  each subpackage.
- **BREAKING:** The `Module` interface no longer requires `Start` and `Stop`.
  Modules that need startup/shutdown behavior now implement `modulex.Startable`
  and/or `modulex.Stoppable`.
- **BREAKING:** `NewManager` now returns `(*Manager, error)` instead of
  `*Manager`. Construction fails with `ErrInvalidPanicPolicy` if
  `WithPanicPolicy` is given a value outside the defined `PanicPolicy` enum.
  A `nil` event bus still defaults to a no-op implementation, so existing
  callers that pass `nil` for local development are unaffected beyond the
  new return value.

### Fixed

- Race in `StopModules` where errors from supervised tasks that finish early
  were not collected during shutdown.

## [0.0.0] - 2026-07-17

### Added

- Initial prototype: module lifecycle orchestration, dependency graph validation,
  service locator, and pluggable event-bus adapters (Chi, NATS, RabbitMQ,
  Watermill, OpenTelemetry).

[Unreleased]: https://github.com/mediusfy/modulex/compare/v0.0.0...HEAD
[0.0.0]: https://github.com/mediusfy/modulex/releases/tag/v0.0.0

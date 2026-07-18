# Changelog

All notable changes to Modulex are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Goroutine leak detection via `go.uber.org/goleak` in the core module and
  all adapter sub-packages (chi, nats, rabbitmq, watermill, otel).
- `make release` target to tag, push, and create GitHub releases.
- `make publish-godev` target to manually request go.dev re-indexing.
- Release workflow notifies `proxy.golang.org` and `pkg.go.dev` to
  trigger module indexing after a new version tag is pushed.
- `make publish-godev` fetches the latest version from `proxy.golang.org`
  and links to `pkg.go.dev` for manual re-indexing.
- `modboundary` analyzer: `-dbschema` and `-sqltables` flags for
  cross-module database table reference detection.
- `modulex/otel/middleware.go`: HTTP middleware (`HTTPMiddleware`) and
  subscriber middleware (`SubscriberMiddleware`) for automatic span
  creation with OpenTelemetry.

### Changed

- Marked the library readiness checklist's "publish v0 prereleases" items
  complete, referencing the [v0.1.0](https://github.com/mediusfy/modulex/releases/tag/v0.1.0)
  release.
- Marked ADR-0030 (Modulex Open-Source Release Readiness Plan) as
  Implemented.

## [0.1.0] - 2026-07-18

Initial v0 prerelease.

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
- `examples/external-consumer`, a standalone Go module (its own `go.mod`)
  that imports only the core `modulex` package, plus
  `scripts/check-consumer-boundary.sh` and a `make check-consumer-boundary`
  target that fail CI if any integration adapter dependency (chi, nats,
  rabbitmq, watermill, otel) leaks into a core-only consumer's build graph.
- `TestEventBusClosedAfterAllModulesStop` proving `StopModules` closes the
  shared `EventBus` only after every module has stopped.
- Explicit doc comments and tests on the `nats` and `rabbitmq` adapters
  clarifying that `Close` does not close the caller-owned connection/channel,
  and documentation in `docs/planning/lifecycle-guide.md` describing which
  integration resources the manager owns versus the caller.
- `tools/modboundary`, an optional `go/analysis` module providing a
  `modboundary` analyzer that flags direct imports between sibling feature
  modules (only allow-listed subpackages such as `ports` may cross a
  boundary), exempting composition roots and a module's own external test
  package. Includes `analysistest`-driven allowed/forbidden import-graph
  fixtures, `scripts/check-module-boundary.sh` (`make
  check-module-boundary`), and a `module-boundary` CI job that runs it
  against `examples/deployment`.
- `scripts/check-api-compat.sh` (`make check-api-compat`), which reports
  API compatibility since the latest git tag for the core package and each
  adapter sub-package using `golang.org/x/exp/cmd/apidiff`, wired into CI
  as the `api-compat` job.
- `scripts/check-changelog.sh` (`make check-changelog`), which fails a pull
  request that touches non-exempt files without updating `CHANGELOG.md`
  (dependency-bump-only and `.github/**`-only changes, and any
  `dependabot[bot]` PR, are exempt), wired into CI as the `changelog` job.

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

[Unreleased]: https://github.com/mediusfy/modulex/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/mediusfy/modulex/releases/tag/v0.1.0
[0.0.0]: https://github.com/mediusfy/modulex/releases/tag/v0.0.0

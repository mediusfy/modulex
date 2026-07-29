# Changelog

All notable changes to Modulex are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.2] - 2026-07-29

### Fixed

- `Manager.InitModules` and `Manager.StartModules` now share a common
  phase-runner helper, eliminating duplicated lifecycle-loop logic flagged by
  SonarCloud.
- `rabbitmq.EventBus.Subscribe` is refactored into `checkClosed`,
  `startConsumer`, `registerConsumer`, and `consumeLoop` helpers.
- `nats.EventBus.Subscribe` no longer spawns a per-subscription goroutine to
  unsubscribe on context cancellation; `Close` already unsubscribes all
  subscriptions.
- `TestRun_SetupHookRunsBeforeInit` no longer races with `StartModules` by
  cancelling the context before the module has started.

### Tests

- Added `internal/eventbustest` shared helpers (`RunPublishSubscribeTests`,
  `RunHandlerErrorLoggingTests`, `SyncBuffer`) and refactored `nats` and
  `rabbitmq` adapter tests to use them, removing duplicated test code flagged
  by SonarCloud.

### Changed

- `app.Run` stores the user-provided base context as a function rather than a
  `context.Context` value to avoid the context-in-struct anti-pattern.
- Bumped `github.com/rabbitmq/amqp091-go` from v1.12.0 to v1.13.0.
- Bumped `ossf/scorecard-action` from v2.4.3 to v2.4.4.

### CI / Security

- Replaced broad `permissions: read-all` in `.github/workflows/ci.yml` and
  `.github/workflows/scorecard.yml` with explicit, minimal permissions.
- Pinned all GitHub Actions to full commit SHAs in CI, Release, and Scorecard
  workflows to address SonarCloud supply-chain findings.
- Enforced HTTPS on redirects in the Release workflow's proxy.golang.org /
  pkg.go.dev notification `curl` calls.

### Added

- `sonar-project.properties` for SonarCloud analysis.
- `docs/reviews/28-07-code_review.md`: moved the code-review artifact out of
  the repository root.

## [0.5.1] - 2026-07-28

### Fixed

- `rabbitmq.EventBus.Close` is now idempotent; calling it more than once no
  longer attempts redundant `ch.Cancel` calls.
- `rabbitmq.EventBus.Subscribe` removes the TOCTOU window between the initial
  closed check and consumer registration.
- `nats.EventBus.Subscribe` no longer spawns a redundant goroutine per
  subscription; `Close` unsubscribes all subscriptions and cancels their
  contexts.
- `otel.NewProviderFromEnv` returns an error for a malformed
  `OTEL_TRACES_SAMPLER_ARG` instead of silently defaulting to `1.0`
  (always-sample).

### Changed

- Cleaned up redundant doc comments and clarified `EventBus` / `EventHandler`
  semantics in `modulex.go`.
- Refactored `chi/chi_test.go` error-path tests into a single table-driven
  test.
- Updated wording in `CODING_STANDARDS.md`, `Makefile`, `README.md`, and
  planning docs.
- Minor `examples/` cleanups.

### Added

- `28-07-code_review.md` at the repository root: documented the v0.5.0
  code-review findings and their resolutions.
- `docs/adr/adr-0031-modulex-value-and-specialization-roadmap.md`.
- `docs/adr/adr-0032-agent-first-development-experience.md`.
- `modulex/app` package referenced from `doc.go`.

## [0.5.0] - 2026-07-28

### Changed

- **Breaking:** `nats.NewEventBus` and `rabbitmq.NewEventBus` now accept
  optional adapter options. Existing calls must continue to compile by adding
  no options or passing the new options explicitly; callers using function
  values must update their signatures.

### Fixed

- `app.Run` now defaults a nil logger safely, and manager startup failure paths
  close the configured EventBus after rolling back modules.
- Module and service registration now coordinates lifecycle-state checks with
  the mutation lock, preventing registration races during initialization.
- `WithTypedConfig` rejects nil typed pointers instead of panicking.
- Health/readiness check names and service lookup names now use consistent
  whitespace validation and normalization.
- NATS, RabbitMQ, Watermill, and JetStream adapters reject cancelled publish or
  subscribe contexts before using the broker client; subscriptions reject nil
  handlers, and NATS subscriptions now follow context cancellation.
- `rabbitmq.EventBus.Subscribe` no longer auto-acks messages before the
  handler runs. It now acks on success and nacks without requeue (logging the
  error) on failure, so a failing handler no longer silently drops the
  message with no visibility.
- `nats.EventBus.Subscribe` now logs handler errors instead of discarding
  them silently. NATS core has no ack/nack semantics, so the message still
  cannot be redelivered, but the failure is no longer invisible.
- `watermill.EventBus.Subscribe`/`Close` no longer track per-subscription
  cancel funcs by comparing `fmt.Sprintf("%p", ...)` of `context.CancelFunc`
  values, which is unreliable since closures from the same call site can
  format identically. Cancel funcs are now tracked by a unique subscription
  ID.
- RabbitMQ EventBus shutdown now rejects new subscriptions after closing,
  waits for active consumers without racing registration, and returns the
  caller's context cancellation or deadline when handlers do not finish.

### Added

- `rabbitmq.WithLogger` and `nats.WithLogger` options to configure the
  `*slog.Logger` used to report subscribe-handler errors (defaults to
  `slog.Default()`).
- A dedicated `integration-test` CI job that runs the RabbitMQ adapter's
  integration tests against a real broker service container. Previously
  these tests always skipped in CI since no broker was reachable.
- `TestEndToEnd_FullStackLifecycle` (`e2e_test.go`): an end-to-end test
  composing `Manager`, `chi`, `httpx` (health/readiness + `Serve`), `otel`
  tracing, and the `watermill` `EventBus` through a full
  Init→Start→exercise→Stop lifecycle.
- `modulex/app`: a new package providing `Run(logger, configLoader, modules,
  opts...)`, an opinionated bootstrap helper that owns manager construction,
  module registration, signal-aware context creation, and the full
  Init→Start→wait→Stop lifecycle — removing the ~30-line skeleton every
  service entrypoint otherwise hand-writes. Configurable via
  `WithContext`, `WithSignals`, `WithShutdownTimeout`, `WithManagerOptions`,
  and `WithSetup`. See `examples/bootstrap`.
- `modulex.WithTypedConfig[T any](cfg T) ManagerOption`: removes the
  hand-written type-assert-and-copy closure every `WithConfigLoader` caller
  otherwise repeats. Returns `ErrConfigTypeMismatch` from `GetConfig` when
  called with a target that isn't `*T`.
- `otel.NewProviderFromEnv(serviceName string, opts ...ProviderOption)`: an
  opt-in helper that builds an OTLP-exporting `*sdktrace.TracerProvider` from
  standard `OTEL_EXPORTER_OTLP_*` environment variables (exporter
  protocol/endpoint, sampling ratio, resource attributes) — the generic
  OTLP-provider-construction boilerplate every OTLP-exporting service
  otherwise hand-rolls. New `go.mod` dependencies:
  `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` and
  `.../otlptracehttp`.
- `nats.JetStreamEventBus`: a new, deliberately publish-only `EventBus`
  implementation backed by NATS JetStream, for services that need
  acknowledged publishing but not JetStream consumption (which requires
  substantially more configuration than the `EventBus` interface can
  express). `Subscribe` returns `ErrJetStreamSubscribeUnsupported`.

## [0.4.2] - 2026-07-22

### Fixed

- Retracted v0.4.0 in `go.mod`. It was tagged on the wrong commit — a
  CI-permissions-only change branched off an older `main` — and published to
  the module proxy before the mistake was caught. v0.4.1 already carries the
  content that was originally intended for v0.4.0 (see that entry below); the
  proxy treats published versions as immutable, so the retraction itself
  could only take effect starting with this release, not v0.4.1.

## [0.4.1] - 2026-07-22

Republishes the content originally intended for v0.4.0 (below) under a new
version, since v0.4.0 was tagged on the wrong commit and the module proxy
would not pick up a corrected tag at the same version.

### Added

See the [0.4.0] entry below — this release's content is identical.

## [0.4.0] - 2026-07-22

**Retracted, do not use.** This version was tagged on the wrong commit and
does not actually contain the changes described below. They are published as
v0.4.1 instead.

### Added

- Readiness checks: `ReadinessRegistrar` and `ReadinessProvider` capability
  interfaces, embedded into `Registry` alongside the existing
  `HealthCheckRegistrar`/`HealthCheckProvider`. Readiness checks are a
  distinct namespace from health (liveness) checks — a failing health check
  means the process should be restarted, a failing readiness check means the
  instance should be pulled from load balancing without being restarted.
  `Manager.RegisterReadinessCheck` and `Manager.ReadinessChecks` mirror the
  existing `RegisterHealthCheck`/`HealthChecks` behavior.
- `modulex/httpx` package: `HealthHandler` and `ReadinessHandler` expose the
  registered health/readiness checks as JSON over HTTP (running checks
  concurrently, each bounded by the request deadline or a 5-second default),
  and `Serve` spawns a `*http.Server` via `modulex.TaskSpawner` with graceful
  shutdown, removing the "ListenAndServe + select + Shutdown with a timeout"
  boilerplate every HTTP-serving consumer previously hand-wrote.
- Automatic traceID/spanID propagation: `SpanContext` now carries trace and
  span IDs, and `Manager` logs parent/child span relationships via `slog`
  during `InitModules`, `StartModules`, `StopModules`, each module lifecycle
  phase, and supervised `Go` tasks.
- README documentation for the previously-undocumented
  `HealthCheckRegistrar`/`HealthCheckProvider` capability interfaces and
  `Manager.ExportDAG()` (Mermaid DAG export of the registered module graph),
  plus a new "Health Checks, Readiness, and HTTP Exposure" section covering
  `modulex/httpx`.

### Fixed

- `Manager.ExportDAG()` now renders modules and their dependencies in sorted
  order. It previously iterated the module map directly, so the generated
  Mermaid DAG's node/edge order varied from call to call, producing spurious
  diffs for consumers that regenerate and commit the DAG (e.g. a `make
  module-dag` target) even when the module topology hadn't changed.
- `StopModules`/`waitForTasks` no longer drops or double-reports supervised
  task errors depending on timing. Task errors were read from two different
  places — a per-`TaskHandle` check and a separate `m.taskErrs` slice — which
  meant a task that had already finished (and been removed from the manager's
  live task set) *before* `StopModules` was called had its error silently
  dropped whenever the overall wait then timed out, while a task that
  finished *while* `StopModules` was still waiting on it had its error
  reported twice. Task errors are now read from `m.taskErrs` exactly once,
  which is the single point every supervised task already records its result
  to before signalling completion.

## [0.3.0] - 2026-07-20

### Added

- `HealthCheckRegistrar`/`HealthCheckProvider` capability interfaces,
  embedded into `Registry`, and `Manager.RegisterHealthCheck`/`HealthChecks`
  for registering and aggregating module health checks.
- `Manager.ExportDAG()`: Mermaid-compatible DAG visualization of the
  registered module dependency graph.
- `WithEventBus` and `WithLogger` `ManagerOption`s.

### Changed

- **Breaking:** `NewManager` now takes only `...ManagerOption` instead of
  positional `(eb, logger, configLoader, opts...)` arguments; pass
  `WithEventBus`/`WithLogger` explicitly where those were previously
  positional.
- RabbitMQ and Watermill `EventBus` adapters: propagate trace context over
  AMQP headers, and fix a subscriber-cancellation bug on the Watermill
  adapter.

## [0.2.0] - 2026-07-18

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

[Unreleased]: https://github.com/mediusfy/modulex/compare/v0.4.2...HEAD
[0.4.2]: https://github.com/mediusfy/modulex/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/mediusfy/modulex/compare/v0.3.0...v0.4.1
[0.4.0]: https://github.com/mediusfy/modulex/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mediusfy/modulex/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mediusfy/modulex/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mediusfy/modulex/releases/tag/v0.1.0
[0.0.0]: https://github.com/mediusfy/modulex/releases/tag/v0.0.0

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

### Changed

- **BREAKING:** `Registry.Go` signature changed from
  `Go(ctx, name, func(ctx context.Context))` to
  `Go(ctx, name, func(ctx context.Context) error) (*TaskHandle, error)`.
- Incident example updated to register its service via a typed key and to start
  a supervised heartbeat task.
- README expanded with typed wiring documentation, non-goals, and an honest
  comparison with plain constructor injection.

## [0.0.0] - 2026-07-17

### Added

- Initial prototype: module lifecycle orchestration, dependency graph validation,
  service locator, and pluggable event-bus adapters (Chi, NATS, RabbitMQ,
  Watermill, OpenTelemetry).

[Unreleased]: https://github.com/mediusfy/modulex/compare/v0.0.0...HEAD
[0.0.0]: https://github.com/mediusfy/modulex/releases/tag/v0.0.0

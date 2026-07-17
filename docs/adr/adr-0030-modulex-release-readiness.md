# ADR-0030: Modulex Open-Source Release Readiness Plan

## Status

Proposed

## Context

Modulex is a Go lifecycle orchestration library for modular monorepos. It aims to let teams build feature modules that can run together in a single monolithic process today and be extracted into standalone services tomorrow without changing core business logic.

The current implementation is functional but still resembles a prototype. Before releasing it to hundreds of teams as an open-source library, we must address structural, API, and process gaps that would otherwise create breaking changes, support burden, and reputational risk.

Key observations from the latest review:

- The core package mixes lifecycle orchestration with framework adapters (Chi, NATS, RabbitMQ, Watermill, OpenTelemetry), forcing unrelated dependencies on every consumer.
- The lifecycle contract is implicit (`initialized bool` only) and does not roll back or stop partial successes.
- Module and service registration lacks validation (nil modules, duplicate keys, invalid names, unknown dependencies).
- Background goroutines started via `Manager.Go` are unsupervised and cannot be cancelled or awaited.
- Service wiring relies on string keys and unchecked type assertions.
- Documentation claims compile-time architectural enforcement that the code does not actually enforce.
- Test coverage is thin for failure paths, concurrency, and adapter isolation.
- Open-source hygiene documents (CONTRIBUTING, SECURITY, CHANGELOG) are missing.

## Decision

We will restructure Modulex into a small, portable core with optional integration packages, introduce an explicit lifecycle state machine, add typed service wiring, and complete the open-source release checklist before tagging any v0 prerelease.

The work is organized into five milestones.

### Milestone 1: Core Lifecycle and Registration Contract

Move the package from prototype to deterministic orchestrator.

- Define explicit lifecycle states: `configuring -> initializing -> initialized -> starting -> running -> stopping -> stopped`.
- Validate module registration: reject nil modules, empty/invalid names, duplicate names, and duplicate service keys.
- Validate the dependency graph: detect missing dependencies, self-dependencies, and cycles; report the full dependency path in errors.
- Roll back successfully initialized modules in reverse order when a later `Init` fails.
- Stop successfully started modules in reverse order when a later `Start` fails.
- Make `StopModules` idempotent and context-aware.
- Preserve all shutdown failures using `errors.Join`.
- Add comprehensive lifecycle, rollback, and concurrency tests.

### Milestone 2: Supervised Background Tasks

Replace `Manager.Go` with lifecycle-owned task execution.

- Give each task a manager-owned cancellation context.
- Collect task errors and apply a configurable panic policy.
- Cancel and await tasks during shutdown, respecting the caller's deadline.
- Reject new tasks after shutdown begins.
- Add race and cancellation tests.

### Milestone 3: Typed Service Wiring

Reduce reliance on string keys and unchecked type assertions.

- Introduce typed service keys (e.g., `Key[T]`) or generic `Provide`/`Resolve` helpers.
- Document constructor injection as the default wiring style and service location as an optional topology tool.
- Add duplicate, missing, and type-mismatch tests.
- Keep the API backward-compatible where possible during v0.

### Milestone 4: Integration Package Extraction

Split framework-specific code out of the core package.

- `modulex` — core lifecycle, graph validation, typed registry, minimal dependencies.
- `modulex/chi` — Chi router integration.
- `modulex/nats` — NATS event-bus adapter.
- `modulex/rabbitmq` — RabbitMQ event-bus adapter.
- `modulex/watermill` — Watermill event-bus adapter.
- `modulex/otel` — OpenTelemetry tracing integration.
- Ensure consumers compile without unused integration dependencies.
- Add isolated integration tests for each adapter.

### Milestone 5: Adoption Readiness

Prepare the project for external contributors and production users.

- Rewrite README and ADR text to be accurate about what Modulex enforces (runtime wiring) versus encourages (hexagonal boundaries).
- Add `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, and a compatibility policy.
- Add a quickstart example that compiles in CI.
- Add package-level examples for `pkg.go.dev`.
- Add CI matrix testing across supported Go versions.
- Add `golangci-lint`, `go mod tidy` verification, and vulnerability scanning.
- Publish v0 prereleases and validate with external example applications.

## Consequences

### Positive

- The core package becomes small and dependency-light.
- Consumers only pull in adapters they actually use.
- Lifecycle behavior is predictable, testable, and safe under partial failure.
- Service wiring becomes type-safe and easier to refactor.
- Documentation accurately sets expectations, reducing misuse and support load.
- The project looks credible and maintainable to external teams evaluating it.

### Negative

- This is a breaking restructuring. Existing single-package imports will need to change.
- The `Registry` interface will shrink or be split; callers using broad registry methods will need updates.
- Framework adapters will move to sub-packages, requiring import path changes.
- More documentation and process overhead before release.

## Milestones and Tickets

The Jira project `modulex` will track this work through the milestones and epics defined below. Each epic contains linked stories/tasks that implement the decisions above.

| Milestone | Epic | Summary |
|-----------|------|---------|
| M1 | CORE-1 | Explicit lifecycle state machine and rollback |
| M1 | CORE-2 | Registration and graph validation |
| M2 | TASK-1 | Supervised background task execution |
| M3 | WIRE-1 | Typed service keys and generic resolution |
| M4 | PKG-1 | Extract Chi, NATS, RabbitMQ, Watermill, and OTel adapters |
| M5 | DOC-1 | Rewrite documentation and examples |
| M5 | PROC-1 | Open-source hygiene and release engineering |

## Related Documents

- `docs/planning/library-readiness-checklist.md`
- `README.md`
- `CODING_STANDARDS.md`
- ADR-0029 (embedded in `README.md`)

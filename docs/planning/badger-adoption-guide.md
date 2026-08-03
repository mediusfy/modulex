# Badger adoption guide

This guide describes the safest adoption path for Badger, which currently
pins an exact Modulex v0 release. Upgrade deliberately and run the Modulex
verification commands before changing the pin.

## Adoption validation (Jira MOD-59, 2026-08-03)

Per [ADR-0031](../adr/adr-0031-modulex-value-and-specialization-roadmap.md)'s
"Adoption validation" ("Badger will be the primary adoption case... will run
the repository's required tests, race checks, build, lint, boundary checks,
and compatibility checks"), Badger's `web/backend` module (pinned to Modulex
v0.6.0) and `examples/external-consumer` (Modulex's own second consumer,
gated by `make check-consumer-boundary` in CI on every push/PR) were both
validated against the current Modulex release.

**Every check passed** — `go build ./...`, `go vet ./...`,
`go test -race -count=1` on `internal/entrypoint`, `internal/tracing`,
`internal/streams/nats`, and every `cmd/...` package that composes a
`modulex.Manager` (covering monolithic composition in `cmd/server` via
`internal/entrypoint.BootstrapModulex`, and independent-service composition
in `incident-svc`, `notifications`, `moddag`, `pr-bridge`, `crossteam-mcp`,
`runbook-mcp`, and `team-mcps/*`), `golangci-lint` on the same
Modulex-touching packages (0 issues), and `incident-svc`'s
`TestIncidentSvcComposesSharedModule` architecture test.

**What Badger has adopted**, confirmed by grepping actual import sites
rather than assuming from this guide's recommendations:

| Capability | Adopted? | Evidence |
|---|---|---|
| Core lifecycle (`NewManager`/`RegisterModule`/`Init`/`Start`) | Yes | Every `cmd/*/main.go` and `internal/entrypoint.BootstrapModulex` |
| Typed service wiring (`modulex.NewKey`/`Provide`/`Resolve`) | Yes | `internal/modules/{k8s,workspace,runbooks,siem,team}/module.go` |
| `modulex/chi`, `modulex/httpx` | Yes | HTTP composition and health/readiness across services |
| `modulex/otel` | Yes | `internal/tracing` |
| `Manager.ExportDAG` | Yes | Has its own dedicated tool, `cmd/moddag` |

**What Badger has not adopted**, and why that is worth tracking even though
none of it blocks the release pin:

| Capability | Adopted? | Finding |
|---|---|---|
| `modulex/app.Run` | No | `internal/entrypoint.BootstrapModulex` (`web/backend/internal/entrypoint/entrypoint.go`) hand-rolls exactly the `NewManager` → register → `InitModules` → `StartModules` sequence `app.Run` exists to replace, and `cmd/server`'s shutdown path is a three-step `app.Shutdown` → `StopModules` → `tracer.Shutdown` sequence. Badger's own review of this guide found that `app.Run` had no way to express that shutdown sequence — it only ever called `StopModules`, with no hook points around it — and rejected adopting it for that reason. `app.WithPreStop`/`app.WithPostStop` (added in response, see CHANGELOG `[Unreleased]`) close this gap: `WithPreStop(appShutdown)` and `WithPostStop(tracerShutdown)` now express the same three-step sequence through `app.Run`. Migrating `BootstrapModulex` to `app.Run` with those hooks remains a same-behavior refactor. |
| `modulex.WithTypedConfig[T]` | No | `BootstrapModulex` still hand-writes the type-assert-and-copy `WithConfigLoader` closure this option was built to eliminate, for exactly one immutable config value (`*config.OnlineDataConfig`) — the textbook case `WithTypedConfig` targets. |
| `modulex/nats`, `modulex/rabbitmq`, `modulex/watermill`, `nats.JetStreamEventBus` | No | Badger's messaging goes entirely through its own `pkg/broker` (`nats`, `rabbitmq`, `kafka`, `amqp1`, generic `pubsub`, and a `rest` adapter). This is not a gap to close: `pkg/broker` supports more broker technologies and a broader contract than Modulex's `EventBus` is meant to — see ADR-0031's non-goal "universal messaging abstraction." Modulex's narrower scope here is intentional and this finding confirms it, rather than motivating a migration. |
| `modulex/grpc` | No | Badger achieves service separation via independently-deployed HTTP services (one binary per `cmd/*`), not Modulex's typed local/remote `Key` swap at a shared composition root. Roadmap item 3 (gRPC/Connect for local-to-remote topology change) remains unvalidated by real Badger usage as a result — worth another look once/if a Badger service actually needs that specific swap, rather than treated as a gap today. |

**Conclusion**: Badger's adoption is real and healthy where it exists — the
five adopted capabilities above are exercised in production-shaped code
across a dozen-plus services and pass every Modulex gate. The two clearest,
lowest-risk next adoption steps are `app.Run` and `WithTypedConfig[T]` in
`internal/entrypoint.BootstrapModulex`, since both replace hand-written code
that already does exactly what those APIs do, with no behavior change. The
messaging and gRPC gaps are not adoption failures — they reflect Modulex's
deliberately narrower scope (see ADR-0031's "Non-goals") against Badger's
broader, already-built `pkg/broker` abstraction.

## Recommended adoption order

1. Replace handwritten manager bootstrap code with `modulex/app.Run`. Pass
   Badger's logger explicitly; `app.Run` also accepts a nil logger and falls
   back to `slog.Default()`.
2. Replace handwritten config-loader type assertions with
   `modulex.WithTypedConfig` where a feature has one immutable configuration
   value.
3. Use `modulex.Key`, `modulex.Provide`, and `modulex.Resolve` for feature
   ports. Keep the interface in the feature's `ports` package and register
   the concrete local or remote implementation at the composition root.
4. Expose manager health and readiness checks through `modulex/httpx` and
   use `httpx.Serve` for lifecycle-owned HTTP servers.
5. Configure OTLP tracing with `otel.NewProviderFromEnv` and pass the adapted
   tracer through `modulex.WithTracer`.
6. Generate the feature dependency graph with `Manager.ExportDAG` after
   changing module dependencies.

## JetStream boundary

`nats.JetStreamEventBus` is publish-only. It waits for the broker publish
acknowledgement, but `Subscribe` intentionally returns
`ErrJetStreamSubscribeUnsupported`. It does not model durable consumer names,
ack policies, replay, redelivery, or dead-letter handling. Badger notification
consumers must keep using a dedicated JetStream consumer implementation until
those semantics are represented by an explicit contract.

## Upgrade checklist

- Pin the exact Modulex version in Badger's `go.mod`.
- Read `CHANGELOG.md` and `docs/planning/migration-guide.md` for that release.
- Run `make test`, `make build`, `make lint`, `make test-arch`,
  `make check-consumer-boundary`, and `make check-module-boundary`.
- Regenerate Badger's module DAG and review changes in the architecture
  document.
- Verify that every feature still owns its ports and that composition roots
  are the only places selecting local versus remote implementations.
- Exercise both the Wails and headless entrypoints before merging the pin.

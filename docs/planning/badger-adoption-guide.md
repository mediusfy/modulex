# Badger adoption guide

This guide describes the safest adoption path for Badger, which currently
pins an exact Modulex v0 release. Upgrade deliberately and run the Modulex
verification commands before changing the pin.

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

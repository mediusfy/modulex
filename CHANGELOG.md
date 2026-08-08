# Changelog

All notable changes to Modulex are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `docs/blog/agent-provider-hooks.md`: an audit of which agent-facing
  surfaces ADR-0032 promised versus what's actually reachable today —
  `tools/mcpserver`'s six tools and `tools/agentcli generate` are wired
  end to end, while `approval`, `patchapply`, and `semindex` were, at the
  time of writing, fully implemented but had no CLI or MCP call site. The
  approval-package gap it identified is now closed (see the entries
  below).
- `tools/mcpserver`'s `run_verification` now consults an approval for every
  blocked (mutating/destructive/approval-required) check, adding
  `approval_status` (keyed by check name) to its output — whether a grant
  already exists, via the non-consuming `Broker.DryRunCheck`. This does not
  add an execution path: a blocked check is still never run, and nothing
  in `tools/mcpserver` can call `Broker.Grant`.
- `approval.FileStore` (`approval/store.go`): durable, file-backed grant
  storage (`Save`/`Load`, `DefaultStorePath`), bridging an approval across
  process boundaries — a grant made by one process must be visible to a
  separately-running MCP server, which has no way to share an in-memory
  `Broker` with it. `Broker` itself remains pure in-memory, unchanged.
- `tools/agentcli` gained a second `modulex agent` subcommand, `approve`
  (`agentcli.Approve`): grants an approval into `<root>/.modulex/approvals.json`
  (`approval.DefaultStorePath`) — the exact file `run_verification` reads —
  closing the loop the `approval_status` field above opened. Deliberately
  a human-run CLI command, not an MCP tool: an approval is only meaningful
  if granted outside the agent's own tool-calling loop. New
  `docs/planning/agent-cli-guide.md` documents both `generate` and
  `approve`; see also the Agent Approval Broker Guide's "Wired end to end"
  section.
- `modulex.agent.yaml` at the repository root: this repository's own
  ADR-0032 agent contract, promoted from
  `contract/testdata/modulex.agent.example.yaml` (which remains as a
  schema-test fixture, now kept in sync with the root file by a new
  `contract.TestRootContract_MatchesExample` drift guard). Previously
  `contract`'s `read_contract` and `tools/agentcli` (once it exists) had
  nothing to read for this repository itself. In the process of promoting
  it, corrected `verification.full`: it was missing `check-module-boundary`
  and `check-api-compat`, both of which are in `verify.FullGates` (the
  actual authoritative full-gate list); the example fixture had never been
  kept in sync with that list.
- `scripts/install-codegraph-hooks.sh`: installs `post-commit`,
  `post-checkout`, `post-merge`, and `post-rewrite` git hooks that run
  `codegraph sync`, per `AGENTS.md`'s "Keeping CodeGraph in sync" section —
  which referenced this script's path before the script existed.
  Idempotent (safe to re-run) and never overwrites a hook file that already
  has other content; it prints the line to add by hand in that case instead.
- `tools/agentcli`: a new nested Go module providing the `modulex` CLI
  binary (`tools/agentcli/cmd/modulex`), starting with `modulex agent
  generate`. It renders `AGENTS.md` and `CLAUDE.md` from
  `modulex.agent.yaml` via `agentdocs.Generate`, plus a static "CodeGraph"
  addendum (`tools/agentcli`'s `toolingAddendum`) covering tooling that has
  no `contract.Contract` field — CodeGraph usage and the per-agent hook
  setup (Kimi, Claude Code, Antigravity) previously hand-maintained
  directly in `AGENTS.md`. `AGENTS.md` is now generated (previously
  hand-written) and `CLAUDE.md` is new — both were regenerated once and
  checked in as part of this change. A new
  `agentcli.TestGeneratedFiles_MatchCheckedIn` test fails CI if either file
  drifts from `modulex.agent.yaml` again, operationalizing `agentdocs.Drift`
  (which existed since MOD-67 but nothing previously called against a real
  checked-in file). Added to `make check-nested-modules`.
- `provenance.VerificationProtectedPaths` and `review.CheckProtectedPaths`/
  `review.ChangedFiles`: `contract.Contract.ProtectedPaths` was, until now,
  schema and documentation only — `contract.RenderText` and
  `agentdocs.Generate` both render a contract's protected paths, but
  nothing checked a real diff against them. `review.Review` now runs
  `CheckProtectedPaths` automatically (alongside the existing secret scan)
  and `tools/mcpserver`'s `review_diff` wires it through from
  `read_contract` automatically. **Breaking change** (advisory-only per
  `COMPATIBILITY.md`'s v0 policy): `review.Review` gained a
  `protectedPaths []string` parameter. See
  `docs/planning/agent-diff-review-guide.md`'s new "Protected paths"
  section. `CheckProtectedPaths` now applies the file-scoped exceptions
  `docs/planning/agent-safety-policy.md` documents for `CHANGELOG.md`
  (adding to `## [Unreleased]` is allowed) and `go.mod` (only `retract`
  directives are protected) instead of flagging every change to either
  file — the naive whole-file match would have flagged this very changelog
  entry. `contract.Contract.Validate` now also rejects a malformed
  `protected_paths` glob pattern instead of letting it silently go
  unenforced, and `tools/mcpserver`'s `review_diff` now propagates a real
  `readContract` error (e.g. an unreadable `modulex.agent.yaml`) as a
  handler error instead of silently treating it as "no protected paths."
- New `workerpool` package (`workerpool.New`, `workerpool.Processor`):
  a small, technology-neutral bounded worker pool — fixed worker count,
  bounded waiting queue, panic recovery, and accept/complete/fail/reject
  counters via `Stats`. Implements ADR-0034's bounded-processor contract.
- `watermill.EventBus.SubscribeWithOptions` and
  `rabbitmq.EventBus.SubscribeWithOptions`: opt-in bounded concurrent
  message processing backed by `workerpool`. Default `Subscribe` behavior
  (one handler at a time, existing ack/ordering semantics) is unchanged;
  concurrency is opt-in only, per ADR-0034. RabbitMQ's prefetch is set to
  `Workers + QueueCapacity` to bound broker-side deliveries consistently
  with the pool's own capacity. Messages are acknowledged, nacked, or (for
  Watermill) nacked-for-redelivery only after the submitted handler
  completes or the pool rejects the message outright — a message is never
  left unresolved.
- Benchmarks for ADR-0034's item 1 ("Add benchmarks... before adding
  pooling or allocation optimizations"): `workerpool_bench_test.go`
  (JSON decode/encode baseline, `Processor.Submit`+`Wait` latency, and
  throughput at several worker counts), plus an adapter-level current-path
  vs `SubscribeWithOptions`-throughput comparison for Watermill and
  RabbitMQ (`watermill_bench_test.go`, `rabbitmq_bench_test.go`) and a
  current-path baseline for core NATS (`nats_bench_test.go`, which per
  ADR-0034 rule 4 does not get a `SubscribeWithOptions` — core NATS has no
  broker ack/retry semantics for a pool to sit in front of). On a local
  run, `Processor.Submit`+`Wait` overhead for a no-op task (~310ns,
  workers=1) is small relative to decoding a representative ~250-byte
  payload (~1.7µs) — pool overhead is not the bottleneck this workload's
  JSON handling is, matching ADR-0034's assumption. `ants/v2` is not added
  by this change; ADR-0034 gates that on these benchmarks justifying it,
  which is a separate follow-up decision.

- `app.WithPreStop`/`app.WithPostStop`: hooks that run immediately before
  and after `modulex.Manager.StopModules` inside `app.Run`, sharing its
  shutdown-timeout context. Addresses real feedback from Badger's `v0.7.0`
  adoption review: `cmd/server`'s shutdown is a three-step
  `app.Shutdown` → `StopModules` → `tracer.Shutdown` sequence that
  `app.Run` previously had no way to express, since it only ever called
  `StopModules` with no hook points around it. `WithPostStop` hooks run
  even if `StopModules` or an earlier hook errored, so cleanup like
  shutting down a tracer provider is never skipped; all hook and
  `StopModules` errors are joined into `Run`'s returned error.

### Fixed

- `rabbitmq.EventBus.Subscribe`/`SubscribeWithOptions` waiting on the
  internal Qos+Consume lock no longer ignores the caller's `ctx`: if
  another subscription's Qos/Consume broker round trip stalls while
  holding the lock, a concurrent `Subscribe` call now returns `ctx.Err()`
  instead of blocking forever.
- `rabbitmq.EventBus.Subscribe` (the plain, non-pooled path) no longer
  calls `Qos` on the underlying channel at all, so it can no longer reset
  a prefetch value set by code sharing that channel outside the EventBus
  (see `NewEventBus`'s doc comment on channel sharing). Only
  `SubscribeWithOptions` configures Qos now, since only it needs a bounded
  prefetch.
- `app.WithPreStop`/`app.WithPostStop` doc comments now state explicitly
  that hooks also run on the early-exit cleanup path when `InitModules` or
  `StartModules` fails, not only after a full startup — this was already
  the implemented behavior but was undocumented, so a hook written
  assuming full startup could be surprised by running against
  uninitialized state.
- `rabbitmq.EventBus.Subscribe` no longer silently inherits a prefetch
  leaked by an earlier `SubscribeWithOptions` call on the same channel.
  `Channel.Qos` with `global=false` changes the channel's default prefetch
  for every consumer created afterward, not only the one immediately
  following the call, and there is no API to read a channel's current Qos
  value back in order to restore it once a pooled subscription no longer
  needs it. A plain `Subscribe` call made after `SubscribeWithOptions` on
  the same `EventBus` now fails immediately, before touching the channel,
  instead of creating a consumer silently bound by the pool's prefetch.

### Docs

- `scripts/check-api-compat.sh` and `COMPATIBILITY.md` now track `app` and
  `workerpool` as part of the API-compatibility surface, alongside the
  existing adapter sub-packages. Both packages gained new exported API in
  this release (`app.WithPreStop`/`WithPostStop`, all of `workerpool`) but
  were missing from the compat script's hardcoded package list, so changes
  to them went unreported.
- ADR-0034's status is updated from "Proposed" to "Accepted", reflecting
  that implementation plan items 1-3 and 6 (benchmarks, the `workerpool`
  processor, and Watermill/RabbitMQ `SubscribeWithOptions` integration)
  have merged; items 4 (JetStream concurrent mode) and 5 (optional
  `ants/v2` adapter) remain open follow-up work.
- Trimmed several oversized doc comments in `review/protectedpaths.go`,
  `review/review.go`, `tools/mcpserver/review.go`, `contract/contract.go`,
  and the corresponding sections of `agent-diff-review-guide.md`/
  `agent-repository-contract-guide.md` — added during the protected-paths
  work (MOD-65), several restated the same rationale two or three times
  over. No behavior change.

## [0.7.0] - 2026-08-03

### Docs

- `docs/planning/badger-adoption-guide.md`: added an "Adoption validation"
  section (Jira MOD-59, per ADR-0031's adoption-validation roadmap item)
  documenting real Badger `web/backend` usage — build/vet/`-race`
  test/lint results (all passing) and a capability-by-capability adoption
  status table, distinguishing genuine low-risk next steps (`app.Run`,
  `WithTypedConfig[T]` are unused but would be drop-in replacements for
  existing hand-written equivalents) from intentional scope gaps (Badger's
  own broader `pkg/broker` covers messaging; Modulex deliberately does not
  aim to replace it).

### Fixed

- `watermill.EventBus.Subscribe` no longer derives its background consume
  loop's context from the caller-supplied `ctx`: a subscription is meant to
  stay active until `Close` is called, but deriving `subCtx` from `ctx`
  meant a caller passing a request-scoped or otherwise transient context
  (rather than a long-lived one) into `Subscribe` would have its
  subscription silently die whenever that context was cancelled, not only
  on `Close`. `subCtx` is now derived from `context.Background()` instead.
  `Close` also gained a `sync.Once` guard so a second call is a safe no-op
  rather than double-closing `w.pubSub`.

### Added

- New `review` package: `review.Review` runs an "agent diff review" —
  boundary (`check-consumer-boundary`, `check-module-boundary`), API
  compatibility (`check-api-compat`), and changelog (`check-changelog`)
  checks via `verify.Run`, plus a new diff-native secret scan
  (`review.ScanSecrets`) over the lines added between two refs, each
  labeled with the `provenance.VerificationCategory` that describes it
  (`VerificationBoundary`, `VerificationCompatibility`,
  `VerificationChangelog`, `VerificationSecretScan`). Implements ADR-0032
  P1, "Add diff review for boundaries, secrets, API compatibility, and
  changelog obligations" (Jira MOD-65). See
  `docs/planning/agent-diff-review-guide.md`.
  - `ScanSecrets` uses `provenance.RedactHighConfidenceSecrets` (the
    precise AWS/PEM/GitHub-token/JWT patterns) plus its own
    quote-required generic pattern, rather than `provenance`'s looser
    generic key/token/password/secret catch-all — smoke-tested against 30
    commits of this repository's own history, the looser pattern produced
    29 false positives from ordinary Go code (`token = hex.EncodeToString(buf)`,
    typed `XxxKey` identifiers, narrative comments); the stricter pattern
    drops that to 13, all legitimate (test fixtures intentionally
    exercising secret-detection code, or doc comments describing the
    patterns).
  - A `// nosecret` marker (any line containing the case-insensitive
    substring "nosecret") suppresses a finding, for a line that is
    secret-shaped on purpose.
- `provenance.RedactSecrets`: the exported form of the secret-shaped-value
  detection `provenance.Envelope.Redact` already used internally.
- `provenance.RedactHighConfidenceSecrets` and `provenance.RedactionMarker`:
  a narrower variant of `RedactSecrets` excluding the loose generic
  key/token/password/secret catch-all, for a caller (like `review.ScanSecrets`)
  scanning source code rather than free-text command output.
- New `tools/provenanceci` module (`provenanceci.BuildEnvelope`) and a
  `provenance` job in `.github/workflows/ci.yml`: every CI run now
  publishes a `provenance.Envelope` JSON artifact (`provenance-<sha>`,
  90-day retention) recording repository state and the pass/fail/skipped
  outcome of every CI job, mapped from GitHub Actions'
  `needs.<job>.result`. Runs with `if: always()` so a record is published
  even when other jobs fail or are cancelled. Implements ADR-0032 P2,
  "Publish provenance artifacts from CI" (Jira MOD-72). See
  `docs/planning/agent-provenance-ci-guide.md`.
- New `tools/mcpserver` module: a read-only MCP server (using the official
  `github.com/modelcontextprotocol/go-sdk`) exposing six tools —
  `discover_repository`, `read_contract`, `recommend_verification`,
  `run_verification`, `review_diff`, `create_handoff` — each a thin
  adapter over `discovery.Discover`, `contract.Contract`, `verify.PlanFor`/
  `Run`, `review.Review`, and `provenance.Envelope`, with no new domain
  logic. Implements ADR-0032's "MCP boundary" (Jira MOD-68). See
  `docs/planning/agent-mcp-server-guide.md`, including its safety notes on
  `run_verification` and `root`'s semantics.
  - `run_verification` classifies each check's `Command` with
    `discovery.ClassifyCommand` before running it: a command classifying as
    mutating, destructive, or approval-required is reported as
    `StatusApprovalRequired` instead of executed (mutating is blocked
    alongside the other two, not just destructive/approval-required, since
    this package's premise is that nothing in it can mutate the target
    repository). Not a new approval/auth mechanism (no grant, no token,
    nothing stateful) — it closes the gap a caller-supplied `Command` would
    otherwise leave in that guarantee, using only the already-built
    `discovery` package.
  - `root` is now a real working directory for `run_verification` and
    `review_diff`, not just a tool-detection hint: new `verify.CheckSpec.Dir`
    (empty = unchanged, process-cwd-relative behavior for every existing
    caller) and a new `dir` parameter on `review.Review`/`review.ScanSecrets`
    let `tools/mcpserver` thread the resolved `root` all the way to each
    check's `exec.Cmd.Dir` and the secret scan's `git -C <dir> diff`, so a
    caller pointing `root` at a different checkout than the server
    process's own cwd gets results computed against `root`, as documented,
    rather than silently against the server's cwd.

## [0.6.0] - 2026-07-30

### Fixed

- `patchapply.Rollback` no longer silently overwrites an existing file with
  empty content when given a `Journal` that was serialized (e.g. to JSON)
  and reloaded: `JournalEntry.OriginalContent` is deliberately excluded from
  JSON so a persisted Journal summary can never leak raw file content, but
  that made a round-tripped Journal's original bytes silently collapse to
  `nil` — indistinguishable from "this path didn't exist before." `Rollback`
  now detects this (a genuine capture always has non-nil content, even
  `[]byte{}`, for an existing file) and returns the new
  `patchapply.ErrJournalNotRestorable` instead of destroying data. `Journal`
  and `JournalEntry`'s doc comments now say explicitly that a Journal is an
  in-memory handle, not a durable/portable artifact.
- `Manager.RegisterHealthCheck`/`RegisterReadinessCheck` now reject a nil
  check function with the new `ErrHealthCheckNil`/`ErrReadinessCheckNil`
  sentinels, instead of silently storing it. A registered nil check would
  panic if any caller invoked it without `httpx`'s existing defensive nil
  guard.
- `Manager.ExportDAG` now escapes a module name containing Mermaid-
  significant characters (a literal `"`, `<`, `>`, `-->`, or `]`) instead of
  interpolating it verbatim: such a name is rendered via a synthetic node ID
  with its text safely quoted, rather than as a raw, unescaped node
  identifier and label, which could otherwise produce a malformed diagram or
  (in a permissive Mermaid rendering configuration) inject Mermaid/HTML
  syntax. Output for every already-well-formed module name (letters,
  digits, `_`, `-`) is byte-identical to before this change.
- `tools/modboundary`'s `-dbschema` glob and `scripts/check-module-
  boundary.sh`'s own existence check for migration files both previously
  matched only one directory level (Go's `filepath.Glob` and a bash glob
  without `shopt -s globstar` both treat `**` exactly like `*`), silently
  skipping a nested migrations directory. The Go analyzer now supports
  genuine recursive `**` matching (a pattern without `**` still matches
  exactly as before, for full backward compatibility); the shell script
  enables `shopt -s globstar` and its own `-dbschema` invocation uses
  `**/migrations/*.sql`.

### Added

- MOD-56: implement the fifth and final ADR-0031 roadmap item, a scaffolding
  workflow and an official reusable test harness. `tools/scaffold` is a new
  standalone Go module (own `go.mod`, following the `tools/modboundary`
  pattern) that generates the recommended
  domain/ports/service/adapters/module.go layout from a feature name using
  `text/template` and `go:embed` (stdlib only, no third-party templating
  dependency); generated modules use constructor injection as the default
  wiring (`service.New(repo)`, matching `examples/hexagonal/incident`) and
  additionally register their `Service` under a typed `modulex.Key` so typed
  service location remains an opt-in alternative, documented as such in both
  the generated `ports/service.go` and the generated `README.md`. The new
  `modtest` package (root-level, alongside `provenance`/`discovery`)
  provides composable, `testing.T`-based helpers —
  `AssertLifecycleOrder`, `AssertRollbackOnInitFailure`/
  `AssertRollbackOnStartFailure`, `AssertRespectsCancellation`/
  `AssertRespectsDeadline`, `AssertHealthCheck`/`AssertReadinessCheck`, and
  `AssertResourceOwnership` — that drive a module under test through a real
  `*modulex.Manager` to verify it against the lifecycle contract; five of
  the six are fully generic, while `AssertResourceOwnership` requires the
  module author to expose a `Closed() bool` on the resource it acquires
  (documented precisely, since Modulex has no generic way to introspect a
  module's owned resources). `modtest`'s own test suite proves each helper
  both passes a well-behaved module and detects a deliberately broken one
  (a module that ignores context cancellation/deadlines, and one whose
  `Stop` leaks its resource), using a `TB` interface and a fake recorder so
  an intentionally-failing helper call does not fail the outer test suite.
  `examples/scaffolded-sample/` is a real, committed module generated by
  the tool (regenerable via the command in its `README.md`) whose generated
  `module_test.go` exercises `modtest` end to end as part of this
  repository's own `make test`/`make test-arch`. See
  [`docs/planning/scaffolding-and-test-harness-guide.md`](docs/planning/scaffolding-and-test-harness-guide.md)
  for CLI usage, the full `modtest` helper reference, and a worked example.
- MOD-57: document the durable-consumer technology choice and semantic
  contract in [ADR-0033](docs/adr/adr-0033-durable-consumer-jetstream.md)
  (NATS JetStream over Kafka/Redpanda — zero new dependency, already-used
  infrastructure, direct semantic fit for ack/retry/replay/consumer-identity/
  dead-letter), and add
  [`docs/planning/durable-consumer-operations-guide.md`](docs/planning/durable-consumer-operations-guide.md)
  covering production configuration (ack-wait/max-deliver/batch-size/
  fetch-wait tuning tradeoffs) and operational failure modes (broker
  unreachable, ack-wait exceeded under load, max-deliver exhaustion and why
  dead-lettering is a handler decision rather than automatic, handler
  panics, shutdown mid-processing, and monitoring recommendations). No code
  changes: MOD-54 already delivered the `nats.JetStreamEventBus`
  `DurableConsumer` implementation and its integration test suite (success,
  nack/redelivery, panic recovery, dead-letter, consumer-identity
  resumption, replay policy, cancellation, and shutdown), which this ticket
  formalizes and documents rather than re-implementing.
- MOD-55: add an optional `grpc` package implementing the third ADR-0031
  roadmap item, gRPC-only (Connect is out of scope — see the package doc
  comment and `docs/planning/grpc-adapter-guide.md` for the scoping
  rationale). `grpc.Server` adapts a `*grpc.Server` into
  `modulex.Starter`/`modulex.Stopper` for Manager-owned lifecycle, with a
  bounded graceful shutdown (`GracefulStop`, falling back to a hard `Stop` if
  it doesn't complete before a configurable timeout). Trace context
  propagates over gRPC metadata via `TraceUnaryClientInterceptor`/
  `TraceUnaryServerInterceptor` and streaming counterparts, using the same
  `otel.GetTextMapPropagator()` mechanism the `nats`/`rabbitmq` adapters use
  for message headers. `UnaryServerErrorInterceptor` and `TranslateError`
  provide consistent, two-way domain-error-to-`codes.Code` mapping.
  `HealthServer` implements `grpc_health_v1.HealthServer` by evaluating a
  `modulex.Manager`'s actual registered health/readiness checks on every
  call, rather than reporting a hardcoded status. `google.golang.org/grpc`
  and `google.golang.org/protobuf` move from indirect to direct dependencies
  of this module (both were already pulled in transitively by the OTLP gRPC
  trace exporter); the core `modulex`/`wire` packages remain free of any
  gRPC/protobuf import, verified by `make check-consumer-boundary`. The
  `examples/deployment/notification` example gains a gRPC-based sibling to
  its existing HTTP remote example, binding the same `ports.Sender`/
  `ports.ServiceKey` to a local implementation in one process and a remote
  gRPC client in another. See
  [`docs/planning/grpc-adapter-guide.md`](docs/planning/grpc-adapter-guide.md)
  for the full design, the error-mapping table, and a walkthrough of the
  example.

- MOD-54: split `EventBus`'s bundled publish/subscribe surface into explicit,
  additive messaging capability interfaces: `Publisher` (`Publish`),
  `Subscriber` (`Subscribe`), and `DurableConsumer` (`SubscribeDurable`).
  `EventBus` itself, and every existing adapter's exported API
  (`nats.EventBus`, `nats.JetStreamEventBus`, `rabbitmq.EventBus`,
  `watermill.EventBus`), are unchanged — any type implementing `EventBus`
  already satisfies `Publisher` and `Subscriber` for free, since Go
  interfaces are structural. `DurableConsumer` is new: it documents
  acknowledgement, retry, replay, ordering, consumer identity, and
  dead-letter semantics explicitly, using a `DurableHandler` that returns an
  `AckDecision` (`Ack`/`Nack`/`DeadLetter`) instead of `EventHandler`'s bare
  `error`, so a caller can tell from the type system (and check via type
  assertion) whether an adapter actually provides durable delivery.
  `nats.JetStreamEventBus` now implements `DurableConsumer` using a real
  JetStream pull-based durable consumer (explicit ack/nack/term, configurable
  max-deliver/ack-wait/replay policy, and dead-letter republish); its
  existing `Publish`/`Subscribe`/`Close`/`NewJetStreamEventBus` signatures
  are unchanged. A panicking `DurableHandler` invocation is recovered and
  treated as `Nack` (logged, then redelivered) rather than crashing the
  process hosting the durable consume loop. See
  [`docs/planning/eventbus-capabilities-guide.md`](docs/planning/eventbus-capabilities-guide.md)
  for the full design and a migration note (existing `EventBus` usage needs
  no changes).

### Fixed

- `Manager.StopModules` (MOD-58) now rejects a concurrent call that lands
  while `InitModules` or `StartModules` is still running on another
  goroutine (`StateInitializing`/`StateStarting`), returning
  `ErrInvalidLifecycleState`. Previously it would race its own shutdown
  (task cancellation, event bus close) against the in-flight phase; once
  that phase completed it would silently overwrite `StateStopped` with
  `StateInitialized`/`StateRunning`, breaking the documented
  `StopModules` idempotency guarantee. Cancel the context passed to the
  in-flight `InitModules`/`StartModules` call instead, then call
  `StopModules` once it returns.
- `app.Run` now stops already-initialized modules if `InitModules` or
  `StartModules` fails partway through, rejects `nil` modules with an error
  naming the offending index, and includes the module's name when
  `RegisterModule` fails.
- `httpx.HealthHandler` and `httpx.ReadinessHandler` respond 503 with a
  descriptive error instead of panicking when given a `nil` provider;
  `httpx.Serve` similarly rejects a `nil` spawner. `runChecks` recovers from a
  panicking check (reporting it as failed) and always bounds each check with
  `defaultCheckTimeout`, deriving from the caller's context so an existing
  shorter deadline is still respected.
- `otel.Tracer`'s `SpanContext` methods (`IsValid`, `TraceID`, `SpanID`) and
  `ContextWithSpanContext` are now nil-safe.
- `otel.HTTPMiddleware`'s response-status wrapper no longer double-writes the
  status header and now forwards `http.Flusher`/`http.Hijacker` to the
  underlying `ResponseWriter`, so SSE and WebSocket handlers work correctly
  behind the middleware.
- `watermill.EventBus.Close` now waits for in-flight subscriber goroutines to
  exit (bounded by the caller's context) before returning, instead of
  returning immediately after requesting cancellation.

### Added

- New `discovery` package (MOD-64): a standalone, dependency-free (besides
  `provenance`) leaf package implementing step 1 of ADR-0032's "Standard
  agent workflow" — `discovery.Discover(root)` scans a repository for Go
  modules (correctly stopping at nested module boundaries, mirroring `go
  list ./...`), composition roots (`examples/` children plus any directory
  with a `func main()`), well-known agent-instruction files, `Makefile`
  targets, CI workflow files, semantic-index presence (`.codegraph`,
  `.git`, `.tokensave`), available tools on `PATH`, and git dirty-worktree
  state — all without depending on global/user-scoped state and without
  ever executing a discovered binary. `discovery.ClassifyCommand` reuses
  `provenance.CommandClass` to classify a command string against an
  extensible, first-match-wins rule table built from
  `docs/planning/agent-safety-policy.md`, with an explicit
  approval-required fail-safe default for unrecognized commands and a
  documented tie-break for commands (like `make publish-godev`) that are
  both networked and externally visible. See
  `docs/planning/agent-discovery-guide.md`.
- `Manager.ModuleContract` (MOD-60): returns a machine-readable
  `ModuleContract` (`ModuleContractEntry` per module) describing registered
  modules and their dependency edges, independent of the existing Mermaid
  `ExportDAG`. Modules and each module's `DependsOn` list are sorted
  alphabetically so the JSON output is byte-identical across calls, making it
  safe to diff between releases or deployments.
- `Manager.Diagnostics` (MOD-60): returns a `Diagnostics` snapshot covering
  lifecycle state, the module contract, registered service names (never
  values), supervised task status (`TaskDiagnostic`), health/readiness check
  names, and lifecycle timings (`LifecycleTimings`, `ModuleTiming`). The
  result is safe to log or attach to a support ticket since it never exposes
  service values, check function bodies, or task closures. `TaskHandle` gained
  non-blocking `Done` and `Err` accessors to support this. See
  `docs/planning/diagnostics-guide.md`.
- New `provenance` package (MOD-66): a standalone, dependency-free schema
  (`Envelope`, versioned via `SchemaVersion`) for recording agent
  provenance/handoff data — repository state, agent identity, changed files,
  command results, verification results, approvals, and rollback status.
  `Status` distinguishes pass/fail/skipped/unavailable/approval-required
  rather than a bare bool; `Envelope.Redact` scrubs common secret-shaped
  patterns from free-text fields and `Envelope.Validate` rejects both
  missing required fields and any unredacted secret-shaped value left
  behind. See `docs/planning/provenance-handoff-schema.md` and
  `provenance/testdata/sample-handoff.json` for a full example.
- `otel.WithInsecure` provider option and `OTEL_EXPORTER_OTLP_INSECURE`
  environment variable to explicitly force TLS on/off for the OTLP exporter.
  Endpoints are now scheme-sanitized, and loopback hosts default to insecure
  when unspecified.
- `docs/planning/agent-safety-policy.md` (MOD-61): documents safe defaults,
  command classification, protected paths, secret-handling rules, and the
  human-approval boundary for AI coding agents (Claude, Kimi, OpenAI/Codex,
  generic MCP clients, no-hook environments) working in this repository.
  Linked from `AGENTS.md`.
- New `verify` package (MOD-63): a standalone leaf package (depending only on
  `provenance` and `discovery`) implementing step 5 of ADR-0032's "Standard
  agent workflow" — `verify.PlanFor(changedFiles)` maps changed paths to
  focused checks (per-package `go test`/`go vet`, example builds, changed
  scripts, `go.mod`/`go.sum` compatibility checks, etc.) via a first-match
  rule table, falling back to the full gate set rather than recommending
  nothing for an unmapped path; `verify.FullGates` is the fixed,
  unconditional list of this repository's required gates (`make build`,
  `test`, `test-arch`, `lint`, `check-consumer-boundary`,
  `check-module-boundary`, `check-api-compat`, `check-changelog`), always
  present in a `Plan` regardless of what changed. `verify.Run` executes a
  `[]CheckSpec` against a `discovery.Repository`'s `Tools` and an explicit
  `allowNetwork` flag, producing exactly one `provenance.VerificationResult`
  per input check — a missing required tool reports `StatusUnavailable`
  (without ever invoking the command) and a networked check without
  `allowNetwork` reports `StatusSkipped`, both with a `Reason`, so a missing
  tool or skipped check is never confused with success.
  `verify.RenderText` renders results as a human-readable summary grouped by
  category. Since `changedFiles` may come from an untrusted diff,
  `PlanFor` never builds a `CheckSpec.Command` from a path containing shell
  metacharacters or a `..` traversal segment — such a path falls back to the
  full gate set instead, preventing shell injection into the commands
  `Run` later executes via `sh -c`. See
  `docs/planning/agent-verification-guide.md`.
- New `contract` package (MOD-62): a standalone leaf package (depending only
  on `provenance` and `gopkg.in/yaml.v3`) defining `Contract`, a versioned,
  YAML-marshalable schema for a repository's declared agent contract
  (`modulex.agent.yaml`) per ADR-0032's "Canonical repository contract" —
  projects/Go modules/composition roots, instruction-file precedence,
  lifecycle/module boundaries, classified commands (reusing
  `provenance.CommandClass`), focused/full verification checks (shaped to
  convert to/from `verify.CheckSpec`), protected/generated paths, required
  tools, optional services, required credential *names* (never values), and
  a named handoff format. `Contract.Validate` returns every problem found via
  `errors.Join`: missing required fields, an unknown `CommandDecl.Class`
  value (explicitly checked, since YAML happily unmarshals any string into
  the enum), and any free-text field matching a secret-shaped pattern
  (mirroring `provenance`'s detection) — unlike `provenance.Envelope`, this
  package has no redaction step, since a checked-in contract file is meant
  to be hand-edited, so a live-looking credential fails validation outright
  rather than being silently rewritten. `RenderText` produces a
  human-readable summary. `contract/testdata/modulex.agent.example.yaml`
  is a complete, valid example describing this repository itself, loaded
  and validated by its own regression test so the schema and the example
  can never drift apart. See
  `docs/planning/agent-repository-contract-guide.md`.
- New `agentdocs` package (MOD-67): a standalone leaf package (depending
  only on `contract`) implementing ADR-0032's "Portability" section —
  `agentdocs.Generate(contract.Contract, target)` renders one contract into
  provider-specific agent instruction documents for four targets
  (`TargetAGENTS`, `TargetCLAUDE`, `TargetKimi`, `TargetCodex`), each
  identifying its source contract and schema version, and each carrying a
  command matrix (sorted by class then name), focused/full verification
  guidance, a standalone safety-policy summary (protected paths, required
  credential names, and the approval-boundary rules from
  `docs/planning/agent-safety-policy.md`), and handoff guidance when
  `Contract.HandoffFormat` is set. Every contract slice is copied and
  sorted before rendering so `Generate` is byte-identical across repeated
  calls for the same input and never mutates the caller's `Contract`.
  `agentdocs.Drift(contract.Contract, target, existingContent)` reports
  whether a checked-in file (e.g. `AGENTS.md`) is stale relative to the
  contract, without reading or writing any file itself. See
  `docs/planning/agent-instruction-generation-guide.md`.
- New `semindex` package (MOD-71): a standalone, dependency-free leaf
  package implementing P2 of ADR-0032's roadmap — "Add CodeGraph/TokenSave
  index-root validation and diagnostics." `semindex.Diagnose(worktreeRoot,
  indexDir, name, reader)` compares a semantic index's declared root
  (read via a caller-supplied `RootReader`, or the package's own
  `DefaultMarkerReader` for indexes adopting the new `.modulex-index-root`
  marker-file convention) against the active worktree root, resolving
  symlinks on both sides before comparing so platform differences (e.g.
  `/tmp` vs. `/private/tmp` on macOS) never produce a false-positive
  mismatch. Results are a four-state `Status`
  (`StatusOK`/`StatusMismatch`/`StatusMissing`/`StatusUnverifiable`) rather
  than a boolean, so "the root couldn't be determined" is never confused
  with "it matches." `semindex.ResolveWorktreeRoot` prefers `git
  rev-parse --show-toplevel` with a caller-supplied fallback.
  `semindex.EvaluateSeverity(diagnosis, policy)` turns a non-OK diagnosis
  into a warn/block decision from a simple `Policy` struct, without
  requiring a `contract.Contract`. This package cannot inspect real
  CodeGraph/TokenSave index formats directly (undocumented, tool-owned,
  and out of scope for Modulex's dependency graph) — it only validates
  indexes that adopt its marker-file convention or that a caller supplies
  a custom `RootReader` for. See
  `docs/planning/semantic-index-diagnostics-guide.md`.
- New `approval` package (MOD-69): a standalone, stdlib-only leaf package
  implementing P2 of ADR-0032's roadmap — "Add an approval broker for
  elevated agent tools." `approval.Broker` is a thread-safe, in-memory,
  live decision mechanism for whether an elevated action (push, release,
  deletion, infrastructure change, database migration, Jira/PR mutation)
  is currently authorized — distinct from `provenance.Approval`, which is
  only a static audit record with no scope or expiry. `NewBroker` starts
  with zero grants, so nothing is approved by default; `Broker.Grant`
  issues a scoped, unguessable (`crypto/rand`-generated), expiring,
  single-use `Grant` for an exact `Scope{Action, Resource}` pair, rejecting
  a non-positive TTL, empty action, or empty approver outright.
  `Broker.Check`/`Broker.CheckToken` fail closed on every path except an
  exact, unexpired, unused scope/token match — an unknown scope, an
  expired grant, an already-consumed grant, or a mismatched action or
  resource are all denied identically, and a matched single-use grant is
  consumed atomically under the broker's lock so two concurrent callers
  can never both consume the same grant. `Broker.DryRunCheck`/
  `DryRunCheckToken` preview a decision without consuming a grant.
  `Grant.Token` is treated as a bearer credential: it is excluded from
  JSON encoding and from `Grant.String()`/default `%v`/`%+v` formatting in
  favor of `TokenHash` (a SHA-256 digest), and `Grant.ToProvenanceApproval`
  converts a grant to a `provenance.Approval` for handoff-artifact
  continuity. `RequiresApproval(contract.Contract, commandName)` derives
  approval policy from a contract's existing `CommandDecl.Class` (true for
  `ClassApprovalRequired`/`ClassDestructive`) without any new field on
  `contract.Contract`, returning an error for an unrecognized command that
  callers must treat as fail-closed. This package has no persistence layer
  (a process restart invalidates all grants, by design) and is not yet
  wired into any CLI or MCP server. See
  `docs/planning/agent-approval-broker-guide.md`.
- New `patchapply` package (MOD-70): a standalone leaf package (depending
  only on `approval` and `provenance`, stdlib otherwise) implementing P2 of
  ADR-0032's roadmap — "Add atomic patch application and rollback
  journaling." `patchapply.Apply(targetDir, []FileChange, ApplyOptions)`
  applies a batch of content-based file mutations (write new content to a
  path, or delete a path — not unified-diff/patch-file parsing) as a single
  all-or-nothing transaction: every path is validated to stay within
  `targetDir` (rejecting an absolute path, a `..` traversal, or a path that
  resolves outside `targetDir` via a symlink planted anywhere in its
  existing directory chain), any batch containing a `Delete: true` entry is
  refused outright unless an approved `approval.Broker`/`Scope` is
  supplied, and every file's current on-disk content is checked against an
  optional `FileChange.ExpectedPriorContent` — all before a single byte is
  written — so a batch that would clobber an unrelated dirty-worktree edit
  fails closed instead of silently overwriting it. Writes use a
  temp-file-then-`os.Rename` pattern in the target file's own directory for
  per-file atomicity; if any individual write/delete in the batch fails
  partway through, `Apply` immediately rolls back everything already
  applied earlier in the same call using the returned `Journal`, so a
  caller only ever observes full success or a full revert, never a
  partially-applied batch. `patchapply.Rollback(targetDir, Journal)` undoes
  a previously successful `Apply` call standalone, and
  `patchapply.Verify(targetDir, Journal)` re-reads every touched file to
  confirm the directory exactly matches its pre-`Apply` state, naming any
  drifted file. `Journal.String()` never includes file content, and the
  few error paths that do preview content (an `ExpectedPriorContent`
  mismatch, a `Verify` drift report) redact secret-shaped values first,
  using a locally copied subset of `provenance`'s secret-pattern detection.
  See `docs/planning/agent-atomic-patch-guide.md`.

### Changed

- SonarCloud code-smell cleanup (MOD-52): renamed single-method capability
  interfaces to follow Go's `-er` naming convention (`Startable` ->
  `Starter`, `Stoppable` -> `Stopper`, `ServiceRegistrar` ->
  `ServiceRegisterer`, `HealthCheckRegistrar` -> `HealthCheckRegisterer`,
  `ReadinessRegistrar` -> `ReadinessRegisterer`); no method signatures
  changed. See the migration guide for details. Also addressed duplicate
  string literals, grouped same-type parameters, added missing doc comments,
  and tightened shell script conditionals; no behavior changes.

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

- `docs/reviews/28-07-code_review.md`: documented the v0.5.0
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

[Unreleased]: https://github.com/mediusfy/modulex/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/mediusfy/modulex/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/mediusfy/modulex/compare/v0.5.2...v0.6.0
[0.5.2]: https://github.com/mediusfy/modulex/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/mediusfy/modulex/compare/v0.4.2...v0.5.1
[0.4.2]: https://github.com/mediusfy/modulex/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/mediusfy/modulex/compare/v0.3.0...v0.4.1
[0.4.0]: https://github.com/mediusfy/modulex/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/mediusfy/modulex/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/mediusfy/modulex/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/mediusfy/modulex/releases/tag/v0.1.0
[0.0.0]: https://github.com/mediusfy/modulex/releases/tag/v0.0.0

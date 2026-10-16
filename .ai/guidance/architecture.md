# Architecture

Short map for agents. The README explains the design, and `docs/adr/` (ADR-0030 to 0035) and `docs/planning/*-guide.md` hold the decisions and per-feature guides.

## What the library is

Modulex is a runtime lifecycle orchestrator and service registry for modular Go applications. Modules declare dependencies; the `Manager` orders them topologically, runs `Init` and `Start` in order and `Stop` in reverse, and lets the composition root (`main.go`) register either an in-process implementation or a network client under the same interface. It does not enforce import boundaries at compile time; the boundary checks below do that.

## Core (`github.com/mediusfy/modulex`, root package)

- `modulex.go`: `Module`, `Manager`, registration, dependency graph and cycle detection, lifecycle, supervised tasks, health and readiness, sentinel errors.
- `wire.go`: typed service wiring. `typedconfig.go`: typed configuration. `diagnostics.go`: module graph and diagnostics output.
- The core imports no adapter package (`make check-consumer-boundary`).

## Optional packages in the root module

| Area | Packages |
|---|---|
| Application bootstrap | `app` (owns process run, signals and shutdown) |
| Messaging (`EventBus` adapters) | `nats` (plus JetStream durable consumer), `rabbitmq`, `watermill`; `mocks/eventbus.go` is a test double |
| Transport | `chi` (router integration), `httpx` (HTTP endpoints, health and readiness exposure), `grpc` (gRPC server adapter, health, tracing) |
| Observability | `otel` (OTLP provider from environment, middleware) |
| Concurrency | `workerpool` (bounded, lifecycle-aware work), `internal/busshutdown`, `internal/handlerpanic` |
| Test helpers | `modtest`, `internal/eventbustest`, `internal/gittest`, `mocks/` |
| Agent support | `contract` (schema for `modulex.agent.yaml`), `agentdocs` (renders `AGENTS.md`/`CLAUDE.md`), `discovery`, `verify`, `review` (diff review, secret scan, protected paths), `patchapply` (atomic, content-based file edits), `approval` (human-granted approvals and broker), `provenance` (handoff envelope schema), `agentreview` (review-and-handoff orchestration), `semindex` (semantic index diagnostics) |

## Nested modules

Each has its own `go.mod`; those that import the root module use `replace github.com/mediusfy/modulex => ../..`, so they always build against the checkout:

- `tools/agentcli`: the `modulex` CLI (`new`, `check`, `doctor`, `agent generate|approve|review|handoff|verify`). It is the single enforcement path the agent hooks and the `PR Review` workflow call.
- `tools/mcpserver`: a read-only MCP server (`discover_repository`, `read_contract`, `recommend_verification`, `run_verification`, `review_diff`, `create_handoff`), used by the editor plugins and agent configs (`.mcp.json`, `opencode.json`).
- `tools/modboundary` (go/analysis analyzer for feature-module boundaries), `tools/provenanceci` (builds the CI provenance envelope), `tools/scaffold` (generates a feature module from `templates/`).
- `examples/external-consumer`: proves the module is usable from outside.
- `services/prreview`: the hosted PR-review GitHub App: webhook receiver and worker on a serverless container platform, with a queue, a document store and a secret store behind small interfaces that have in-memory implementations. Its infrastructure is Terraform in `infra/prreview/`.

## Examples

`examples/` holds composition roots (`bootstrap`, `deployment` with monolith and remote topologies, `hexagonal`, `quickstart`, `scaffolded-sample`). `examples/deployment` is what `make check-module-boundary` analyses.

## Agent tooling around the repo

- `modulex.agent.yaml` is the machine-readable contract: projects, boundaries, command classes, verification gates, protected paths, required credential names. `AGENTS.md` and `CLAUDE.md` are generated from it.
- `scripts/agenthooks/` (`pre`, `guard`, `while`, `post`) are wired for Claude Code (`.claude/settings.json`), opencode (`.opencode/plugin/`) and Antigravity (`.antigravity/hooks/`). They call the `modulex` CLI.
- `editors/vscode` and `editors/intellij` are clients of `tools/mcpserver`; `editors/tool-surface.json` is the shared tool list.

## CI and release

- `ci.yml` runs on PRs and pushes to `main`; its last job publishes a provenance envelope of every job's result. `pr-review.yml` runs the deterministic review engine on PRs. `release.yml` runs on tags and repeats the gates before creating a GitHub release. `scorecard.yml` runs OpenSSF Scorecard weekly, on pushes to `main` and on branch protection changes.
- `make release VERSION=vX.Y.Z` runs a local preflight and then tags and pushes. A tag published to the Go module proxy is permanent (see the `retract v0.4.0` note in `go.mod`), so releasing always needs a human.

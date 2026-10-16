# Testing

The generated `AGENTS.md` lists the full gates (from `modulex.agent.yaml`): `make build`, `make lint`, `make test`, `make test-arch`, `make check-consumer-boundary`, `make check-module-boundary`, `make check-api-compat`, `make check-changelog`. This file says what each level covers and what "done" means.

## Go tests

- `make test` runs `go test -v ./...`; `make test-arch` runs `go test -race ./... -count=1`. CI runs `go test -v -race -count=1 -coverprofile=coverage.out ./...` on Linux and macOS with Go `1.26.x` and `stable`, after `go mod tidy -diff`, a `gofmt -s -l .` check, `go build` and `go vet`.
- Tests sit next to the code (`*_test.go`). Use table-driven tests; `modtest` has reusable checks for module lifecycle, health, cancellation, resource cleanup and rollback, and `internal/eventbustest` has shared EventBus round-trip helpers.
- `e2e_test.go`, `example_test.go` and `examples/deployment/integration_test.go` exercise whole applications in-process. Example functions are documentation too; keep their `// Output:` blocks correct.
- RabbitMQ tests skip when no broker is reachable. CI's `integration-test` job runs `go test -race ./rabbitmq/...` against a RabbitMQ service container (`RABBITMQ_URL`).
- Nested modules have their own tests: `make check-nested-modules` runs `go mod tidy -diff`, `go build` and `go test` in each `tools/*` module, `examples/external-consumer` and `services/prreview`. `services/prreview` tests run against in-memory implementations of its store and queue, without cloud services.
- Mocks in `mocks/` are mockery-generated; regenerate rather than edit.

## Static and boundary checks

- `make lint` (golangci-lint), `make vuln` (govulncheck), `make check-consumer-boundary`, `make check-module-boundary` (the `tools/modboundary` analyzer; its own tests use `testdata/src/allowed*` and `forbidden*`), `make check-api-compat` (report only), `make check-changelog`.

## Editor plugins

- `editors/vscode`: `npm ci && npm test`. `editors/intellij`: `./gradlew --no-daemon test`. Both run in CI's `editor-plugins` job. The real-server end-to-end tests (`realserver.e2e.test.ts`, `RealServerE2ETest.kt`) talk to the MCP server built from `tools/mcpserver`.

## PR review engine

- The `PR Review` workflow builds `tools/agentcli` and runs `modulex agent handoff` on the PR diff: secret scan, protected-path check (a warning, not a failure) and the contract's declared gates. A failure other than protected paths fails the job. Run the same locally with `go build -C tools/agentcli -o /tmp/modulex ./cmd/modulex` and `/tmp/modulex agent handoff -base origin/main -head HEAD`.

## Merge and verification criteria

A PR is ready for review or merge only when every step below passes. If one doesn't apply, the PR says so and why.

1. **Green CI.** On the PR's latest commit, every `CI` job (`Build & Test` matrix, `Integration Tests (RabbitMQ)`, `Lint`, `Vulnerability Scan`, `Consumer Dependency Boundary`, `Module Boundary Analyzer`, `API Compatibility Report`, `CHANGELOG.md Check`, `Nested Go Modules`, `Editor Plugins`) and `PR Review` pass. A red job that isn't caused by this PR is reported on the PR, never ignored.
2. **Sonar addressed.** SonarCloud automatic analysis runs on every PR (`.sonarcloud.properties`). Its quality gate passes and the PR adds no new blocker, critical or major issues; fix lower-severity issues too, or say on the PR why one stays.
3. **Tests pass at every level that applies:**
   - **Unit:** `make build`, `make lint`, `make test` and `make test-arch` locally (CONTRIBUTING requires all four).
   - **Integration:** the RabbitMQ job for adapter changes (or a local broker with `RABBITMQ_URL`); `make check-nested-modules` for changes to the root API, `tools/`, `services/prreview` or `examples/external-consumer`.
   - **End to end:** `e2e_test.go` and the `examples/` integration tests (part of `make test`); the editor plugin tests for changes to `tools/mcpserver` or `editors/`.
   - **Smoke:** for CLI or MCP server changes, build the binary and run one real command (for example `modulex agent handoff` on the branch, or start `tools/mcpserver` and list its tools). Report the result in the PR.
   - Report skipped or unavailable checks as skipped, never as passing (agent safety policy).
4. **Coverage and gaps.**
   - Run `go test -race -count=1 -coverprofile=coverage.out ./...` and `go tool cover -func=coverage.out` (or `-html`) and check that the lines and branches the PR adds or changes are covered. No coverage floor is enforced.
   - Every new or changed branch needs a test, including error paths, context cancellation, shutdown ordering, empty and boundary inputs, and for agent-facing packages hostile input (path traversal, secret-shaped strings, malformed contracts).
   - Write down any gap you leave and why.

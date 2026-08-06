<!-- GENERATED FROM modulex.agent.yaml (schema v1.0.0) — DO NOT EDIT BY HAND. Regenerate via agentdocs.Generate; see docs/planning/agent-instruction-generation-guide.md. -->

# CLAUDE.md — Claude Code Instructions

This file is Claude-specific guidance for Claude Code sessions in this repository. It covers the same contract as AGENTS.md; where a Claude-specific hook or setting is available it may supplement this file, but nothing below requires one to be present.

Generated from `modulex.agent.yaml` (schema v1.0.0). See `docs/planning/agent-repository-contract-guide.md` for the contract's full schema and `docs/planning/agent-instruction-generation-guide.md` for how this file is generated and how to detect drift between it and the checked-in contract. Do not hand-edit below this line — regenerate instead.

## Projects

- **modulex** (`.`) — module `github.com/mediusfy/modulex`
  Core lifecycle/dependency-injection library: Manager, module wiring, supervised tasks, health/readiness, boundary analysis, and the provenance/discovery/verify/contract agent-support packages.
  - composition root: `examples/bootstrap`
  - composition root: `examples/deployment`
  - composition root: `examples/hexagonal`
  - composition root: `examples/quickstart`
  - composition root: `examples/external-consumer`

## Boundaries

- **core-no-adapter-deps** — enforced by `make check-consumer-boundary` (scripts/check-consumer-boundary.sh)
  The core modulex package must not import any messaging or observability adapter package (nats, rabbitmq, watermill, otel, chi, httpx are all optional, separately importable).
- **feature-module-boundary** — enforced by `make check-module-boundary` (tools/modboundary)
  examples/deployment's feature modules must not reach into each other's internal packages.

## Command matrix

| Class | Name | Command | Reason |
|---|---|---|---|
| approval_required | make-release | `make release VERSION=vX.Y.Z` | tags and pushes a release; tagging/publishing a release always requires explicit human approval per agent-safety-policy.md |
| destructive | git-clean-f | `git clean -f` | irreversibly deletes untracked files; requires explicit human approval per agent-safety-policy.md |
| mutating | gofmt-w | `gofmt -s -w .` | rewrites tracked source files in place |
| networked | go-mod-download | `go mod download` | fetches module content from the configured module proxy |
| safe | go-test | `go test ./...` | read-only; compiles and runs tests with no side effects |

## Verification

Focused checks are recommended for a specific, scoped change. Full gates are always required before push or release, regardless of what changed — focused checks are never a substitute for full gates.

### Focused checks

| Name | Command | Reason | Required tool |
|---|---|---|---|
| gofmt-check | `gofmt -s -l .` | fast formatting check, cheap enough to run on every change | gofmt |

### Full gates (always required before push or release)

| Name | Command | Reason | Required tool |
|---|---|---|---|
| build | `make build` | compiles all packages and examples; required before any push or release | go |
| check-api-compat | `make check-api-compat` | reports public API changes since the latest git tag | go |
| check-changelog | `make check-changelog` | verifies CHANGELOG.md is updated when required (PR diff vs origin/main) | git |
| check-consumer-boundary | `make check-consumer-boundary` | verifies the core package has no adapter dependencies | go |
| check-module-boundary | `make check-module-boundary` | runs the modboundary analyzer against examples/deployment to enforce feature-module boundaries | go |
| lint | `make lint` | runs golangci-lint across the repository; required before any push or release | golangci-lint |
| test | `make test` | runs the full test suite; required before any push or release | go |
| test-arch | `make test-arch` | runs the full test suite under the race detector; required before any push or release | go |

## Safety policy

The default posture in this repository is repository-local and read-only: agents may read any file and run non-mutating commands freely. The following always require explicit, current-session human approval — a general standing instruction to "work autonomously" does not itself cover these unless it names them specifically:

- Pushing to a shared branch or the default branch.
- Opening, merging, or closing a pull request.
- Tagging or publishing a release.
- Deleting a branch, file, or remote resource not created by the agent in the current session.
- Any infrastructure or CI/CD configuration change.
- Any external mutation (issue tracker, chat, or third-party service state).
- Force-pushing, history rewriting, or skipping commit hooks/CI gates.

See `docs/planning/agent-safety-policy.md` for the full policy; this section summarizes it and does not replace it. Where the two disagree, `docs/planning/agent-safety-policy.md` wins.

### Protected paths (do not modify without explicit human approval)

- `.github/CODEOWNERS`
- `.github/workflows/*.yml`
- `CHANGELOG.md`
- `CODEOWNERS`
- `SECURITY.md`
- `go.mod`

### Required credentials (names only — never values)

- `GITHUB_TOKEN`

## Handoff

Before reporting work as complete, run the full verification gates above and produce a handoff artifact conforming to: `provenance.Envelope v1.0.0`. Report skipped or unavailable checks explicitly — never report a check as passing when it was skipped or could not run.

<!-- End of generated content. Source: modulex.agent.yaml (schema v1.0.0). Regenerate via agentdocs.Generate rather than editing by hand. -->

<!-- The section below is static, not generated from modulex.agent.yaml — it documents tooling with no contract.Contract field (see tools/agentcli/agentcli.go's toolingAddendum). Edit it there, not here. -->

## CodeGraph

This project uses CodeGraph (`.codegraph/codegraph.db`) as the source of truth for code navigation. Before starting work on this project or beginning a new turn, run:

```bash
codegraph sync
```

When investigating code, prefer querying CodeGraph over raw `grep`/`find`. Useful queries:

```bash
# Find symbols by name
codegraph query "Manager"

# Find definitions in a file
codegraph query --file modulex.go "StartModules"

# Show index status
codegraph status
```

### Keeping CodeGraph in sync

Git hooks are installed under `.git/hooks` to run `codegraph sync` automatically on commit, checkout, merge, and rewrite. To install them in a fresh clone:

```bash
./scripts/install-codegraph-hooks.sh
```

#### Agent-specific hooks

- **Kimi Code CLI**: hooks are configured in `~/.kimi-code/config.toml`. The project-specific hook at `~/.kimi-code/hooks/codegraph-sync.sh` runs `codegraph sync` on `SessionStart` and `UserPromptSubmit` when the session cwd is this repo.
- **Claude Code**: run `codegraph install` and choose global or local installation to enable native Claude Code hook integration.
- **Antigravity / `agy`**: does not expose a pre-turn hook mechanism. Rely on the git hooks above and this rule.

All agents (Kimi, Claude, Antigravity) must use CodeGraph for locating symbols, call sites, and references.

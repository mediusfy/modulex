
## Agent safety policy

Before making mutating, networked, or external changes (pushing, opening/
merging PRs, tagging releases, transitioning Jira issues, deleting
files/branches, or editing CI config), read
[docs/planning/agent-safety-policy.md](docs/planning/agent-safety-policy.md).
It defines command classification, protected paths, secret-handling rules,
and which actions require explicit human approval. It applies to Claude,
Kimi, OpenAI/Codex, generic MCP clients, and no-hook environments alike.

## Commands

- **Build all**: `make build` (builds Go services + frontend)
- **Lint all**: `make lint` (runs Go + frontend linters)
- **Test all**: `make test` (runs Go + frontend tests)
- **Run tests**: `go test ./...` in subdirectories or use `make test`
- **Lint Go**: `golangci-lint run ./...`
- **Format Go**: `gofmt -w .`
- **Install dependencies**: `make deps` (run inside `platform/` or root)

## CodeGraph

This project uses CodeGraph (`.codegraph/codegraph.db`) as the source of truth for code navigation. Before starting work on this project or beginning a new turn, run:

```bash
codegraph sync
```

When investigating code, prefer querying CodeGraph over raw `grep`/`find`. Useful queries:

```bash
# Find symbols by name
codegraph query "StartWorkloadTestRun"

# Find definitions in a file
codegraph query --file web/backend/internal/k8s/testrun_session.go "TestRunStatus"

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
- **Antigravity / `agy`**: does not expose a pre-turn hook mechanism. Rely on the git hooks above and this `AGENTS.md` rule.

All agents (Kimi, Claude, Antigravity) must use CodeGraph for locating symbols, call sites, and references.

## Before Pushing to GitHub

`make test-arch`, `make build`, `make lint`, and `make test` must all pass locally before pushing any branch or commit to GitHub. Do not push if any of these fail.




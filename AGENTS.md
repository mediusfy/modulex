
## Agent safety policy

Before making mutating, networked, or external changes (pushing, opening/
merging PRs, tagging releases, transitioning Jira issues, deleting
files/branches, or editing CI config), read
[docs/planning/agent-safety-policy.md](docs/planning/agent-safety-policy.md).
It defines command classification, protected paths, secret-handling rules,
and which actions require explicit human approval. It applies to Claude,
Kimi, OpenAI/Codex, generic MCP clients, and no-hook environments alike.

## Commands

Modulex is a Go module monorepo (core `modulex` package plus adapter
sub-packages `chi`, `nats`, `rabbitmq`, `watermill`, `otel`, `grpc`, and
standalone leaf packages such as `discovery`, `provenance`, `verify`,
`review`, `contract`, `agentdocs`, `semindex`, `approval`, `patchapply`,
`modtest`).
There is no frontend or web service in this repository. See `make help`
for the full target list; the ones used most often:

- **Build**: `make build` (compiles all packages and examples)
- **Test**: `make test` / **race + arch**: `make test-arch`
- **Lint**: `make lint`
- **Format**: `make fmt`
- **Install dependencies**: `make deps`
- **Vulnerability scan**: `make vuln`
- **Boundary checks**: `make check-consumer-boundary`, `make check-module-boundary`
- **API compatibility report**: `make check-api-compat`
- **Changelog check**: `make check-changelog`

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
- **Antigravity / `agy`**: does not expose a pre-turn hook mechanism. Rely on the git hooks above and this `AGENTS.md` rule.

All agents (Kimi, Claude, Antigravity) must use CodeGraph for locating symbols, call sites, and references.

## Before Pushing to GitHub

`make test-arch`, `make build`, `make lint`, and `make test` must all pass locally before pushing any branch or commit to GitHub. Do not push if any of these fail.




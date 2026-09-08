# Agent Pointcuts Guide

An agent working in this repository should not have to guess the rules — the
library states them, enforces them, and verifies the result. The pointcut
layer does that: four interception points, wired identically into Claude
Code, opencode, Antigravity, and Kimi.

One principle drives the design: **the library is the single source of
repository logic.** The hooks never re-implement a check. They shell into
the repo's own `modulex` CLI (`doctor`, `agent generate -check`,
`agent verify`, the approval store) — the same domain packages the MCP
server exposes. One contract (`modulex.agent.yaml`), one enforcement path,
any agent.

## The pointcuts

All four live in `scripts/agenthooks/`. Each reads the host's hook payload
from stdin (or `MODULEX_HOOK_PAYLOAD` for hosts that can't pipe stdin) and
answers with an exit code: `0` allows, `2` blocks with the reason on stderr.

| Pointcut | Script | Fires | Does |
|---|---|---|---|
| pre | `pre.sh` | session start | Builds the `modulex` CLI into `.modulex/bin/`, prints `modulex doctor` and the generated-doc drift status into the agent's context. Never blocks. |
| guard | `guard.sh` | before each edit / shell command | Denies edits to `protected_paths`, hand-edits of the generated AGENTS.md/CLAUDE.md, and commands classed `destructive`/`approval_required` — unless a live grant exists in `.modulex/approvals.json`. Also blocks force-pushes and `--no-verify`. |
| while | `while.sh` | after each edit (or each turn) | Runs the contract's focused checks (today: `gofmt -s -l`) on exactly the touched files. With no file payload, checks every dirty `.go` file. |
| post | `post.sh` | when the agent believes it is done | Blocks conclusion until generated docs match the contract, touched `.go` files are gofmt-clean, and `modulex agent verify` passes on the committed diff. `MODULEX_HOOK_FULL=1` escalates to the full gates. |

## Setup per agent

**Claude Code, opencode, and Antigravity work out of the box** — their
adapters are checked in and contain no logic of their own:

| Host | Wiring | Pointcuts |
|---|---|---|
| Claude Code | `.claude/settings.json` (hooks) + `.mcp.json` (MCP server) | pre, guard, while, post |
| opencode | `.opencode/plugin/modulex-pointcuts.js` + `opencode.json` (MCP server) | pre, guard, while, post |
| Antigravity | `.antigravity/hooks/hooks.json` | pre, guard, while (per-turn), post |

**Kimi Code CLI** configures hooks globally, not per repository. Add this to
`~/.kimi-code/config.toml`; the scripts read the session `cwd` from the hook
payload and stay silent outside this repo:

```toml
[[hooks]]
event = "SessionStart"
command = "/path/to/modulex/scripts/agenthooks/pre.sh"
timeout = 180

[[hooks]]
event = "UserPromptSubmit"
command = "/path/to/modulex/scripts/agenthooks/while.sh"
timeout = 60
```

Kimi exposes no tool-call or session-end hook, so the guard and post gates
don't run there — run `modulex agent verify -base <ref>` before handing
work off.

## Approvals

`guard.sh` never grants anything. When it blocks a command it names the
remedy:

    modulex agent approve -action <command-name> -approved-by <human> [-ttl 10m]

run by a human, outside the agent's loop. The grant lands in
`.modulex/approvals.json` (gitignored, sensitive) and is honored by this
guard and by the MCP server's `run_verification` alike.

## Failure posture

The hooks degrade loudly, never silently. A missing go toolchain, a missing
python3 (guard and post need it to parse payloads — both stand down with a
warning rather than enforce blindly or block forever), or a verify run in
which no check executed is reported as "not verified", never as success.

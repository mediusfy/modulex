# Agent Pointcuts Guide

Modulex aims to be an AI-first-class citizen: an agent working in this
repository should not have to *infer* the rules of engagement — the library
tells it, enforces them, and verifies the result. This guide documents the
pointcut layer that does that: three aspect-oriented interception points
(**pre**, **while**, **post**) plus a per-tool-call **guard**, wired
identically into Claude Code, opencode, and Antigravity.

The design principle is the same one tools/mcpserver follows: **the library
is the single source of repository logic.** The hooks never re-implement a
check — they shell into the repo's own `modulex` CLI (`doctor`,
`agent generate -check`, `agent verify`, the approval store), which wraps the
same domain packages (`contract`, `verify`, `approval`, `agentdocs`,
`provenance`) the MCP server exposes. One contract (`modulex.agent.yaml`),
one enforcement path, any agent.

## The pointcuts

All four live in `scripts/agenthooks/` and are tool-agnostic: they read the
host's hook payload from stdin (or `MODULEX_HOOK_PAYLOAD` for hosts that
cannot pipe stdin), and communicate back through exit codes — `0` allow,
`2` block with the reason on stderr.

| Pointcut | Script | Fires | Does |
|---|---|---|---|
| pre | `pre.sh` | session start | Builds the `modulex` CLI into `.modulex/bin/`, prints `modulex doctor`, reports AGENTS.md/CLAUDE.md drift (`agent generate -check`), and announces the active pointcuts. Everything on stdout lands in the agent's context — the library introducing itself. Never blocks. |
| guard | `guard.sh` | before each edit / shell command | Enforces the contract: denies edits to `protected_paths`, redirects hand-edits of the generated AGENTS.md/CLAUDE.md to `modulex agent generate`, and blocks commands classed `destructive`/`approval_required` unless a matching unexpired grant exists in `.modulex/approvals.json` (written by a human via `modulex agent approve`, outside the agent loop). Also blocks force-pushes and `--no-verify` per agent-safety-policy.md. |
| while | `while.sh` | after each edit (or each turn) | Runs the contract's focused checks (`verification.focused` — today `gofmt -s -l`) on exactly the touched surface, so drift is fed back the moment it appears. Given no per-file payload (per-turn hosts), it checks every dirty `.go` file instead. |
| post | `post.sh` | when the agent believes it is done | Makes "done" mean what the contract says: no generated-doc drift, focused gofmt clean over dirty files, and `modulex agent verify -base <merge-base>` passing over the committed diff. Failure blocks conclusion (exit 2) and hands the agent the failing checks; success reminds it to produce the `provenance.Envelope v1.0.0` handoff via `modulex agent handoff`. `MODULEX_HOOK_FULL=1` escalates verify to the full gates. Honors Claude Code's `stop_hook_active` to avoid block loops. |

## Adapters

Each host wires the same four scripts through its native hook system; the
adapters contain no logic of their own.

**Claude Code** — `.claude/settings.json`:
`SessionStart → pre.sh`, `PreToolUse (Edit|Write|MultiEdit|NotebookEdit|Bash)
→ guard.sh` (exit 2 denies the tool call), `PostToolUse (edits) → while.sh`,
`Stop → post.sh` (exit 2 blocks the agent from concluding). `.mcp.json`
additionally registers tools/mcpserver, so the agent can call
`read_contract`, `recommend_verification`, `run_verification`, `review_diff`,
`create_handoff`, and `discover_repository` directly.

**opencode** — `.opencode/plugin/modulex-pointcuts.js`:
plugin init → `pre.sh`, `tool.execute.before` → `guard.sh` (a thrown error
blocks the call), debounced `file.edited` → `while.sh`, `session.idle` →
`post.sh`. Payloads travel via `MODULEX_HOOK_PAYLOAD`. `opencode.json`
registers the same MCP server.

**Antigravity** — `.antigravity/hooks/hooks.json`:
`PreInvocation → pre.sh`, `PreToolUse → guard.sh`,
`PostInvocation → while.sh` (per-turn mode, no file payload),
`Stop → post.sh`.

## Approval flow

`guard.sh` never grants anything. When it blocks a `destructive` or
`approval_required` command it names the exact remedy:

    modulex agent approve -action <command-name> -approved-by <human> [-ttl 10m]

run by a human outside the agent loop. The grant lands in
`.modulex/approvals.json` (gitignored, sensitive) and is honored by both this
guard and tools/mcpserver's `run_verification` — the same file bridges every
process that needs to agree an approval exists.

## Failure posture

The hooks degrade transparently, never silently: a missing go toolchain, an
unbuildable CLI, or a verify run in which no check actually executed is
*reported* to the agent as "not verified" rather than swallowed as success —
matching the contract's handoff rule that a skipped check must never be
reported as passing.

# Hooking coding agents into Modulex: what's built, what's wired, and what's still just a library

Someone asked a reasonable question this week: what hooks should we set up
in Antigravity, Claude Code, Kimi Code CLI, and opencode so they use Modulex
more effectively? The honest answer turned out to require checking two
things first — what ADR-0032 ("Agent-First Development Experience")
actually promised, and what of that promise is reachable by an agent today
versus sitting in a Go package nobody calls yet. This post is that audit,
plus the resulting recommendation.

**Update:** item 1 below (`approval/` wired into `run_verification`) is
now done — see the [Agent Approval Broker Guide](../planning/agent-approval-broker-guide.md)'s
"Wired for visibility, not yet for granting" section. The `approval/`
quotes in this post reflect the state at the time it was written.

## What "hooks" means here

Two different mechanisms get called "hooks" in this conversation, and
they're not interchangeable:

- **Agent hooks** — a coding agent's own lifecycle events (`SessionStart`,
  `PreToolUse`, `Stop`, ...) that run a script when something happens
  inside *that agent's* session. Claude Code and Kimi Code CLI have these.
  Antigravity does not.
- **MCP tools** — endpoints a server exposes that an agent calls on
  demand, mid-conversation, regardless of which lifecycle hooks it
  supports. This is provider-agnostic: any MCP client can use it.

Modulex already has the second one. The question is really about the
first — and about how much of the second is actually finished.

## What's already wired

`tools/mcpserver` is a real, working, read-only MCP server exposing six
tools, all backed by the same domain packages the CLI and CI use — no
second source of truth:

| Tool | Backs |
|---|---|
| `discover_repository` | `discovery.Discover` |
| `read_contract` | `modulex.agent.yaml` parse + `contract.Contract.Validate` |
| `recommend_verification` | `verify.PlanFor` |
| `run_verification` | `verify.Run`, gated by `discovery.ClassifyCommand` |
| `review_diff` | `review.Review` (boundaries, secrets, API compat, changelog, protected paths) |
| `create_handoff` | `provenance.Envelope` assembly + `Validate` |

Any MCP-capable agent gets all of this for free by registering the server
— no per-provider code required. That was ADR-0032's explicit intent:
*"No provider-specific hook or global configuration may be required for
the baseline workflow."*

On top of MCP, there's `tools/agentcli`'s `modulex agent generate`, which
renders `AGENTS.md` and `CLAUDE.md` straight from `modulex.agent.yaml` —
this is the "generated agent instruction files" leg of the ADR, and it's
what keeps this repository's own `AGENTS.md`/`CLAUDE.md` in sync with the
contract instead of hand-drifting. And there are real git hooks today
(`scripts/install-codegraph-hooks.sh`: `post-commit`, `post-checkout`,
`post-merge`, `post-rewrite`) that keep CodeGraph synced — the one piece
of automation that works identically for every agent, including
Antigravity, because git hooks don't care what's driving the commits.

## What's built but not wired to anything

This is the part worth saying plainly: three of ADR-0032's P2 items are
**fully implemented, fully tested, fully documented** — and completely
inert. Nothing calls them.

- **`approval/`** — a real in-memory approval broker (token-based,
  `crypto/rand`, scope-bound grants) for the "elevated action" gate the
  safety policy describes in prose. Its own guide says it outright:
  *"This is a design and mechanism, not an integration. No CLI, no MCP
  server, and no other call site in this repository invokes `approval`
  yet."*
- **`patchapply/`** — atomic patch application with rollback journaling,
  for ADR-0032's "apply patches atomically and retain rollback
  information." Same story: *"no CLI, no MCP server, and no call site in
  this repository invokes it yet."*
- **`semindex/`** — the generic framework for validating that a semantic
  index (CodeGraph, TokenSave) actually points at the active worktree
  before an agent trusts its results. Explicitly scoped as "no CLI" in its
  own guide.

None of these show up as an MCP tool. None show up as a `modulex agent`
subcommand. They're correct, tested Go packages sitting one layer below
where an agent could reach them.

## The CLI gap

ADR-0032's "Standard agent workflow" describes eight `modulex agent`
subcommands: `discover`, `inspect`, `plan`, verify, `review`, `handoff`,
plus the edit step in between. `tools/agentcli/cmd/modulex` implements
exactly **one**: `generate`. Every other workflow step the ADR describes as
CLI-reachable is, today, only reachable through the MCP server. That's
fine for an MCP-capable agent — it's the whole read-only workflow, tool by
tool. It's a real gap for anything that can shell out but doesn't speak
MCP.

## Recommendation, per provider

**Claude Code** — register `tools/mcpserver` as an MCP server
(`claude mcp add`); that's the whole read-only workflow with zero
provider-specific code. Layer a `SessionStart`/`UserPromptSubmit` hook
calling `read_contract` to prime the session the same way the existing
CodeGraph-sync hook primes the graph. The interesting one is a
`PreToolUse` hook on `Bash`: shell out to the same classification logic
`run_verification` already uses (`discovery.ClassifyCommand`) and actually
block `Destructive`/`ApprovalRequired` commands before they run, instead
of relying on the agent reading `agent-safety-policy.md` prose and
self-policing. This is also the natural place to finally call `approval/`
for something real — a blocked command could mint an approval token the
human then explicitly redeems, instead of the policy living only in
markdown.

**Kimi Code CLI** — same `SessionStart`/`UserPromptSubmit` mechanism, already
wired in `~/.kimi-code/config.toml` for CodeGraph sync; a `read_contract`
priming hook slots in identically. MCP support is unconfirmed — check
before assuming it's there.

**Antigravity** — no pre-turn hook mechanism, full stop (documented in
`AGENTS.md`). The only lever is git hooks and static file content. That
means the git-hooks script is the actual integration point for
Antigravity, not agent-level configuration — e.g. extending
`install-codegraph-hooks.sh` with a `pre-push` step that runs
`review_diff`'s protected-paths check, so an unauthorized `go.mod` edit
gets caught even for an agent that can't be handed a live hook.

**opencode** — not mentioned anywhere in this repository's contract or
docs. Its hook and MCP capabilities are unverified here; worth checking
before designing anything provider-specific for it.

## What to actually build next

In priority order, given what's already true:

1. **Wire `approval/` into `run_verification`'s block path.** Right now a
   blocked command (`ApprovalRequired`/`Destructive`) just returns
   "blocked" — there's a real broker sitting unused that could turn that
   into an actual grant/redeem flow instead of a dead end.
2. **Add the missing `modulex agent` CLI subcommands** (`discover`,
   `verify`, `review`, `handoff` at minimum) so the ADR's workflow is
   reachable without MCP — this is what makes Modulex useful to an agent
   that can shell out but doesn't speak MCP (or a plain CI step).
3. **Extend the git-hooks script** with a `pre-push` (or `pre-commit`)
   check that calls `review.CheckProtectedPaths`/`review.Review` directly
   — the one piece of automation every provider gets, including
   Antigravity, for free.
4. **Confirm opencode's actual hook/MCP surface** before writing any code
   for it.

Everything above composes with what already exists — `tools/mcpserver`,
`review`, `verify`, `contract`, `approval`, `patchapply`, and `semindex`
are all real, tested packages. The work left is wiring, not invention.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [Agent MCP Server Guide](../planning/agent-mcp-server-guide.md)
- [Agent Approval Broker Guide](../planning/agent-approval-broker-guide.md)
- [Agent Atomic Patch Guide](../planning/agent-atomic-patch-guide.md)
- [Semantic Index Diagnostics Guide](../planning/semantic-index-diagnostics-guide.md)
- [Agent Instruction Generation Guide](../planning/agent-instruction-generation-guide.md)

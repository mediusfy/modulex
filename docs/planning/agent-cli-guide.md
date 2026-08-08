# Agent CLI Guide

This guide covers `tools/agentcli` and the `modulex` binary it builds
(`tools/agentcli/cmd/modulex`): the `modulex agent` CLI, a shell-invokable
entry point to the same domain logic
[`tools/mcpserver`](agent-mcp-server-guide.md) exposes over MCP, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)'s
"Standard agent workflow" — for an agent or CI step that doesn't speak
MCP, or a step (like `approve`, below) that specifically must not be
something an agent can invoke on its own behalf.

`agentcli` is a separate, nested Go module (own `go.mod`), like
`tools/mcpserver`, `tools/modboundary`, `tools/scaffold`, and
`tools/provenanceci`: this keeps `cmd/modulex`'s dependencies out of the
root module's build graph. It depends on the root module via a local
`replace` directive. `cmd/modulex` is the actual binary; `agentcli` holds
each subcommand's logic as plain, testable Go functions, so nothing here
needs to exec a built binary to test — the same split
`tools/mcpserver`'s handlers use.

## Two subcommands

```
modulex agent generate [-root <path>]
modulex agent approve -action <name> [-resource <name>] -approved-by <name> [-ttl <duration>] [-root <path>]
```

| Subcommand | Domain function | What it does |
|---|---|---|
| `generate` | `agentcli.LoadContract` + `agentcli.WriteGeneratedFiles` | Renders `AGENTS.md`/`CLAUDE.md` from `<root>/modulex.agent.yaml` |
| `approve` | `agentcli.Approve` | Grants an approval a separately-running MCP server can see |

## `generate`

Reads `<root>/modulex.agent.yaml`, renders `AGENTS.md` and `CLAUDE.md` via
`agentdocs.Generate` plus a static tooling addendum
(`agentcli.go`'s `toolingAddendum` — CodeGraph usage and per-agent hook
setup, content that has no `contract.Contract` field to derive from), and
writes both at `<root>`, overwriting any existing copies. `LoadContract`
has no "not present" tri-state the way `tools/mcpserver`'s `readContract`
does: `generate` has nothing useful to do without a valid contract, so a
missing or invalid file is a plain error.

This repository regenerates its own `AGENTS.md`/`CLAUDE.md` this way
whenever `modulex.agent.yaml` changes —
`agentcli.TestGeneratedFiles_MatchCheckedIn` fails CI if either checked-in
file drifts from what the contract would currently produce.

## `approve`

Grants an approval for `-action` (and, if given, `-resource`), attributed
to `-approved-by`, valid for `-ttl` (default 10 minutes), by writing to
`<root>/.modulex/approvals.json` — an
[`approval.FileStore`](agent-approval-broker-guide.md) at
`approval.DefaultStorePath(root)`, the exact same file
`tools/mcpserver`'s `run_verification` reads (via the non-consuming
`Broker.DryRunCheck`) to populate its `approval_status` output field for a
blocked check.

```console
$ modulex agent approve -action make-release -approved-by drew@jocham.io -ttl 5m
granted: Grant{token_hash=... scope=make-release approved_by=drew@jocham.io ...}
token (sensitive — do not share or log this): 4f9c2e...
```

**This is a human-run command, not something an agent should invoke on its
own behalf.** An approval is only meaningful if it's granted outside the
agent's own tool-calling loop — an agent that could call `approve` for
itself would defeat the entire point of the approval boundary
(`agent-safety-policy.md`'s "Human approval boundary"). `approve` never
runs, unblocks, or executes anything itself; it only records that a human
has approved a specific, scoped action. `run_verification`'s existing
command classification (`discovery.ClassifyCommand`) still decides what's
blocked — an approval only becomes visible in that tool's output, it never
changes what the tool will actually execute.

`-action` should match the check `name` a blocked `run_verification` call
already reported (its `Scope.Action`), so the grant is visible for the
same check the human is looking at. `-resource` is empty (unscoped) unless
the caller has a reason to scope the approval further — see `Scope`'s doc
comment for why an empty and a non-empty `Resource` are never
interchangeable.

Because `-root` selects which repository's approvals file is read and
written, granting for the wrong `-root` is silently a no-op from the MCP
server's perspective (it just never sees the grant) — always pass the
same `-root` a corresponding `run_verification` call used, or the
repository's actual working directory if omitted (`.` is the default).

## Why two separate processes, one file

`modulex agent approve` and `tools/mcpserver`'s MCP server are different
OS processes with no shared memory — a CLI invocation's `approval.Broker`
is fresh and empty every time. The only way a grant reaches the MCP
server's own `DryRunCheck` calls is by both resolving
`approval.DefaultStorePath(root)` to the identical file and reading it
fresh: `approve` loads, grants, and saves back; `run_verification` loads
(never caching) on every call. See the
[Agent Approval Broker Guide](agent-approval-broker-guide.md)'s "Wired end
to end" section for the full mechanism.

## Related work

- [Agent MCP Server Guide](agent-mcp-server-guide.md) — the MCP-facing
  half of this same domain logic
- [Agent Approval Broker Guide](agent-approval-broker-guide.md) —
  `approval.Broker`/`FileStore`, which `approve` writes to
- [Agent Repository Contract Guide](agent-repository-contract-guide.md) —
  `modulex.agent.yaml`'s schema, which `generate` reads
- [Agent Instruction Generation Guide](agent-instruction-generation-guide.md) —
  `agentdocs.Generate`, which `generate` wraps
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  "Human approval boundary" section `approve`'s human-only framing follows
- ADR-0032's "Standard agent workflow" and "Portability" sections
- Jira MOD-69: the approval broker `approve` exposes over the CLI

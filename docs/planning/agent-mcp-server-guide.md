# Agent MCP Server Guide

This guide covers `tools/mcpserver`: the read-only MCP server for agent
workflows, per [ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience")'s "MCP boundary" section (Jira
MOD-68):

> The initial MCP server is read-only and should expose stable operations
> such as: discover projects; read the repository contract; inspect the
> module graph; find affected modules; recommend verification; run
> declared verification; review a diff; create a handoff.
>
> Write-capable tools are separate, disabled by default, and require an
> explicit approval token or human confirmation. The MCP server must call
> the same domain and CLI APIs rather than implementing a second source of
> repository logic.

Every tool below is a thin adapter over an existing leaf package —
`discovery.Discover`, `contract.Contract`/`Validate`, `verify.PlanFor`/`Run`,
`review.Review`, `provenance.Envelope` — with no new domain logic; see each
package's own guide for the mechanics `mcpserver` inherits rather than
reimplements.

`tools/mcpserver` is a separate, nested Go module (own `go.mod`), like
`tools/modboundary`, `tools/scaffold`, and `tools/provenanceci`: this keeps
the MCP SDK dependency
([`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk),
the official Go SDK, maintained with Google) out of the root module's build
graph. It depends on the root module via a local `replace` directive, the
same way `tools/provenanceci` does. Verified alongside the other nested
modules by `make check-nested-modules`.

## No write-capable tools

Nothing in this package can mutate the target repository, run a git command
beyond a read-only one (`status`, `diff`, `rev-parse`), grant or check an
approval, or apply a patch. This is what makes the server "read-only" per
the ADR. A future ticket may add a separate, explicitly write-capable tool
set (behind its own approval mechanism, per ADR-0032's "Safety and
governance" section) — this package intentionally does not.

## The six tools

| Tool | Input | Output | Backing call |
|---|---|---|---|
| `discover_repository` | `root` | `repository` (`discovery.Repository`) | `discovery.Discover` |
| `read_contract` | `root` | `present`, `contract`, `validation_errors` | read `<root>/modulex.agent.yaml` + `yaml.Unmarshal` + `contract.Contract.Validate` |
| `recommend_verification` | `changed_files` | `plan` (`verify.Plan`) | `verify.PlanFor` |
| `run_verification` | `root`, `checks`, `allow_network` | `results` (`[]provenance.VerificationResult`) | `discovery.Discover` (tools) + `verify.Run` |
| `review_diff` | `root`, `base_ref`, `head_ref`, `allow_network` | `results` (`[]provenance.VerificationResult`) | `discovery.Discover` (tools) + `review.Review` |
| `create_handoff` | `root`, `agent_name`, `verification` | `envelope` (`provenance.Envelope`) | `discovery.Discover` + `git rev-parse` + `provenance.Envelope.Redact`/`Validate` |

Two of ADR-0032's eight listed operations collapse into an existing tool
rather than getting one of their own, because the underlying package output
already contains both:

- **"inspect the module graph"** → `discover_repository`. `discovery.Repository.Modules`
  and `.CompositionRoots` already *are* the module graph; a separate tool
  would return a strict subset of `discover_repository`'s own output.
- **"find affected modules"** → `recommend_verification`. `verify.PlanFor`'s
  `Plan.FocusedChecks` already *is* "what's affected by these changed
  files"; `recommend_verification` and "find affected modules" are the same
  call under two names.

Every tool's handler is a thin `AddTool` adapter over a plain, table-tested
Go function (`discoverRepository`, `readContract`, `recommendVerification`,
`runVerification`, `reviewDiff`, `buildHandoffEnvelope`) — see
`tools/mcpserver/*.go` and their `*_test.go` files.

### `read_contract`'s tri-state result

`read_contract` never treats "no contract file" as an error — most
repositories don't have a `modulex.agent.yaml` yet, and that's a normal
outcome an agent should be able to branch on, not a failure to catch:

| `present` | `contract` | `validation_errors` | Meaning |
|---|---|---|---|
| `false` | — | — | No `modulex.agent.yaml` at `root` |
| `true` | `null` | one parse-error string | File present but not valid YAML |
| `true` | parsed contract | one string per violated rule | File present, parsed, but fails `Contract.Validate()` |
| `true` | parsed contract | `null` | File present and fully valid |

## Safety: `run_verification` executes the `Command` you give it, verbatim

`run_verification`'s `checks[i].command` is executed by `verify.Run` via a
shell (`sh -c`), with no sanitization — this is not a new risk introduced
by the MCP layer; `verify.CheckSpec.Command` has always worked this way for
its existing callers (`verify.FullGates`, `review.Checks`). What's new is
that `run_verification` lets an MCP *caller* supply `Command` directly,
rather than only ever running commands `verify.PlanFor` built itself from
this repository's own trusted rule table.

**`run_verification` is intended for `Command` values that originated from
this repository's own tooling** — copied from a prior `recommend_verification`
or `review_diff` result, or from `verify.FullGates` — **not arbitrary
caller-authored shell.** This is consistent with the ADR's framing: "run
declared verification" is explicitly listed as part of the *read-only*
server's surface. Read-only here means "no tool writes to the repository or
mutates external state" (no file writes, no git mutation, no approvals) —
not "no tool ever spawns a process."

`review_diff`'s `base_ref`/`head_ref`, by contrast, are safe from injection
by construction regardless of content: `review.Review`'s `git diff`
invocation uses an argv slice (`exec.CommandContext`), never a shell — a bad
ref just makes the git command fail, surfaced as a `StatusUnavailable`
result, never as arbitrary code execution.

## Worked example

The server is stateless across calls — an agent chains tool calls itself,
passing results from one into the next:

1. `discover_repository{root: "."}` — read the module layout, available
   tools, and whether the tree is dirty.
2. `read_contract{root: "."}` — check for repository-specific rules and
   safe/mutating/destructive command classifications.
3. `recommend_verification{changed_files: ["verify/verify.go"]}` — get
   focused checks for what changed, plus the full gate list.
4. `run_verification{root: ".", checks: <plan.focused_checks>}` — run them.
5. `review_diff{root: ".", base_ref: "origin/main", head_ref: "HEAD"}` —
   check the diff for boundary/secret/compatibility/changelog issues.
6. `create_handoff{root: ".", agent_name: "claude", verification: <results from 4 and 5>}` —
   assemble a `provenance.Envelope` recording what ran and what it found.

## Running the server

```
cd tools/mcpserver && go build -o mcpserver ./cmd/mcpserver
```

The binary speaks MCP over stdio (`mcp.StdioTransport`) — a host like
Claude Code spawns it as a subprocess and communicates over its stdin/stdout,
the same way `tools/provenanceci`'s CLI is invoked from a CI step, except
`mcpserver` is long-running rather than one-shot.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-verification-guide.md`](agent-verification-guide.md) —
  `verify.CheckSpec`/`Run`/`PlanFor`, which `run_verification`/`recommend_verification` wrap
- [`docs/planning/agent-diff-review-guide.md`](agent-diff-review-guide.md) —
  `review.Review`, which `review_diff` wraps
- [`docs/planning/agent-discovery-guide.md`](agent-discovery-guide.md) —
  `discovery.Repository`, which `discover_repository` wraps and every
  tool-gated call (`run_verification`, `review_diff`) relies on
- [`docs/planning/agent-repository-contract-guide.md`](agent-repository-contract-guide.md) —
  `modulex.agent.yaml`'s schema, which `read_contract` parses
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  `provenance.Envelope`, which `create_handoff` assembles
- Jira MOD-63/MOD-65/MOD-64/MOD-66: `verify`/`review`/`discovery`/`provenance`,
  the packages this server wraps

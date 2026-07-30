# Agent Instruction Generation Guide

This guide covers the `agentdocs` package: rendering a `contract.Contract`
into provider-specific agent instruction documents, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P1: "Generate portable agent
instruction files and repository templates" (Jira MOD-67). The ADR's
"Portability" section is the source of truth for what this package
produces:

> The repository contract is authoritative. Thin adapters may generate:
>
> - `AGENTS.md` for OpenAI/Codex and generic repository-aware agents;
> - `CLAUDE.md` or Claude-specific hooks when available;
> - Kimi project guidance and optional CodeGraph hook setup;
> - MCP discovery metadata and read-only tool descriptions.
>
> No provider-specific hook or global configuration may be required for
> the baseline workflow. Agents without hooks must still be able to use
> the CLI and contract directly.

`agentdocs` is the "thin adapter": it turns one `contract.Contract` into
consistent, provider-specific guidance so `AGENTS.md`, `CLAUDE.md`, Kimi
guidance, and Codex guidance never drift from the contract, or from each
other, on the facts that matter — commands, verification, protected paths,
credentials, handoff format. Only the framing text differs per target.

`agentdocs` is a standalone leaf package
(`github.com/mediusfy/modulex/agentdocs`) that does **not** import the core
`modulex` package, the same "small, independent package" convention already
used by `provenance`, `discovery`, `verify`, and `contract`. It depends on
`contract` for the `Contract` schema and otherwise only on the standard
library (`text/template`, `sort`, `strings`). It does not import
`discovery` or `verify`: the command matrix and verification guidance are
derived entirely from `contract.Contract.Commands` and
`contract.Contract.Verification`, which already carry name, command,
class, and reason for every entry.

## What it generates, and why four targets

```go
type Target string

const (
    TargetAGENTS Target = "AGENTS.md"
    TargetCLAUDE Target = "CLAUDE.md"
    TargetKimi   Target = "kimi"
    TargetCodex  Target = "codex"
)

func Generate(c contract.Contract, target Target) (string, error)
```

One `Generate` function takes a `Target` parameter rather than four
separate `GenerateAGENTS`/`GenerateCLAUDE`/`GenerateKimi`/`GenerateCodex`
functions. All four outputs share one section structure built from the
same `Contract` fields; only the title and introductory framing differ:

| Target | Title | Framing |
|---|---|---|
| `TargetAGENTS` | AGENTS.md — Repository Agent Instructions | Baseline guidance for OpenAI/Codex and any other generic, repository-aware coding agent; the file OpenAI/Codex and generic agents read per ADR-0032. |
| `TargetCLAUDE` | CLAUDE.md — Claude Code Instructions | Claude-specific framing for Claude Code sessions; notes that a Claude-specific hook may supplement it but is not required. |
| `TargetKimi` | Kimi Code CLI — Project Guidance | Project guidance for the Kimi Code CLI; references the `~/.kimi-code/config.toml` hook convention (see this repository's own `AGENTS.md`, "Agent-specific hooks") as an optional supplement, not a requirement. |
| `TargetCodex` | Codex / Repository-Aware Agent Instructions | Guidance for the OpenAI Codex CLI/cloud agent and other generic repository-aware agents that read a dedicated instructions document distinct from `AGENTS.md` by filename, even though its content shape mirrors `AGENTS.md` by design. |

An unknown `Target` (a typo, or a caller built before a fifth target was
added) is a caller bug, so `Generate` returns an error rather than
panicking or silently emitting an empty or default document.

## Every generated document carries

- **A source-version header and footer.** Every document opens with
  `<!-- GENERATED FROM modulex.agent.yaml (schema vX.Y.Z) — DO NOT EDIT BY
  HAND. ... -->` naming the exact `Contract.SchemaVersion` value the
  document was rendered from (not the `contract.SchemaVersion` package
  constant — the two can differ if a contract file is pinned to an older
  schema version) and closes with a matching footer. Markdown tolerates an
  HTML comment anywhere, so this works identically for all four targets;
  see the package doc comment's "All four targets render as Markdown"
  section for why a second comment syntax was not introduced for Kimi/Codex.
- **A command matrix.** `Contract.Commands`, rendered as a Markdown table
  (Class, Name, Command, Reason), sorted by Class then Name so the table's
  row order never depends on `modulex.agent.yaml`'s incidental YAML
  ordering.
- **Safety-policy content, rendered standalone.** `Contract.ProtectedPaths`
  and `Contract.RequiredCredentials` (names only), plus a static summary of
  [`agent-safety-policy.md`](./agent-safety-policy.md)'s approval boundary
  (push, PR, release, deletion, infrastructure/CI change, and external
  mutation all require explicit human approval). That approval-boundary
  bullet list is hand-written in `renderSafetySection`, not derived from
  the contract — `Contract` intentionally has no approval-boundary field of
  its own — so a document is useful standalone even if a reader never
  opens `agent-safety-policy.md`. If the two documents ever disagree,
  `agent-safety-policy.md` (linked from every generated document) is the
  one that wins.
- **Verification guidance**, `Contract.Verification.Focused` and `.Full` as
  two clearly separated tables, with the same "focused checks are never a
  substitute for full gates" framing `verify`'s own doc comment uses.
- **Handoff guidance**, if `Contract.HandoffFormat` is set: a short
  statement naming the format and requiring skipped/unavailable checks to
  be reported explicitly, never silently omitted.

Everything above is common to all four targets — deliberately, since the
acceptance criterion is "one contract generates *consistent*
provider-specific guidance," not four independently-maintained documents
that could disagree on a protected path or a required credential.

## Determinism and drift detection

`Generate(c, target)` produces byte-identical output for the same input on
every call. `Contract`'s slice fields (`Commands`,
`Verification.Focused`/`Full`, `ProtectedPaths`, `RequiredCredentials`,
`Projects`, `Boundaries`, and so on) carry no ordering guarantee coming out
of YAML — a human reordering entries in `modulex.agent.yaml` without
changing their meaning must not change the generated document's byte
content in a way that looks like real drift. Every such slice is copied
and sorted before rendering (by Class-then-Name for commands, by Name for
checks/projects/boundaries, alphabetically for plain string lists) —
mirroring the sorted-slice determinism discipline `provenance` and
`discovery` already use — and the sort operates on a copy, never
`c`'s own slice, so `Generate` never mutates a `Contract` a caller still
holds a reference to.

```go
func Drift(c contract.Contract, target Target, existingContent string) (bool, error)
```

`Drift` reports whether `existingContent` (e.g. read by the caller from a
checked-in `AGENTS.md`) differs from what `Generate(c, target)` would
currently produce — "the checked-in file is stale relative to
`modulex.agent.yaml`." Neither `Generate` nor `Drift` reads or writes a
file; both are pure string-in, string-out (or `contract.Contract`-in)
functions, so a future `modulex agent generate`/`modulex agent verify` CLI
step (out of scope for this ticket) can wire in the actual file I/O and
call `Drift` in CI to fail a build whose checked-in `AGENTS.md` no longer
matches its contract.

## Usage example

```go
data, err := os.ReadFile("modulex.agent.yaml")
if err != nil {
    return err
}
var c contract.Contract
if err := yaml.Unmarshal(data, &c); err != nil {
    return err
}
if err := c.Validate(); err != nil {
    return err
}

for _, target := range []agentdocs.Target{
    agentdocs.TargetAGENTS,
    agentdocs.TargetCLAUDE,
    agentdocs.TargetKimi,
    agentdocs.TargetCodex,
} {
    doc, err := agentdocs.Generate(c, target)
    if err != nil {
        return err
    }
    // A caller-owned step: write doc to the target's conventional path,
    // or diff it against the checked-in file via Drift.
    fmt.Println(doc)
}
```

A truncated sample of `Generate(c, agentdocs.TargetAGENTS)` against this
repository's own `contract/testdata/modulex.agent.example.yaml` fixture:

```markdown
<!-- GENERATED FROM modulex.agent.yaml (schema v1.0.0) — DO NOT EDIT BY HAND. ... -->

# AGENTS.md — Repository Agent Instructions

This file is baseline guidance for OpenAI/Codex and any other generic,
repository-aware coding agent operating in this repository, per
ADR-0032's portability guidance. ...

## Command matrix

| Class | Name | Command | Reason |
|---|---|---|---|
| approval_required | make-release | `make release VERSION=vX.Y.Z` | tags and pushes a release; ... |
| destructive | git-clean-f | `git clean -f` | irreversibly deletes untracked files; ... |
...
```

See `agentdocs/testdata/golden/` for the full generated output of all four
targets against this same fixture — one golden file per target,
regenerated and byte-compared on every test run, which both documents what
each target looks like in full and catches unintentional template drift.

## Out of scope

- No CLI command (`modulex agent generate`) reads `modulex.agent.yaml` or
  writes `AGENTS.md`/`CLAUDE.md` from it yet — that is future work that
  would call this package.
- This repository's own real `AGENTS.md` is not generated by this package
  today; `agentdocs` is a library a future CLI/script would call, not a
  replacement for the hand-maintained file in this repository's root.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [Agent Repository Contract Guide](./agent-repository-contract-guide.md)
- [Agent Verification Guide](./agent-verification-guide.md)
- [Agent safety, secrets, and approval policy](./agent-safety-policy.md)
- Jira MOD-67: Generate portable agent instruction files and repository templates

# ADR-0032: Agent-First Development Experience

## Status

Proposed

## Context

Modulex already provides useful foundations for AI-assisted development:
deterministic lifecycle behavior, dependency graphs, typed service wiring,
supervised tasks, health/readiness checks, boundary analysis, examples, and
repository verification gates. However, an AI coding agent still has to infer
how to select a project, discover applicable rules, choose safe commands,
identify affected modules, run the right checks, and produce an auditable
handoff.

The repository currently relies mainly on human-readable `AGENTS.md` guidance,
Make targets, CI configuration, and optional indexing tools. That works for
experienced contributors but creates setup friction and inconsistent behavior
across Claude, Kimi, OpenAI/Codex, IDE agents, and generic MCP clients.

We want a developer to point an agent at a repository, select a project, and
begin useful work with safe defaults, explicit guardrails, industry-standard
verification, and a clear human approval boundary.

## Decision

Modulex will provide an agent-facing workflow built around one canonical,
versioned repository contract and multiple thin integrations.

```text
repository contract
        |
        +--> modulex agent CLI
        |       +--> discover / inspect / plan
        |       +--> verify / review / handoff
        |
        +--> generated AGENTS.md / CLAUDE.md / Kimi guidance
        |
        +--> read-only MCP server
        |
        +--> runtime diagnostics and boundary analyzers
```

The runtime library remains focused on lifecycle orchestration and
technology-neutral diagnostics. Repository inspection, Git/worktree handling,
command execution, approvals, provenance, and agent protocol concerns belong
in the CLI, optional packages, or a separate MCP server.

## Canonical repository contract

Repositories may provide a versioned `modulex.agent.yaml` contract. It should
describe:

- projects, Go modules, composition roots, and relevant source paths;
- applicable instruction files and precedence rules;
- lifecycle and module boundaries;
- safe, mutating, networked, destructive, and approval-required commands;
- focused and repository-wide verification commands;
- generated and protected paths;
- required tools and optional services;
- secret and credential requirements without storing secret values;
- expected artifacts, reports, and handoff format.

Human-readable agent files remain supported, but generated or validated agent
instructions should derive from this contract rather than becoming competing
sources of truth.

## Standard agent workflow

1. `modulex agent discover` identifies the repository root, projects, modules,
   composition roots, instruction files, Make targets, CI workflows, and
   available indexes.
2. `modulex agent inspect` returns the applicable rules, module graph,
   boundaries, relevant files, required checks, and environment requirements.
3. `modulex agent plan` records intended files, affected modules, tests,
   boundary implications, approval requirements, and rollback strategy.
4. The agent edits in an isolated worktree or applies an explicit patch.
5. `modulex agent verify` runs focused checks followed by required repository
   gates and reports skipped checks separately from successful checks.
6. `modulex agent review` checks the diff for unexpected files, secrets,
   boundary violations, API compatibility changes, generated-file drift, and
   missing changelog or documentation updates.
7. `modulex agent handoff` creates a human-readable report and a structured
   provenance envelope.
8. Human approval is required for elevated actions such as network access,
   release, push, deletion, infrastructure changes, or external mutations.

## Safety and governance

The default agent mode is repository-local and read-only. Write-capable tools
must use explicit path allowlists and isolated worktrees where possible.

The system must:

- never expose secret values in prompts, reports, or persisted artifacts;
- classify commands by filesystem, network, destructive, and approval impact;
- provide dry-run behavior for external or destructive actions;
- preserve dirty-worktree state and unrelated edits;
- apply patches atomically and retain rollback information;
- record skipped checks, missing tools, and unavailable services explicitly;
- require human approval for push, release, deletion, infrastructure changes,
  database migrations, and external Jira/PR mutations;
- redact command output before it enters provenance artifacts;
- verify that semantic indexes such as CodeGraph or TokenSave point at the
  active worktree before their results are trusted.

## Provenance and handoff

The handoff format should record, without secrets:

- repository path, branch, commit, and dirty-worktree state;
- agent, tool, and CLI versions;
- changed files and artifact hashes;
- commands, classifications, exit codes, durations, and environment needs;
- focused and full verification results;
- boundary, compatibility, security, and secret-scan results;
- skipped checks and their reasons;
- approvals and rollback status.

The format should be versioned and suitable for PR descriptions, CI artifacts,
MCP responses, and audit systems.

## Portability

The repository contract is authoritative. Thin adapters may generate:

- `AGENTS.md` for OpenAI/Codex and generic repository-aware agents;
- `CLAUDE.md` or Claude-specific hooks when available;
- Kimi project guidance and optional CodeGraph hook setup;
- MCP discovery metadata and read-only tool descriptions.

No provider-specific hook or global configuration may be required for the
baseline workflow. Agents without hooks must still be able to use the CLI and
contract directly.

## MCP boundary

The initial MCP server is read-only and should expose stable operations such as:

- discover projects;
- read the repository contract;
- inspect the module graph;
- find affected modules;
- recommend verification;
- run declared verification;
- review a diff;
- create a handoff.

Write-capable tools are separate, disabled by default, and require an explicit
approval token or human confirmation. The MCP server must call the same domain
and CLI APIs rather than implementing a second source of repository logic.

## Scope and ownership

### Modulex core

Keep lifecycle, dependency graphs, typed wiring, supervised tasks,
health/readiness, tracing, and technology-neutral runtime diagnostics here.

### Optional packages

Potential packages include `diagnostics`, `agentcontract`, `provenance`, and
reusable boundary-analysis integrations. They must remain usable without
forcing agent tooling on ordinary runtime consumers.

### CLI

The CLI owns discovery, project selection, planning, verification, diff review,
handoff generation, worktree support, and approval checks.

### MCP server

The MCP server exposes the CLI/domain capabilities to agents and remains
read-only by default.

### Templates

Templates provide contract files, generated agent guidance, safe-action policy,
verification checklists, and handoff schemas for new repositories.

## Non-goals

This ADR does not make Modulex an autonomous deployment system, unrestricted
computer-use agent, secrets manager, CI replacement, or provider-specific
agent framework. It does not grant agents permission to push, release, delete,
or mutate external systems by default.

## Consequences

### Positive

- Agents can start with repository-specific context instead of reconstructing
  it from scattered files.
- Safety, approvals, and verification become explicit and portable.
- Claude, Kimi, OpenAI/Codex, and generic MCP clients can share one contract.
- Handoffs become auditable and easier for humans to review.
- Existing lifecycle, boundary, and CI capabilities become agent-accessible.

### Negative

- The project must maintain a contract schema and compatibility policy.
- CLI and MCP tooling add release and security responsibilities.
- Command classification and approval design require careful maintenance.
- Some agent features depend on host capabilities and cannot be made fully
  portable.

## Delivery roadmap

### P0

- Define and validate the repository contract.
- Add project discovery and command classification.
- Extend MOD-60 with structured agent-consumable diagnostics.
- Add focused verification with explicit skipped statuses.
- Document safe defaults, secrets, approvals, and forbidden actions.

### P1

- Add provenance and handoff JSON.
- Add diff review for boundaries, secrets, API compatibility, and changelog
  obligations.
- Generate agent instruction files and repository templates.
- Provide a read-only MCP server.

### P2

- Add an approval broker for elevated tools.
- Add atomic patch application and rollback journaling.
- Publish provenance artifacts from CI.
- Add CodeGraph/TokenSave index-root validation and diagnostics.

## Success criteria

- A new repository can be discovered and selected with one documented command.
- An agent receives machine-readable rules, affected modules, and verification
  requirements before editing.
- Unsafe actions are classified and approval-gated.
- Focused and full verification results distinguish pass, fail, skipped, and
  unavailable states.
- A handoff artifact can reproduce what the agent did and why.
- The same contract supports Claude, Kimi, OpenAI/Codex, and generic MCP use.

## Related work

- `docs/adr/adr-0031-modulex-value-and-specialization-roadmap.md`
- `docs/planning/library-readiness-checklist.md`
- `AGENTS.md`
- `tools/modboundary/`
- Jira MOD-60: Add Modulex diagnostics and module-contract export

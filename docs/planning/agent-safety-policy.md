# Agent safety, secrets, and approval policy

Status: Active (P0 of [ADR-0032](../adr/adr-0032-agent-first-development-experience.md))

This document defines the safe defaults and forbidden actions for any AI
coding agent working in this repository — Claude, Kimi, OpenAI/Codex, IDE
agents, generic MCP clients, and any agent running without hook support. It
is the canonical policy referenced by [AGENTS.md](../../AGENTS.md) and, once
delivered (Jira MOD-62), by the versioned `modulex.agent.yaml` repository
contract. Where this document and a generated agent instruction file
disagree, this document wins.

## Scope

This policy applies to any automated or semi-automated agent session
operating on this repository's working tree, git history, CI configuration,
or connected external systems (GitHub, Jira, SonarCloud, package registries).
It does not apply to a human operator's own manual commands.

## Default posture

The default agent mode is **repository-local and read-only**:

- Agents may read any file in the working tree and run non-mutating
  commands (`go build`, `go vet`, `go test`, `golangci-lint run`, `git
  status`/`diff`/`log`) freely.
- Any action that writes outside the working tree, mutates git history,
  contacts a network endpoint, or mutates a third-party system requires
  either an isolated worktree/branch, explicit human approval, or both, as
  detailed below.
- When in doubt about whether an action is safe, an agent must treat it as
  requiring approval rather than assuming permission.

## Command classification

Every command an agent runs falls into one of these classes. Agents should
classify commands before running them, not after.

| Class | Examples | Rule |
|---|---|---|
| Safe / read-only | `go build`, `go test`, `go vet`, `golangci-lint run`, `git status`, `git diff`, `git log`, `gofmt -l` | Always allowed. |
| Local mutating | editing tracked files, `git add`, `git commit` on a feature branch, `gofmt -w` | Allowed on a feature branch or isolated worktree; never on `main` directly. |
| Networked, non-destructive | `go mod download`, `govulncheck`, reading from SonarCloud/Jira/GitHub APIs | Allowed, but agents must not embed secret values in the command line or log output (see Secrets below). |
| Destructive / history-rewriting | `git reset --hard`, `git push --force`, `git clean -f`, `git branch -D`, deleting files not created this session | Requires explicit human approval before running. Never used as a shortcut past a stuck agent. |
| External mutation | `git push`, opening/merging a pull request, commenting on or transitioning a Jira issue, tagging/publishing a release, posting to Slack | Requires explicit human approval for the specific action and scope, granted either inline in conversation or via a durable instruction (e.g. this file, a project CLAUDE.md, or an explicit user request in the current session). A prior approval does not extend to a different action or a broader scope than what was granted. |
| Infrastructure / environment | modifying CI workflow files, changing branch protection, rotating credentials, changing repository settings | Always requires explicit human approval; agents should treat these as higher-risk than ordinary code changes even when technically permitted by their credentials. |

## Protected paths

Agents must not modify the following without explicit human approval, even
if a task's scope would technically touch them:

- `.github/workflows/*.yml` — CI/CD pipeline definitions.
- `go.mod` `retract` directives and any published version's git tag —
  published module versions are immutable on `proxy.golang.org` the moment a
  tag is pushed (see the release process notes in `CONTRIBUTING.md` and
  `Makefile`'s `release` target).
- `CHANGELOG.md` version-section boundaries for already-released versions
  (adding to `## [Unreleased]` is expected and encouraged).
- `SECURITY.md`, `CODEOWNERS`/`.github/CODEOWNERS`, and any file governing
  who can approve or merge changes.
- Files outside the repository working tree (global git config, shell
  profiles, credential stores) — an agent's write access is scoped to the
  repository unless a task explicitly and narrowly requires otherwise.

## Isolated worktrees and dirty-state preservation

- Prefer an isolated git worktree or a dedicated feature branch for any
  non-trivial change, so a human's in-progress or uncommitted work is never
  at risk.
- Before running any command that can discard uncommitted work (`git
  checkout --`, `git restore`, `git reset --hard`, `git clean`), an agent
  must run `git status` and, if it finds unexpected in-progress state,
  preserve it (stash with `-u`, or commit to a side branch) rather than
  discard it. Files the agent created itself earlier in the same session are
  the exception — the agent may clean those up freely.
- An agent must never assume a dirty working tree is disposable scratch
  state belonging to it.

## Secrets and credentials

- Secret values (API tokens, passwords, private keys, session cookies) must
  never appear in a prompt, an agent's own output, a commit message, a PR
  description, a Jira comment, a provenance/handoff artifact, or a log line
  an agent writes to disk.
- Agents may *reference* where a secret lives (an environment variable name,
  a keychain entry, a config file path) but must not print, echo, or embed
  its value, including inside a shell command that would put it in shell
  history or CI logs.
- Command output that might contain secret values (e.g. verbose HTTP
  request/response dumps, `env` output) must be redacted before it enters
  any artifact intended for humans or CI, not just before it's stored
  long-term.
- If an agent discovers a secret committed to the repository or exposed in
  logs, it must flag this to the human operator immediately rather than
  attempting remediation (such as history rewriting) on its own, since
  remediation of an already-exposed secret is itself a destructive,
  approval-required action (rotation, history rewrite, force-push).

## Human approval boundary

The following always require an explicit, current-session human approval —
a general standing instruction to "work autonomously" does not itself cover
these unless it names them specifically:

- Pushing to a shared branch or `main`.
- Opening, merging, or closing a pull request.
- Tagging or publishing a release (`make release`, `git tag`, `make
  publish-godev`).
- Deleting a branch, file, or remote resource not created by the agent in
  the current session.
- Any infrastructure or CI/CD configuration change.
- Any external mutation: creating/transitioning/commenting on a Jira issue,
  posting to Slack, modifying a third-party service's configuration.
- Force-pushing, history rewriting, or skipping commit hooks / CI gates
  (`--no-verify`, `--no-gpg-sign`).

Scope discipline: an approval covers the specific action and scope granted
(e.g. "push and open a PR for this ticket"), not a standing blanket
authorization for unrelated future actions, even within the same session.

## Dry runs and rollback

- Where a command supports a dry-run or plan-only mode (`terraform plan`,
  `--dry-run` flags, `git merge --no-commit --no-ff` to preview), prefer it
  before the mutating equivalent when the action is destructive or external.
- Patches should be applied atomically: either a full logical change lands,
  or none of it does. Partial, half-applied refactors must not be left on a
  branch that could be merged.
- Before an irreversible action (tagging a release, force-pushing, deleting
  a remote branch), an agent should be able to state what the rollback path
  is, or explicitly flag that there isn't one.

## Verification before handoff

Before proposing a change as complete, an agent must run the repository's
required gates (`make build`, `make lint`, `make test`, `make test-arch`,
and any focused checks relevant to the change) and report pass, fail,
skipped, and unavailable states separately (see Jira MOD-63 for the
structured verification workflow this feeds into). An agent must never
report a check as passing when it was actually skipped or unavailable
(e.g. a required external service being unreachable is not the same as the
check passing).

## Pre-acceptance review checklist

A human reviewing agent-produced work before accepting it should confirm:

- [ ] The diff touches only files relevant to the stated task — no
      unexplained changes to unrelated files.
- [ ] No secret values appear anywhere in the diff, commit messages, or PR
      description.
- [ ] No protected path (see above) was modified without prior explicit
      approval.
- [ ] `make build`, `make lint`, `make test`, and `make test-arch` were
      actually run and reported as passing (not assumed).
- [ ] Any external mutation (push, PR, release, Jira transition) was
      explicitly approved for this specific action.
- [ ] `CHANGELOG.md`'s `## [Unreleased]` section was updated if the change
      is user-visible.
- [ ] Any skipped or unavailable check is called out explicitly, with a
      reason, rather than silently omitted.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [ADR-0031: Modulex value and specialization roadmap](../adr/adr-0031-modulex-value-and-specialization-roadmap.md)
- `AGENTS.md`
- Jira MOD-62: Define and validate the Modulex agent repository contract (forthcoming `modulex.agent.yaml`, which will supersede this document as the machine-readable source once delivered)
- Jira MOD-63: Add focused agent verification with explicit skipped statuses

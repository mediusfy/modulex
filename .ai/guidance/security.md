# Security

Modulex is a public library plus agent tooling that reads repositories, applies patches and gates agent actions, and a hosted PR-review service that handles webhooks and installation tokens. Vulnerability reports go through [SECURITY.md](../../SECURITY.md), never a public issue. The agent rules here summarise [docs/planning/agent-safety-policy.md](../../docs/planning/agent-safety-policy.md), which wins where the two differ.

## Agent safety policy

- The default is repository-local and read-only. Destructive commands (`git reset --hard`, `git clean -f`, force-push, deleting files you did not create) and external mutations (push, opening or merging a PR, releases, issue tracker or chat changes) need explicit, current-session human approval for that action.
- Protected paths need explicit human approval: `.github/workflows/*.yml`, `.github/CODEOWNERS`, `CODEOWNERS`, `SECURITY.md`, `go.mod` (retract directives) and released `CHANGELOG.md` sections. Adding to `## [Unreleased]` is expected.
- `scripts/agenthooks/guard.sh` enforces protected paths, the generated `AGENTS.md`/`CLAUDE.md`, and command classes from `modulex.agent.yaml`. A grant comes only from a human running `modulex agent approve`, which writes `.modulex/approvals.json` (mode 0600, gitignored). Never write that file yourself or work around the guard.
- Report skipped or unavailable checks as such, never as passing.

## Code that handles untrusted input

- `patchapply` resolves every target path against the repository root and rejects absolute paths, `..` components and symlink escapes (`patchapply/path.go`). Any new code that writes a caller- or model-supplied path must use it rather than `os.WriteFile` on a joined path.
- `review` and `provenance` scan diffs and output for secret shapes and redact them; `review/protectedpaths.go` flags protected-path changes. Keep secret values out of envelopes, logs and PR comments. Tests should use obviously fake values that the scanners still recognise.
- `tools/mcpserver` is the read-only half of the agent tooling: it does not edit the repository, and `run_verification` runs only checks declared in the contract. Adding a tool that mutates files or state changes its security model and needs an ADR.
- `services/prreview`: the receiver verifies the webhook HMAC with a constant-time comparison (`webhook.VerifySignature`), caps the body at 1 MiB, dedupes deliveries and rate-limits per installation before enqueueing. The worker uses a short-lived installation token per job, and each installation's AI key is read from a secret store, never from config or the repo. AI failure degrades to an engine-only review.

## Library behaviour

- A panicking handler or task must become an error, not a crash: EventBus adapters use `internal/handlerpanic.Recover`, and supervised tasks (`modulex.go`) and `workerpool` recover their own goroutines. New goroutine entry points need the same.
- Blocking operations take a context and respect cancellation; unbounded queues or goroutines are a denial-of-service bug (`workerpool` exists to bound them).

## Workflows

- Actions are pinned to full commit SHAs with the version in a comment; read-only `permissions:` by default, widened per job only where needed (`lint` gets `checks: write`, `release` gets `contents: write`, Scorecard its documented set).
- `PR Review` and `CI` run PR-head code with a read-only token and no secrets, so they are safe on fork PRs. Keep secrets out of `pull_request` jobs.
- Pass `github.event` values to scripts through `env:`, not by interpolating them into `run:`.

## Agent context files

- `.ai/recent_context.md` is written by `.github/workflows/ai-recent-context.yml` from merged PR text, so it is a channel from PR authors to every coding agent. The workflow only takes merged PRs from this repo (never forks), reads the text through the API instead of `run:`, quotes each entry and caps its length. Keep those guards if you change it.

## Secrets

- Never commit secret values. `modulex.agent.yaml` lists only credential names (`GITHUB_TOKEN`). Terraform state and variable files for `infra/prreview` stay out of git (`infra/prreview/.gitignore`).

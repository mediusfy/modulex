# Agent Provenance CI Guide

This guide covers `tools/provenanceci` and the `provenance` job in
`.github/workflows/ci.yml`: publishing a `provenance.Envelope` as a build
artifact for every CI run, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P2: "Publish provenance artifacts
from CI" (Jira MOD-72).

## What gets published, and where

Every push to `main` and every pull request produces a `provenance.json`
artifact, named `provenance-<commit-sha>`, retained for 90 days, attached to
the `provenance` job in that workflow run. It is a
[`provenance.Envelope`](provenance-handoff-schema.md) recording:

- **Repository state**: the commit SHA and branch this run checked out
  (`github.sha`/`github.ref_name`), and whether the working tree was dirty
  by the time the artifact was built (`git status --porcelain`, checked
  fresh in the `provenance` job itself).
- **Agent**: `{"name": "ci", "tool": "github-actions"}` — this envelope
  describes what CI verified, not an AI agent's actions; `AgentInfo` is
  general enough to describe either.
- **Verification**: one `provenance.VerificationResult` per CI job (Category
  `VerificationFull`, since each corresponds 1:1 to one of this
  repository's required gates), sorted by name.

## Example

```json
{
  "schema_version": "1.0.0",
  "repository": { "path": ".", "branch": "main", "commit": "f3a5f44...", "dirty": false },
  "agent": { "name": "ci", "tool": "github-actions" },
  "verification": [
    { "name": "api-compat", "category": "full", "status": "pass" },
    { "name": "build-and-test", "category": "full", "status": "pass" },
    { "name": "changelog", "category": "full", "status": "skipped", "reason": "GitHub Actions job was skipped (its \"if\" condition was not met)" },
    { "name": "lint", "category": "full", "status": "pass" }
  ],
  "created_at": "2026-08-02T20:12:50Z"
}
```

## `tools/provenanceci`: a separate, nested Go module

Like `tools/modboundary` and `tools/scaffold`, `tools/provenanceci` is its
own Go module (own `go.mod`), not part of the root module's build graph —
verified alongside them by `make check-nested-modules`. It depends on the
root module for `provenance.Envelope` the same way
`examples/external-consumer` does, via a local `replace` directive.

```go
func BuildEnvelope(cfg Config) (provenance.Envelope, error)
```

`Config` carries repository state plus `Jobs []JobResult`, one entry per CI
job (`Name`, `Result`). `BuildEnvelope` maps each job's `Result` — one of
GitHub Actions' documented `needs.<job>.result` values — to a
`provenance.VerificationResult`:

| GitHub Actions result | `Status` | `Reason` |
|---|---|---|
| `success` | `StatusPass` | — |
| `failure` | `StatusFail` | — |
| `cancelled` | `StatusFail` | "GitHub Actions job was cancelled before completing" |
| `skipped` | `StatusSkipped` | "GitHub Actions job was skipped..." (required by `Envelope.Validate` for `StatusSkipped`) |
| anything else | `StatusFail` | names the unrecognized value |

An unrecognized value fails safe (`StatusFail`, not silently treated as a
pass) — the same fail-safe-default philosophy `discovery` and `verify`
already follow for anything this repository's tooling cannot confidently
interpret.

Results are sorted by job name before being returned, so the artifact's
`verification` array is deterministic regardless of the order `-job` flags
were passed in.

`BuildEnvelope` calls `Envelope.Redact()` then `Envelope.Validate()` before
returning; a non-nil error means the envelope itself was malformed (most
likely a missing commit) — a bug in `provenanceci` or its caller, not a
CI job failure. Individual job failures are recorded as `StatusFail`
`VerificationResult`s inside an otherwise-valid envelope, never surfaced as
an error here.

## The `provenanceci` CLI

```
provenanceci -commit <sha> -branch <name> -out provenance.json \
    -job build-and-test=success -job lint=success ...
```

`-job name=result` is repeatable; at least one is required. `-commit` is
required. `-branch`/`-repo`/`-out` are optional (default `""`, `"."`,
`"provenance.json"`). The tool computes `Dirty` itself via `git status
--porcelain` in `-repo` (defaulting to "not dirty" if that git invocation
fails, e.g. `-repo` is not a git repository — a best-effort signal, not a
hard requirement).

## Wiring into `ci.yml`

The `provenance` job `needs` every other job in the workflow
(`build-and-test`, `integration-test`, `lint`, `vuln`,
`consumer-boundary`, `module-boundary`, `api-compat`, `changelog`) and runs
with `if: always()`, so it still publishes a record even when one or more
of those jobs failed or was cancelled — arguably the most useful case for a
provenance artifact to exist at all. Each `needs.<job>.result` value is
passed through as an environment variable (not interpolated directly into
the `run:` shell script) before being read back with `"$VAR"`, avoiding the
GitHub Actions script-injection anti-pattern of interpolating a `${{ }}`
expression directly into shell text.

The `provenance` job's own exit status reflects only whether it could
*produce* a valid envelope, never the pass/fail outcome of the jobs it
describes — see `BuildEnvelope`'s doc comment above for why.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  the `provenance.Envelope` schema this job produces
- [`docs/planning/agent-diff-review-guide.md`](agent-diff-review-guide.md) —
  `review.Review`, a related but distinct diff-scoped check this job does
  not run or depend on
- Jira MOD-66: `provenance` package (`Envelope`, `Redact`, `Validate`)

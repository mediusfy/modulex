# Provenance and Handoff Schema

This guide covers the `provenance` package: a versioned, JSON-marshalable
schema (`provenance.Envelope`) for recording what an AI coding agent did to a
repository and why, per [ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P1: "Add provenance and handoff
JSON."

`provenance` is a standalone leaf package (`github.com/mediusfy/modulex/provenance`)
that does **not** import the core `modulex` package or any other package in
this repository. A future `modulex agent handoff` CLI, a CI step, or any
other tool can depend on it alone to produce or consume an `Envelope`,
without pulling in the runtime library — the same "small, independent
package" convention already used by `httpx`, `otel`, `chi`, and the
messaging adapters.

## Why a schema, not just a log line

An agent's changes are auditable only if a human (or another system) can
later answer: what changed, what was run to verify it, what was skipped and
why, what required approval, and whether it can be undone. `Envelope` gives
those questions a fixed shape so they can be attached to a PR description, a
CI artifact, an MCP response, or an audit log, and compared across many
agent runs.

## Versioning

```go
const provenance.SchemaVersion = "1.0.0"
```

`SchemaVersion` is a plain semver string, not a Go module path or API
version — the schema is consumed as data, potentially by non-Go tooling
(a future CLI, CI steps, MCP clients) with no notion of Go module
compatibility rules. Bump the minor version for backward-compatible
additions (new optional fields), the major version for breaking changes
(renamed, removed, or retyped fields), and document every schema change in
`CHANGELOG.md`.

## The Status enum

Every command and verification step reports one of five explicit states,
never a bare pass/fail bool:

```go
const (
    StatusPass             Status = "pass"
    StatusFail             Status = "fail"
    StatusSkipped          Status = "skipped"
    StatusUnavailable      Status = "unavailable"
    StatusApprovalRequired Status = "approval_required"
)
```

This distinguishes "ran and passed" from "intentionally not run" (skipped,
with a reason — e.g. out of scope for this change) from "could not be run"
(unavailable, with a reason — e.g. a required tool or network endpoint was
missing) from "gated behind a human decision that hasn't happened yet"
(approval-required). `Validate` enforces that every `StatusSkipped` or
`StatusUnavailable` result carries a non-empty `Reason` — a step is never
allowed to silently disappear from the record.

## Schema shape

```go
type Envelope struct {
    SchemaVersion string
    Repository    RepoState
    Agent         AgentInfo
    Changes       []FileChange
    Commands      []CommandResult
    Verification  []VerificationResult
    Approvals     []Approval
    Rollback      *RollbackStatus
    CreatedAt     time.Time
}
```

- **`RepoState`** — path, branch, commit, and dirty-worktree state the
  envelope was produced from.
- **`AgentInfo`** — agent name, agent/model version, host tool, tool
  version, and CLI version, so a handoff can be attributed.
- **`FileChange`** — one changed file: its path (and `OldPath` for a
  rename), its `ChangeType` (`added`/`modified`/`deleted`/`renamed`), and an
  optional `"<algorithm>:<hex>"` content hash so a reviewer or CI system can
  confirm the handoff matches the actual diff.
- **`CommandResult`** — one command the agent ran: name, args,
  `CommandClass` (`safe`/`mutating`/`networked`/`destructive`/
  `approval_required`, per ADR-0032's command-classification requirement),
  `Status`, exit code (a `*int`, so "no exit code" is distinguishable from an
  explicit `0`), duration, environment needs (tool/service/credential
  *names*, never values), free-text output, and a reason.
- **`VerificationResult`** — one verification step, distinguished by
  `VerificationCategory` (`focused`, `full`, `boundary`, `compatibility`,
  `security`, `secret_scan`, `changelog`) rather than as separate top-level
  slices per category — this keeps the schema flat and lets new categories
  be added without a breaking change.
- **`Approval`** — one human approval granted for an elevated action (push,
  release, deletion, infrastructure change, ...), who granted it, when, and
  free-text notes.
- **`RollbackStatus`** — whether a rollback path exists, whether it's been
  applied, and by what method. `Envelope.Rollback` is a pointer: `nil` means
  rollback was never assessed, distinct from an assessed-but-unavailable
  `RollbackStatus{Available: false}`.

See `provenance/provenance.go` for full field-level doc comments, and
`provenance/testdata/sample-handoff.json` for a complete, realistic,
already-redacted example.

## Redaction and validation

Free-text fields (`CommandResult.Output`/`Args`/`Reason`,
`VerificationResult.Message`/`Reason`, `Approval.Notes`,
`RollbackStatus.Notes`) can end up holding raw command output, which might
contain a secret. Two functions guard against that:

```go
func (e *Envelope) Redact()        // scrub secret-shaped values in place
func (e *Envelope) Validate() error // reject structural errors and any
                                     // secret-shaped value left unredacted
```

`Redact` scans every free-text field for common secret-shaped patterns —
`AWS_SECRET*` env-var assignments, PEM private-key blocks, GitHub token
prefixes (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`), generic
`key=`/`token=`/`password=`/`secret=` assignments, and JWT-shaped strings —
and replaces matches with `[REDACTED]`.

`Validate` runs the same detection as a backstop: even if a caller forgets
to call `Redact`, `Validate` will refuse to pass an envelope that still
contains a secret-shaped value, alongside its structural checks (required
fields present; every skipped/unavailable result has a reason).

**This is a best-effort, pattern-based safety net, not a guarantee.** It
catches common, recognizable secret shapes, but it can both miss secrets
(false negatives) and flag ordinary strings that merely look secret-shaped
(false positives). The only real prevention is not putting secret values
into these fields in the first place — see
[`docs/planning/agent-safety-policy.md`](agent-safety-policy.md), the
human-facing policy this schema operationalizes.

## Building, redacting, validating, and marshaling one

```go
env := provenance.Envelope{
    SchemaVersion: provenance.SchemaVersion,
    Repository: provenance.RepoState{
        Path:   "/repo",
        Branch: "MOD-66-provenance-handoff-json",
        Commit: "abc1234",
        Dirty:  false,
    },
    Agent: provenance.AgentInfo{
        Name: "claude",
        Tool: "claude-code",
    },
    Commands: []provenance.CommandResult{
        {
            Name:           "make test",
            Classification: provenance.ClassSafe,
            Status:         provenance.StatusPass,
        },
    },
    CreatedAt: time.Now().UTC(),
}

env.Redact()
if err := env.Validate(); err != nil {
    return err
}

b, err := json.Marshal(env)
if err != nil {
    return err
}
fmt.Println(string(b))
```

`json.Marshal(env)` is deterministic across repeated calls against the same
data: every field in the schema is a scalar or an ordered slice, so unlike
the core module's `Manager.Diagnostics`/`ModuleContract` (which sort
map-derived slices for the same reason), `provenance` has no map types to
sort in the first place.

## Relationship to Manager.Diagnostics

`Manager.Diagnostics()` and `Manager.ModuleContract()` (see
[`diagnostics-guide.md`](diagnostics-guide.md)) are a **running manager's**
point-in-time state export, part of the core `modulex` package. `provenance.Envelope`
is a **record of agent work performed on the repository** — commands run,
files changed, checks passed or skipped, approvals granted — and lives in
its own package with no dependency on the core module. A future CLI could
embed a `Manager.Diagnostics()` snapshot inside a `provenance.Envelope`'s
free-text fields if useful, but the two schemas are independent today.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md)
- [`docs/planning/diagnostics-guide.md`](diagnostics-guide.md)
- `provenance/testdata/sample-handoff.json` — a complete example envelope

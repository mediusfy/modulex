# Agent Atomic Patch Guide

This guide covers the `patchapply` package: atomic, content-based file
mutation with rollback journaling for a single target directory, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P2: "Add atomic patch application
and rollback journaling" (Jira MOD-70). The ADR's "Safety and governance"
section requires that the system:

> preserve dirty-worktree state and unrelated edits
>
> apply patches atomically and retain rollback information

and [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md)'s "Dry
runs and rollback" section states the same requirement in policy form:

> Patches should be applied atomically: either a full logical change
> lands, or none of it does.

`patchapply` is the concrete mechanism satisfying both. It is a standalone
leaf package (`github.com/mediusfy/modulex/patchapply`) depending on
`approval` and `provenance` from this repository, and on the standard
library otherwise — no diff-parsing library, no third-party dependency of
any kind.

## Unlike every other package in this family, this one writes to disk

`provenance`, `discovery`, `verify`, `contract`, `agentdocs`, `semindex`,
and `approval` are all read-only or in-memory mechanisms. `patchapply` is
different: it genuinely writes to, and can destroy, real files on disk. A
bug here can lose a human's uncommitted work — categorically different
from a data-schema bug. Every design decision in this package (the
validation-before-any-write ordering, the deletion-requires-approval rule,
the atomic per-file writes, the all-or-nothing batch semantics) exists
because of that.

## Scoped to content-based changes, not diff-format parsing

"Patch" here means **a set of intended file mutations** — write new
content to a path, or delete a path — expressed directly as a
`[]FileChange`, not literal unified-diff or `git apply`-style patch text:

```go
type FileChange struct {
    Path                 string // relative to targetDir
    NewContent           []byte // ignored if Delete is true
    Delete               bool
    ExpectedPriorContent []byte // optional; nil skips the drift check
}
```

An agent (or any caller) that has already decided what a file's new
content should be does not need this package to also parse a diff format
to get there — it needs a safe place to land that content. This keeps
`patchapply` a filesystem-transaction primitive with zero new dependencies,
which is what the acceptance criteria below actually need. A caller
starting from unified-diff text is expected to parse it into a
`[]FileChange` itself (or with a library of its own choosing) before
calling `Apply`.

## The ordering guarantee

`Apply(targetDir string, changes []FileChange, opts ApplyOptions) (Journal, error)`
performs these steps, in this exact order, for the **entire batch** before
any filesystem write happens:

1. **Validate every path.** Every `FileChange.Path` must stay within
   `targetDir` — reject the whole batch on any violation (absolute path,
   `..`, or a path that resolves outside `targetDir` via a symlink),
   before touching anything.
2. **Refuse an unapproved delete outright.** If any change has
   `Delete: true` and no `approval.Broker` was supplied at all, reject the
   whole batch — see "Deletion requires approval" below.
3. **Check approval, if configured.** If a `Broker`/`Scope` was supplied
   (regardless of whether the batch contains a delete), call
   `Broker.Check(scope)` now and reject the whole batch — no filesystem
   access at all — if it does not return `provenance.StatusPass`.
4. **Check for unrelated drift.** Read every change's CURRENT on-disk
   content (or note it doesn't exist). This both populates the rollback
   journal's "original state" and enforces `ExpectedPriorContent`: if the
   file was edited since the caller computed this change set against a
   known baseline, the mismatch is caught here and the whole batch is
   rejected before any write.

Only after all four steps pass for the entire batch does `Apply` write
anything, one change at a time, via a temp-file-then-`os.Rename` pattern
(same directory as the target file, so the rename is same-filesystem and
therefore atomic). **If any individual write or delete fails partway
through, `Apply` immediately rolls back every change already applied
earlier in the same call**, using the journal entries captured so far,
before returning the error.

This ordering is deliberate:

- Validating paths before consulting the approval broker means a
  malformed batch never spends a single-use grant on garbage input.
- Checking approval before reading content means an unapproved destructive
  batch never even touches the filesystem to look.
- Checking for drift before writing anything means a batch that would
  have clobbered a human's unrelated edit never lands a single byte.

`Apply` returns exactly one of two outcomes — there is no third:

- **success**: everything applied, and the returned `Journal` describes
  exactly what happened to every file; or
- **failure**: the returned error explains what went wrong, and
  `targetDir` has already been restored to its exact pre-`Apply` state.

A caller never observes a partially-applied batch as `Apply`'s own return
value.

## Deletion requires approval — the precise rule

A batch containing **any** `FileChange` with `Delete: true` is rejected
outright, before touching the filesystem, **unless** `ApplyOptions.Broker`
is non-nil **and** `Broker.Check(scope)` returns `provenance.StatusPass`
for `ApplyOptions.Scope`. There is no way to delete a file through this
package without a broker configured and an approved grant for the exact
scope presented:

```go
b := approval.NewBroker()
scope := approval.Scope{Action: "delete", Resource: "generated/stale.txt"}
grant, _ := b.Grant(scope, "drew@jocham.io", 10*time.Minute)

journal, err := patchapply.Apply(targetDir, []patchapply.FileChange{
    {Path: "generated/stale.txt", Delete: true},
}, patchapply.ApplyOptions{Broker: b, Scope: scope})
```

Without a `Broker` at all, that same call is rejected with
`patchapply.ErrApprovalRequired`, no matter what the caller intended — a
nil `Broker` is not "caller opted out of gating," it is "deletion is
refused, full stop."

**Why the line is drawn at `Delete`, not at "any mutation":** a
pure-write batch (no `Delete: true` entries) may proceed with a nil
`Broker`, because overwriting a file's content is always recoverable via
this package's own rollback journal — the original bytes are retained in
every `Journal` a successful `Apply` returns. A deletion, by contrast, is
only recoverable if that journal survives and someone actually calls
`Rollback`; a process crash before that happens loses the file for good.
That asymmetry, not a blanket "writes are safe, deletes are dangerous"
claim, is the reasoning: a write's undo path lives entirely inside this
package's own returned `Journal`, while a delete's undo path additionally
depends on that journal surviving and being acted on.

This is a documented default, not a recommendation: a caller applying to a
real, possibly-shared worktree should supply a `Broker` for write-only
batches too, and should always set `ExpectedPriorContent`.

If a `Broker`/`Scope` is supplied for a pure-write batch, it is still
checked — supplying a `Broker` signals "gate this batch," independent of
whether it happens to contain a delete.

**A consequence worth knowing:** `Broker.Check` consumes a single-use
grant on a match, exactly once, at step 3 — before the drift check in step
4. If the batch is later rejected in step 4 (content drift) or fails
partway through its writes (step 5), the grant has already been spent; the
caller must obtain a fresh grant to retry. This follows directly from the
required step ordering (approval must be checked before the filesystem is
touched at all) combined with `approval.Broker`'s single-use-grant design
— see [`approval.Broker.DryRunCheck`](agent-approval-broker-guide.md) if a
caller wants to preview approval without spending a grant before a real
`Apply` attempt.

## Preserving unrelated dirty-worktree changes

```go
baseline, _ := os.ReadFile(filepath.Join(targetDir, "config.go"))

journal, err := patchapply.Apply(targetDir, []patchapply.FileChange{
    {
        Path:                 "config.go",
        NewContent:           newConfigContent,
        ExpectedPriorContent: baseline,
    },
}, patchapply.ApplyOptions{})
```

If a human (or any other process) edits `config.go` after `baseline` was
captured but before `Apply` runs, `Apply` detects the mismatch at step 4
and rejects the **entire** batch with `patchapply.ErrPriorContentMismatch`
— before writing anything. The human's edit is left exactly as they made
it. `ExpectedPriorContent` uses `bytes.Equal` semantics, under which `nil`
(file does not exist) and a non-nil empty slice compare equal — this
package does not distinguish "expected absent" from "expected present but
empty"; a caller that must distinguish those two cases needs to track
existence itself.

`ExpectedPriorContent` is optional (`nil` skips the check entirely) but a
caller applying to a real, possibly-dirty worktree should always set it —
`nil` is documented as strictly less safe, not as a recommended default.

## Rollback and repeat verification

```go
// Undo a previously successful Apply call, any time later.
if err := patchapply.Rollback(targetDir, journal); err != nil {
    log.Fatal(err)
}

// Confirm targetDir now exactly matches its pre-Apply state.
if err := patchapply.Verify(targetDir, journal); err != nil {
    log.Fatal(err) // names every file that still doesn't match
}
```

`Rollback` restores every file `Journal` recorded to its exact prior
bytes (for a file that existed before `Apply`) or removes it entirely,
including any directory chain `Apply` itself created for it (for a file
that did not exist before `Apply` — it is deleted, never left behind as an
empty file). `Verify` re-reads every journaled file and reports, per file,
any difference from the journal's recorded pre-`Apply` state — useful both
right after a `Rollback` (should report nothing) and later, to detect
drift that happened after a rollback.

`Apply`'s own internal partial-failure handling (step 5 in "The ordering
guarantee") uses the exact same rollback mechanism as the standalone
`Rollback` function — there is one rollback implementation, not two that
could drift out of sync with each other.

### A narrow limitation: directories this package creates

When `Apply` creates a new directory chain to write a file that had no
existing parent directory, `Rollback` removes that entire chain via
`os.RemoveAll` once every file `Apply` put in it has been individually
rolled back. If some other, concurrent process places an unrelated file
into that same directory after `Apply` ran but before `Rollback` does, that
file is also removed — `Rollback` has no way to distinguish "content this
package is responsible for" from "content a third party added to a
directory this package happens to own" once it decides to clean up the
whole chain. This is a real, confirmed behavior (not just a theoretical
concern), verified directly against this implementation.

This is a narrow, documented limitation, not a violation of any guarantee
this package makes: `Apply`/`Rollback` are scoped to a single target
directory under this package's own control, not a general-purpose
concurrent-multi-writer filesystem transaction manager. A caller operating
on a directory that other processes might also write to concurrently
should not rely on `Rollback`'s directory cleanup being safe against that
kind of interleaving.

## Diagnosable without leakage

`Journal.String()` never includes file content at all — only paths,
existed-before flags, and outcomes (`written`, `deleted`, `noop`) — so it
is always safe to log, print, or embed in a provenance/handoff artifact.
The handful of error paths that do include a content preview for diagnosis
(an `ExpectedPriorContent` mismatch, a `Verify` drift report) redact the
content first:

- The full, untruncated content is scanned against a locally copied subset
  of `provenance`'s secret-shaped patterns (AWS secret keys, PEM private
  key blocks, GitHub token prefixes, generic key/token/password/secret
  assignments, JWT-shaped strings) — mirroring `provenance.go` and
  `contract/secrets.go`'s own copies, per this repository's convention of
  duplicating this short regex list across small leaf packages rather than
  exporting it from `provenance` or sharing an internal package.
- Only **after** redaction is the result truncated to a bounded preview
  length. This order matters: truncating first could cut a matchable
  secret shape in half (e.g. a GitHub token whose length requirement falls
  past the truncation point), leaving a remnant that no longer matches any
  pattern and would slip through unredacted. Redacting the complete
  content first closes that gap.

This is a best-effort safety net, matching `provenance`/`contract`'s own
framing exactly: it catches common, recognizable secret shapes, but it can
both miss secrets in unrecognized formats and over-redact ordinary text
that happens to match a pattern. It is not a guarantee, and it does not
change the underlying advice in
[`agent-safety-policy.md`](agent-safety-policy.md): don't put secrets into
files an agent is likely to touch in the first place.

## Path safety

Every path is validated before `Apply` does anything with the filesystem.
Validation rejects, and never silently sanitizes:

- an empty path;
- an absolute path;
- a path containing a `..` component (checked after `filepath.Clean`, so
  every traversal shape collapses to a detectable leading `..`);
- a path that cleans to `.` (referring to `targetDir` itself);
- a path whose nearest **existing** ancestor directory resolves, via
  `filepath.EvalSymlinks`, to somewhere outside `targetDir` — this catches
  a symlink planted anywhere in the existing portion of the path that
  would otherwise let a write escape `targetDir`, even though the target
  file itself may not exist yet (which is the normal case for a new file
  in a patch).

This is treated with the same seriousness as a shell-injection
vulnerability, for the filesystem instead of a shell: a batch with any
path-safety violation is rejected in full, before a single byte is
written anywhere — including for the other, legitimate changes in the
same batch.

## Example: a successful apply, a rejected apply, and a rollback

```go
package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"
    "time"

    "github.com/mediusfy/modulex/approval"
    "github.com/mediusfy/modulex/patchapply"
)

func main() {
    targetDir, _ := os.MkdirTemp("", "example")
    readme := filepath.Join(targetDir, "README.md")
    _ = os.WriteFile(readme, []byte("# Old title\n"), 0o644)

    baseline, _ := os.ReadFile(readme)

    // 1. A successful apply: update README.md, add a new file.
    journal, err := patchapply.Apply(targetDir, []patchapply.FileChange{
        {Path: "README.md", NewContent: []byte("# New title\n"), ExpectedPriorContent: baseline},
        {Path: "NOTES.md", NewContent: []byte("scratch notes\n")},
    }, patchapply.ApplyOptions{})
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("applied:", journal)

    // 2. A rejected apply: someone edited README.md since baseline was
    // captured, so this second Apply call detects drift and changes
    // nothing.
    _ = os.WriteFile(readme, []byte("# New title\nSomeone's edit.\n"), 0o644)
    _, err = patchapply.Apply(targetDir, []patchapply.FileChange{
        {Path: "README.md", NewContent: []byte("# Yet another title\n"), ExpectedPriorContent: baseline},
    }, patchapply.ApplyOptions{})
    fmt.Println("rejected as expected:", err) // ErrPriorContentMismatch

    // 3. Roll back the FIRST (successful) apply, and confirm it worked.
    if err := patchapply.Rollback(targetDir, journal); err != nil {
        log.Fatal(err)
    }
    if err := patchapply.Verify(targetDir, journal); err != nil {
        log.Fatal(err)
    }
    fmt.Println("rolled back and verified clean")

    // A deletion still requires an approved broker/scope.
    b := approval.NewBroker()
    scope := approval.Scope{Action: "delete", Resource: "NOTES.md"}
    if _, err := b.Grant(scope, "drew@jocham.io", time.Minute); err != nil {
        log.Fatal(err)
    }
    if _, err := patchapply.Apply(targetDir, []patchapply.FileChange{
        {Path: "NOTES.md", Delete: true},
    }, patchapply.ApplyOptions{Broker: b, Scope: scope}); err != nil {
        log.Fatal(err)
    }
}
```

## Not yet wired into anything

This package is a standalone mechanism: no CLI, no MCP server, and no call
site in this repository invokes it yet. It operates on whatever
`targetDir` a caller gives it; **this package does not create git
worktrees, branches, or any other form of isolation itself** — a caller
that wants "isolated worktree" semantics (per ADR-0032's "The agent edits
in an isolated worktree or applies an explicit patch") is expected to set
that up by whatever means before calling `Apply`, and pass that resulting
path in as `targetDir`.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md) —
  P2's "Add atomic patch application and rollback journaling," and the
  "Safety and governance" section this package operationalizes.
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  human-facing policy this package makes checkable in code, including its
  "Isolated worktrees and dirty-state preservation" and "Dry runs and
  rollback" sections.
- [`docs/planning/agent-approval-broker-guide.md`](agent-approval-broker-guide.md) —
  `approval.Broker`/`approval.Scope`, which `ApplyOptions` consults for the
  deletion-requires-approval rule.
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  `provenance.Status`, reused for the approval-check result, and the
  secret-pattern detection this package's diagnostics locally mirror.
- Jira MOD-70: this package (`patchapply.Apply`, `patchapply.Rollback`,
  `patchapply.Verify`, `patchapply.Journal`).
- Jira MOD-69/66: `approval` and `provenance` — the sibling packages this
  one builds on without modifying.

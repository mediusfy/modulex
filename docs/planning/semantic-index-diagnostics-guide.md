# Semantic Index Diagnostics Guide

This guide covers the `semindex` package: root validation and diagnostics
for semantic code indexes (CodeGraph, TokenSave, and similar tools), per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P2: "Add CodeGraph/TokenSave
index-root validation and diagnostics" (Jira MOD-71). The ADR's "Safety and
governance" section requires that agents:

> verify that semantic indexes such as CodeGraph or TokenSave point at the
> active worktree before their results are trusted.

`semindex` is a standalone leaf package
(`github.com/mediusfy/modulex/semindex`) that does **not** import the core
`modulex` package, `discovery`, or `contract` — the same "small,
independent package" convention already used by `provenance`, `discovery`,
`contract`, and `verify`. It requires no third-party dependency: no SQLite
driver, no YAML/JSON library, nothing beyond the standard library.

## The problem this solves

A semantic index is usually built once, then queried repeatedly — often
from a long-lived process (an MCP server, an IDE plugin) that outlives any
single checkout. If that process is later asked a question from a
*different* git worktree of the same logical repository — a second clone,
a sibling worktree checked out for another branch, a directory that got
renamed or moved — the index can keep answering from stale, wrong-branch,
or wrong-repository state, with nothing about the response making that
obvious.

This is not hypothetical: it has been observed in practice, where a
running index tool answered a code-search query from within one checkout
using an index that had actually been built against a *different* checkout
of a different project on the same machine — a different branch's worth of
code, silently presented as if it applied to the checkout the query came
from. The tool itself detected and reported the mismatch (it happened to
know its own build root and the caller's working directory), but that
capability is tool-specific and not something every index or every agent
integration can rely on. `semindex` exists to give agents a general,
tool-agnostic way to catch this same class of problem, whether or not the
tool in question already does its own detection.

## What `semindex` cannot do

`semindex` cannot open `.codegraph/codegraph.db`, a hypothetical
`.tokensave/tokensave.db`, or any other third-party index's on-disk
format and inspect it directly. Those formats are undocumented, owned by
their respective tools, and offer no compatibility guarantee to this
package. Reverse-engineering them (or adding a SQLite driver just to peek
inside) would also violate the constraint that semantic-index tooling stay
out of Modulex's core dependency graph.

Instead, `semindex` defines a convention an index tool can *opt into*, and
otherwise reports "unverifiable" rather than guessing. See "The
marker-file convention" below for what that convention is, and "The
four-state model" for why "unverifiable" is a first-class outcome rather
than an edge case.

## Relationship to `discovery.IndexStatus`

`discovery.Discover` already reports whether a well-known index directory
(`.codegraph`, `.tokensave`) is *present* at a repository root
(`discovery.IndexStatus{Name, Present}` — see
[`agent-discovery-guide.md`](agent-discovery-guide.md)). `semindex` answers
a deeper question discovery does not attempt: given that a directory is
present, does it actually belong to *this* worktree?

`semindex` does not import `discovery` and does not require a
`discovery.Repository` as input — `Diagnose` works from a plain worktree
root and index directory path. A caller who already ran
`discovery.Discover` can feed one of its `Indexes[i].Name`/derived
directory values in; a caller who hasn't can call `Diagnose` directly.

## The marker-file convention

The convention: a plain-text file named `.modulex-index-root`
(`semindex.MarkerFileName`) inside the index's own directory (e.g.
`.codegraph/.modulex-index-root`), whose first non-empty line is the
**absolute path** the index was built against. No JSON, no YAML — a single
line of text, so there is no format to get wrong and nothing to parse
beyond `TrimSpace`.

```go
func MarkerFileReader(markerName string) RootReader
var DefaultMarkerReader RootReader // = MarkerFileReader(MarkerFileName)

func WriteMarkerFile(indexDir, markerName, root string) error
```

`WriteMarkerFile` is the convention's writer side: it creates `indexDir` if
needed and writes `root` as the marker file's contents. It is not required
by `Diagnose` — that only ever reads — but it's the simplest way for a
real tool (or a test) to adopt the convention.

**Nothing in the CodeGraph or TokenSave ecosystem writes this file today.**
Getting a real tool to adopt it is explicitly out of scope for this
package; that is future integration work. Until a tool adopts it,
`semindex` can only validate:

1. An index directory that *does* contain a `.modulex-index-root` marker
   (whether written by a tool that has adopted the convention, or by a
   test using `WriteMarkerFile`), via `DefaultMarkerReader`; or
2. Any index directory a caller wants to check via a custom `RootReader` —
   a closure the caller supplies that already knows how to extract that
   specific tool's actual root declaration (shelling out to a status
   command and parsing its output, reading a tool-specific file format,
   etc.). All such tool-specific logic lives in the caller's closure, never
   in this package.

Any index that does neither is reported as `StatusUnverifiable` — never as
a match.

## Core types

```go
type Status string

const (
    StatusOK           Status = "ok"
    StatusMismatch     Status = "mismatch"
    StatusMissing      Status = "missing"
    StatusUnverifiable Status = "unverifiable"
)

type IndexRoot struct {
    Name       string
    Dir        string
    Root       string
    Determined bool
}

type Diagnosis struct {
    Name         string
    Status       Status
    WorktreeRoot string
    IndexRoot    string
    Remediation  string
}

type RootReader func(indexDir string) (root string, ok bool, err error)

func ReadIndexRoot(name, dir string, reader RootReader) IndexRoot
func Diagnose(worktreeRoot, indexDir, name string, reader RootReader) Diagnosis
func ResolveWorktreeRoot(dir, fallbackRoot string) (string, error)
```

## The four-state model

`Diagnose` never collapses to a boolean:

| Status | Meaning |
|---|---|
| `StatusOK` | The index's declared root was determined and matches the active worktree. |
| `StatusMismatch` | The index's declared root was determined and does **not** match — a stale or wrong-repository index. |
| `StatusMissing` | No index directory was found at the given path at all. |
| `StatusUnverifiable` | An index directory exists but its declared root could not be determined (no marker file, no `RootReader`, or the `RootReader` reported `ok=false` or an error). |

`StatusUnverifiable` is deliberately distinct from both `StatusOK` and
`StatusMismatch`. A caller that only checked "is this a mismatch?" would
treat "I couldn't verify this" the same as "this is fine" — exactly the
false confidence this package exists to prevent. Any real third-party tool
that has not adopted the marker convention and has no custom `RootReader`
supplied will report `StatusUnverifiable` forever, and that's the correct,
honest answer, not a bug to work around.

## Worktree root resolution

```go
func ResolveWorktreeRoot(dir, fallbackRoot string) (string, error)
```

Prefers `git -C dir rev-parse --show-toplevel`; falls back to
`fallbackRoot` if git isn't on `PATH`, `dir` isn't inside a git working
tree, or the command fails or returns empty. `dir` is a single, discrete
`exec.Command` argument — never concatenated into a shell string — and is
expected to be a path the caller already trusts (typically the process's
own working directory), not arbitrary untrusted text. Nothing about an
index's declared root (which comes from a marker file or a caller-supplied
`RootReader`, and is therefore not something this package controls) ever
reaches a command line; it is only ever compared as a string, in
`Diagnose`.

Both `worktreeRoot` and the index's declared root are resolved via
`filepath.Abs` + `filepath.EvalSymlinks` before comparison, so platform
differences like `/tmp` vs. `/private/tmp` on macOS never produce a
false-positive mismatch.

## Contract-driven severity

```go
type Severity string

const (
    SeverityOK    Severity = "ok"
    SeverityWarn  Severity = "warn"
    SeverityBlock Severity = "block"
)

type Policy struct {
    TreatMismatchAsFailure     bool
    TreatMissingAsFailure      bool
    TreatUnverifiableAsFailure bool
}

func EvaluateSeverity(d Diagnosis, policy Policy) Severity
```

Per the ticket's "mismatch is a visible warning or failure according to
contract policy" requirement, whether a non-OK `Diagnosis` should merely
warn or should block a workflow is a policy decision, not something
`Diagnose` hardcodes. `EvaluateSeverity` is a separate, optional function —
not a parameter to `Diagnose` — so a caller who just wants a plain
diagnosis is never forced to think about severity at all.

`semindex` deliberately does **not** import
`github.com/mediusfy/modulex/contract` for this. Forcing every caller to
construct a full `contract.Contract` (parse YAML, populate required
fields, call `Validate`) just to get a yes/no severity decision would be a
far heavier dependency than the question warrants, and would pull
`contract` — and, transitively, `provenance` — into an otherwise
dependency-light leaf package. A caller that already has a
`contract.Contract` (for example, a future per-index-name policy entry in
the repository contract schema) is free to derive a `Policy` value from it
before calling `EvaluateSeverity`; this package only needs the resulting
yes/no decisions, never the schema they came from.

`Policy`'s zero value treats every non-OK status as `SeverityWarn` — the
safer default for a caller that hasn't made an explicit policy decision.
`TreatUnverifiableAsFailure` defaults to `false` in particular because a
real tool that simply hasn't adopted the marker convention yet would
otherwise always block, which would make incremental adoption
impractical.

## Usage example

```go
worktree, err := semindex.ResolveWorktreeRoot(".", "")
if err != nil {
    return err
}

d := semindex.Diagnose(worktree, ".tokensave", "tokensave", semindex.DefaultMarkerReader)

switch semindex.EvaluateSeverity(d, semindex.Policy{TreatMismatchAsFailure: true}) {
case semindex.SeverityBlock:
    return fmt.Errorf("tokensave index unusable: %s", d.Remediation)
case semindex.SeverityWarn:
    log.Printf("tokensave: %s", d.Remediation)
case semindex.SeverityOK:
    // proceed normally
}
```

For a real tool that hasn't adopted the marker-file convention, a caller
supplies its own `RootReader` instead of `DefaultMarkerReader` — for
example, a closure that shells out to that tool's own status command and
parses the root it reports, keeping all tool-specific parsing outside this
package:

```go
tokensaveReader := func(indexDir string) (string, bool, error) {
    // tool-specific: e.g. run the tool's own status command and parse
    // its reported root out of the response.
    return root, ok, err
}
d := semindex.Diagnose(worktree, ".tokensave", "tokensave", tokensaveReader)
```

## Explicitly out of scope

- No actual integration with real CodeGraph or TokenSave file formats —
  this package is the generic framework only.
- No CLI — a future `modulex agent` CLI step could call `Diagnose` for
  every index `discovery.Discover` finds present, but that wiring does not
  exist yet.
- No dependency on `contract.Contract` — see "Contract-driven severity"
  above.

# Agent Discovery and Command Classification Guide

This guide covers the `discovery` package: repository discovery and command
classification for an AI coding agent, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P0: "Add Modulex agent project
discovery and command classification" (Jira MOD-64), step 1 of the ADR's
"Standard agent workflow":

> `modulex agent discover` identifies the repository root, projects,
> modules, composition roots, instruction files, Make targets, CI
> workflows, and available indexes.

`discovery` is a standalone leaf package
(`github.com/mediusfy/modulex/discovery`) that does **not** import the core
`modulex` package — the same "small, independent package" convention
already used by `provenance`, `httpx`, `otel`, `chi`, and the messaging
adapters. It depends on `provenance` for the `CommandClass` enum only, so
command classification here and provenance/handoff records produced
elsewhere never drift into two competing vocabularies.

## Why discovery is its own read-only step

An agent pointed at an unfamiliar repository has to answer several
questions before it can safely do anything: which directories are Go
modules, where does the build get wired together, which instruction files
apply, what can `make` do, what CI already checks, and what's actually
available on this machine right now. Reconstructing that from scratch every
session is slow and error-prone. `discovery.Discover` answers all of it in
one pass, and does so without executing anything beyond a single read-only
`git status --porcelain` — it walks files, parses text, and checks `PATH`,
nothing more.

Per the ADR's "discovery works without global hooks" acceptance criterion,
`Discover` never reads `~/.claude`, `~/.kimi-code`, or any other
user-scoped or global state. Everything comes from the given root directory
and the process's `PATH`.

## What `Discover` returns

```go
func Discover(root string) (Repository, error)
```

`root` must be an existing directory, and is always supplied explicitly by
the caller — `Discover` never falls back to the process's current working
directory implicitly.

```go
type Repository struct {
    Root             string
    IsGitRepo        bool
    Dirty            bool
    Modules          []GoModule
    CompositionRoots []CompositionRoot
    InstructionFiles []InstructionFile
    MakeTargets      []string
    CIWorkflows      []string
    Indexes          []IndexStatus
    Tools            []ToolStatus
}
```

- **`Modules`** (`GoModule{Path, ModulePath}`) — every Go module found under
  `Root`, walked from `go.mod` files. See "Nested-module boundary
  behavior" below.
- **`CompositionRoots`** (`CompositionRoot{Path, Reason}`) — every direct
  child directory of `examples/` (unconditionally, even if it has no
  `func main()`), plus any directory anywhere under `Root` containing a Go
  file with a package-level `func main()`. `Reason` distinguishes the two:
  `"direct child directory of examples/"` vs. `"contains a func main()
  declaration"`.
- **`InstructionFiles`** (`InstructionFile{Path, Name}`) — every well-known
  agent-instruction file found anywhere in the tree: `AGENTS.md`,
  `CLAUDE.md`, `.cursorrules`, `copilot-instructions.md`. This is a fixed,
  deliberately non-exhaustive list; it reports what it finds and where, not
  a claim of completeness.
- **`MakeTargets`** — target names parsed from a `Makefile` at `Root`, from
  both `.PHONY:` lines and plain `target:` lines at column zero. A simple
  line-based parser, not a Makefile grammar: it does not execute `make` or
  understand pattern rules, multi-target lines, or line continuations.
  Lines containing `:=` (variable assignments, including the no-space
  `FOO:=bar` form) are skipped so `VERSION := v1.0.0` is never mistaken for
  a target.
- **`CIWorkflows`** — `*.yml`/`*.yaml` file names directly under
  `.github/workflows/`, if that directory exists.
- **`Indexes`** (`IndexStatus{Name, Present}`) — presence/absence of
  well-known semantic-index directories at `Root`: `.codegraph`, `.git`,
  `.tokensave`. Absence is reported explicitly, never omitted.
- **`Tools`** (`ToolStatus{Name, Present, Path}`) — presence/absence on
  `PATH` of `go`, `git`, `golangci-lint`, `gofmt`, `docker`, resolved with
  `exec.LookPath` only. `Discover` never executes any discovered binary.
  Like `Indexes`, a missing tool is always present in the result with
  `Present: false`, never silently dropped — see "Missing tools and
  services are always explicit" below.
- **`IsGitRepo` / `Dirty`** — see "Dirty-worktree detection" below.

Every slice field is sorted and never nil, so `json.Marshal(repo)` is
byte-identical across repeated calls against the same on-disk state — the
same discipline `provenance.Envelope` and the core module's
`Manager.Diagnostics`/`Manager.ModuleContract` follow (see
[`diagnostics-guide.md`](diagnostics-guide.md) and
[`provenance-handoff-schema.md`](provenance-handoff-schema.md)).

## Nested-module boundary behavior

Walking for `go.mod` files stops descending into a directory's subtree the
moment it finds a `go.mod` there. A nested module owns its own
subdirectories; they are never walked looking for a further nested module
as if it belonged to the parent scan. This mirrors how `go list ./...`
treats module boundaries.

Concretely, given:

```
go.mod                      (module example.com/root)
pkg/sub/go.mod               (module example.com/root/pkg/sub)
pkg/sub/deeper/go.mod        (module example.com/root/pkg/sub/deeper)
```

`Discover` reports exactly two modules — `.` and `pkg/sub` —
`pkg/sub/deeper` is **not** reported, because the walk stopped descending
as soon as it found `pkg/sub/go.mod`. In this repository, the equivalent
real structure is the root module plus `examples/external-consumer` and
`tools/modboundary`, each its own module with its own `go.mod`.

## Dirty-worktree detection

`IsGitRepo` is true iff `Root` contains a `.git` entry (directory or file —
the latter covers `git` worktrees). If there is no `.git` entry, `Discover`
reports `IsGitRepo: false` and `Dirty: false` — "not a git repository" is a
normal, expected outcome, never an error.

If a `.git` entry exists, `Discover` shells out to `git status --porcelain`
(preferred over hand-rolling git plumbing) and sets `Dirty` to whether it
produced any output. If that command cannot run — `git` missing from
`PATH`, or a non-functional `.git` directory such as a bare
`os.Mkdir(".git")` in a test fixture — `Discover` still reports
`IsGitRepo: true` (the entry is present) but leaves `Dirty` at its zero
value (`false`) rather than failing discovery as a whole.

## Missing tools and services are always explicit

Per the ADR's "missing tools and optional services are reported explicitly"
acceptance criterion, `Tools` and `Indexes` always contain one entry per
well-known name, whether present or not. A missing `golangci-lint` or an
absent `.codegraph/` shows up as `{Present: false}` in the result — it is
never omitted from the slice.

## Command classification

```go
type ClassificationRule struct {
    Pattern *regexp.Regexp
    Class   provenance.CommandClass
    Reason  string
}

var ClassificationRules []ClassificationRule

func ClassifyCommand(cmd string) (provenance.CommandClass, string)
```

`ClassifyCommand` matches `cmd` (a full command line, e.g. `"make release
VERSION=v0.2.0"`) against `ClassificationRules` in order and returns the
first match's `provenance.CommandClass` and a human-readable reason. The
rule table is built from
[`docs/planning/agent-safety-policy.md`](agent-safety-policy.md)'s
command-classification table, and reuses `provenance.CommandClass`
(`ClassSafe`, `ClassMutating`, `ClassNetworked`, `ClassDestructive`,
`ClassApprovalRequired`) rather than a parallel enum.

Rule table summary (first match wins):

| Command | Class | Why |
|---|---|---|
| `git status` / `diff` / `log` | Safe | read-only inspection |
| `git reset --hard` / `clean -f` / `branch -D` | Destructive | discards work or a branch irreversibly |
| `git push` / `git tag` | ApprovalRequired | external mutation |
| `git add` / `git commit` | Mutating | local index/history write |
| `go build` / `test` / `vet` | Safe | no side effects |
| `go mod download` | Networked | fetches from the module proxy |
| `make fmt` | Mutating | rewrites files in place (`gofmt -s -w .`) |
| `make deps` | Networked | `go mod download` |
| `make vuln` | Networked | fetches the govulncheck vulnerability database |
| `make release` | ApprovalRequired | tags and pushes a release |
| `make publish-godev` | ApprovalRequired | see tie-break below |
| `make build` / `test` / `test-arch` / `lint` / `help` / `check-*` | Safe | read-only build/test/verification targets |

### The `make publish-godev` tie-break

`make publish-godev` is genuinely both `ClassNetworked` (it curls
`proxy.golang.org`) and externally visible: asking the public module
proxy/`pkg.go.dev` to (re-)index a released version is a publishing action
with an outward effect, not a private read. `ClassifyCommand` resolves this
by giving `ClassApprovalRequired` precedence: **when a command is both
`ClassNetworked` and would trigger an externally-visible or
approval-gated action, `ClassApprovalRequired` wins.**

The reasoning: collapsing an ambiguous command to the weaker "networked"
classification would let an agent run it unattended on the theory that mere
network I/O is always fine, which defeats the point of having an
approval-required class at all. An approval gate must never be satisfiable
just because the command also happens to match a broader, cheaper class.
Any future rule that is genuinely both networked and approval-required
should follow this same precedence — see the inline comment above the
`make publish-godev` rule in `discovery/classify.go`.

### Fail-safe default for unrecognized commands

If no rule in `ClassificationRules` matches, `ClassifyCommand` returns
`provenance.ClassApprovalRequired`, **not** `ClassSafe`. An unrecognized
command must never silently pass through as though it had been vetted —
this mirrors `agent-safety-policy.md`'s "when in doubt about whether an
action is safe, an agent must treat it as requiring approval rather than
assuming permission."

## Example

```go
repo, err := discovery.Discover("/path/to/repo")
if err != nil {
    return err
}

if repo.Dirty {
    // preserve the human's in-progress work rather than assuming it is
    // disposable, per agent-safety-policy.md.
}

for _, cmd := range []string{"make fmt", "make release", "go test ./..."} {
    class, reason := discovery.ClassifyCommand(cmd)
    fmt.Printf("%-16s -> %-18s (%s)\n", cmd, class, reason)
}
```

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  human-facing policy this rule table operationalizes
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  the `provenance.CommandClass` enum reused here
- [`docs/planning/diagnostics-guide.md`](diagnostics-guide.md) — the
  sorted-slice/deterministic-JSON convention this package follows
- Jira MOD-62: the forthcoming `modulex.agent.yaml` repository contract,
  which discovery output is expected to feed
- Jira MOD-63: focused agent verification with explicit skipped statuses

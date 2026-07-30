# Agent Verification Guide

This guide covers the `verify` package: mapping changed files to focused
checks, and pairing them with this repository's always-required full gates,
per [ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P0: "Add focused agent verification
with explicit skipped statuses" (Jira MOD-63), step 5 of the ADR's "Standard
agent workflow":

> `modulex agent verify` runs focused checks followed by required
> repository gates and reports skipped checks separately from successful
> checks.

`verify` is a standalone leaf package (`github.com/mediusfy/modulex/verify`)
that does **not** import the core `modulex` package — the same "small,
independent package" convention already used by `provenance`, `discovery`,
`httpx`, `otel`, `chi`, and the messaging adapters. It depends on
`provenance` for `Status`, `VerificationCategory`, and `VerificationResult`
(so its output is exactly what a future `modulex agent handoff` would
consume, with no translation layer) and on `discovery` for
`discovery.ToolStatus` (so tool-availability gating reuses the same data
`discovery.Discover` already produces).

## Why focused and full are two mandatory outputs, not alternatives

An agent that only ran a fast, scoped check after touching one package could
convince itself a change is safe when it actually broke something elsewhere
— a renamed exported symbol, a changed shared type, a broken build tag. At
the same time, running the entire gate suite (`make build`, `test`,
`test-arch`, `lint`, and every boundary/compatibility/changelog script) for
every single-line change is slow enough that agents (and humans) are tempted
to skip it "just this once."

`verify.PlanFor` resolves this by never presenting focused checks and full
gates as a choice: its output, a `Plan`, always carries both:

```go
type Plan struct {
    FocusedChecks []CheckSpec // recommended based on changed files
    FullGates     []CheckSpec // always the complete fixed list, unconditionally
}
```

`Plan.FullGates` is **never** a function of `changedFiles` — it is always a
copy of the package-level `verify.FullGates` list, every time, for every
changeset, including an empty one. Nothing in this package's API lets a
caller substitute `FocusedChecks` for `FullGates`; per ADR-0032's acceptance
criterion "Full gates remain required before push or release," the full
gate set is the actual required gate, and focused checks are a
cheap-to-run, fast-feedback supplement to run *during* development, not a
shortcut past it.

## Why `PlanFor`, not `Plan`

The originating ticket sketched a function named `Plan` returning a type
also named `Plan`. Go does not allow a function and a type to share one
identifier in the same package, so the function here is `PlanFor`:

```go
func PlanFor(changedFiles []string) Plan
```

`PlanFor` takes only `changedFiles`, not a `discovery.Repository`: every
mapping rule below is derived from path shape alone (prefixes and
suffixes), never from repository contents, so no repository context is
needed to select focused checks. `Run` (below), by contrast, does need
`discovery.ToolStatus` data, because tool availability is an environment
fact `PlanFor` cannot know from paths alone.

## The focused-check rule table

`focusedChecksForFile` (unexported; `PlanFor` is the public entry point)
evaluates one changed path against a first-match-wins rule table, built for
this repository's actual layout:

| Changed path | Focused checks recommended |
|---|---|
| `.github/workflows/*.yml`/`.yaml` | **None.** See "CI workflow changes" below. |
| `scripts/check-*.sh` | Run that specific script directly (e.g. `./scripts/check-changelog.sh`). |
| `examples/**` | `go build ./examples/...`, plus `go test ./examples/<name>/...` when the path identifies a specific example directory. |
| `go.mod` / `go.sum` (root only) | `make check-api-compat`, `go mod verify`. |
| `CHANGELOG.md` | `make check-changelog` (trivially fast to re-check now, not just at full-gate time). |
| A `.go` file in a top-level package directory (e.g. `httpx/httpx.go`) | `go test ./<package>/...`, `go vet ./<package>/...`. |
| A `.go` file directly at the repository root (e.g. `modulex.go`) | `go test .`, `go vet .`. |
| Anything else | **The entire full gate set**, recommended as focused checks. See "Unmapped paths" below. |

`PlanFor` runs this rule for every path in `changedFiles`, concatenates the
results, removes exact duplicates (same category, name, and command), and
sorts the result by category then name — so the same changeset produces
byte-identical `FocusedChecks` regardless of the order the paths were given
in.

### CI workflow changes: no focused shortcut, ever

A change under `.github/workflows/` contributes **zero** entries to
`FocusedChecks`. This is intentional, not an oversight: CI configuration is
consequential enough that ADR-0032's guidance ("this always requires the
full gate set, never just a focused check") rules out any spot-check at
all. Since `Plan.FullGates` is present unconditionally regardless of what
`FocusedChecks` contains, this requirement is satisfied automatically —
there is nothing extra to add. A changeset consisting *only* of workflow
file changes therefore has `FocusedChecks == []` (empty, never nil) and
`FullGates` populated as always.

### Unmapped paths: fall back to the full gate set, never to nothing

If no rule above matches a changed path, `focusedChecksForFile` does **not**
return an empty result. Mirroring `discovery`'s fail-safe-to-approval-
required philosophy (see
[`agent-discovery-guide.md`](agent-discovery-guide.md)'s "Fail-safe default
for unrecognized commands"), an unmapped change must never silently produce
zero recommended checks. Instead, it returns the entire `FullGates` list,
relabeled as focused checks, each with a `Reason` naming the unmapped path:

```
no focused-check rule matched "docs/some-random-file.md"; falling back to
the full gate set out of caution rather than recommending nothing
```

This is a different case from the CI-workflow case above: workflow changes
are *deliberately* focused-check-free because a focused check is the wrong
tool for that change, while an unmapped path falls back to the full gate
set because nothing else can vouch for it — the difference between "we
know this needs the full gates and nothing less" and "we don't know what
this needs, so assume the most, not the least."

### Known gaps

The rule table is a reasonable mapping for this repository's actual layout,
not an exhaustive or fully general solution:

- A changed file inside a nested Go module other than the root module
  (`examples/external-consumer/*.go`, `tools/modboundary/*.go`) is mapped
  the same way as a root-module package directory (`go test
  ./<dir>/...`), which is not necessarily a valid build target from the
  root module's perspective, since those directories are separate Go
  modules with their own `go.mod`. A future revision could consult
  `discovery.Repository.Modules` to find the nearest enclosing module and
  rewrite the command accordingly.
- The `examples/` rule identifies "the specific example's own tests" by
  taking the first path segment after `examples/`; it does not cross-check
  that segment against `discovery.Repository.CompositionRoots`.

## The full gate list

```go
var FullGates []CheckSpec
```

`FullGates` is the fixed, complete list of this repository's required gates
before push or release, per `AGENTS.md` ("`make test-arch`, `make build`,
`make lint`, and `make test` must all pass locally before pushing") plus the
boundary/compatibility/changelog scripts already wired into the `Makefile`:

| Gate | Command | `RequiredTool` |
|---|---|---|
| build | `make build` | `go` |
| test | `make test` | `go` |
| test-arch | `make test-arch` | `go` |
| lint | `make lint` | `golangci-lint` |
| check-consumer-boundary | `make check-consumer-boundary` | `go` |
| check-module-boundary | `make check-module-boundary` | `go` |
| check-api-compat | `make check-api-compat` | `go` |
| check-changelog | `make check-changelog` | `git` |

It is exported so a caller (CI, a future `modulex agent verify` CLI, or a
one-off script) can iterate the canonical list without hardcoding it
themselves. Treat it as read-only; `PlanFor` always hands back a defensive
copy in `Plan.FullGates`.

## The five-state model

`Run` produces one `provenance.VerificationResult` per input `CheckSpec`,
each carrying one of `provenance.Status`'s five explicit states — never a
bare pass/fail bool, and never inferred as success by omission:

| Status | Meaning | Triggered by |
|---|---|---|
| `StatusPass` | The check ran and exited 0. | Command actually executed, exit code 0. |
| `StatusFail` | The check ran and exited non-zero (or errored). | Command actually executed, exit code != 0. |
| `StatusSkipped` | Intentionally not run. | `CheckSpec.Networked` is true and the caller's `allowNetwork` is false. |
| `StatusUnavailable` | Could not be run. | `CheckSpec.RequiredTool` is set and that tool is absent from the `[]discovery.ToolStatus` passed in. |
| `StatusApprovalRequired` | Not produced by this package. | Reserved for a future `modulex agent verify`/`review` step that gates elevated actions; `verify.Run` never emits it. |

`StatusSkipped` and `StatusUnavailable` results always carry a non-empty
`Reason` (`provenance.VerificationResult.Reason`), matching
`provenance.Envelope.Validate`'s requirement that those two statuses never
appear without an explanation.

## Tool-availability and network-capability gating

```go
func Run(ctx context.Context, checks []CheckSpec, tools []discovery.ToolStatus, allowNetwork bool) []provenance.VerificationResult
```

Before attempting any check, `Run` checks two things, in order:

1. **Tool availability.** If `CheckSpec.RequiredTool` is non-empty, `Run`
   looks it up in `tools` (typically `discovery.Discover(root).Tools`). If
   the tool is missing or not marked `Present`, `Run` returns
   `StatusUnavailable` immediately — the check's `Command` is **never
   invoked**. This is the core guarantee behind "verification does not
   silently treat missing tools as success": a missing `golangci-lint`
   can never be confused with `make lint` having actually run and passed,
   even though `make lint`'s own shell logic would print a warning and
   exit 0 if invoked directly with `golangci-lint` absent.
2. **Network capability.** If `CheckSpec.Networked` is true and the
   caller's `allowNetwork` argument is false, `Run` returns
   `StatusSkipped` with a reason like `"networked check skipped: no
   network access in this environment"`. Real network-reachability
   detection is out of scope; `allowNetwork` is an explicit,
   caller-supplied capability flag, matching the ticket's guidance to
   "accept an explicit `allowNetwork` bool parameter ... rather than"
   attempting real detection.

Only if both checks pass does `Run` actually execute `CheckSpec.Command` via
`sh -c`, honoring `ctx` for cancellation. Exit code 0 maps to `StatusPass`;
anything else maps to `StatusFail`. Combined stdout+stderr is captured into
`VerificationResult.Message`, truncated to the last 4KB (`maxOutputBytes`)
if longer — long enough to see a failure, bounded so a runaway or verbose
command cannot make a result set grow without limit.

### Why these fields live on `CheckSpec`, not re-derived from `Command`

`CheckSpec.RequiredTool` and `CheckSpec.Networked` are set once, when a
`CheckSpec` is constructed (by `PlanFor`, or by a caller building one by
hand), rather than something `Run` re-parses or pattern-matches out of
`Command` strings. This keeps `Run` a generic executor that works for any
`CheckSpec` from any caller without needing to understand this repository's
specific command vocabulary, and keeps the "what does this check need"
decision in one place.

### The "never fewer than input" invariant

Every single `CheckSpec` passed to `Run` produces exactly one
`provenance.VerificationResult` — the length of `Run`'s return value always
equals `len(checks)`. Nothing can silently disappear from the result set,
regardless of how many checks are unavailable, skipped, passing, or
failing.

## Human-readable rendering

```go
func RenderText(results []provenance.VerificationResult) string
```

`RenderText` groups `results` by `Category`, and within each category prints
one line per result — status (upper-cased), name, duration, and reason (if
any) — followed by an indented `Message` block if present. This is the
"human-readable" counterpart to `results` itself already being
machine-readable (it is `[]provenance.VerificationResult`, already
JSON-marshalable as-is via the `provenance` package). The rendered text is
suitable for pasting directly into a PR comment or printing to a terminal.

## Usage example

```go
changed := []string{"httpx/httpx.go", "modulex.go", "docs/README.md"}
plan := verify.PlanFor(changed)

repo, err := discovery.Discover(".")
if err != nil {
    return err
}

focusedResults := verify.Run(ctx, plan.FocusedChecks, repo.Tools, false /* allowNetwork */)
fullResults := verify.Run(ctx, plan.FullGates, repo.Tools, false)

fmt.Println(verify.RenderText(append(focusedResults, fullResults...)))
```

Given `changed`, `plan.FocusedChecks` recommends `go test ./httpx/...`,
`go vet ./httpx/...` (from `httpx/httpx.go`), and `go test .`, `go vet .`
(from `modulex.go`); `docs/README.md` matches no rule and contributes
nothing extra here only because the other two files already produced
focused checks — an unmapped path only triggers the full-gate fallback for
itself, not for the whole changeset (see "Unmapped paths" above).
`plan.FullGates` always contains the complete eight-gate list regardless.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-discovery-guide.md`](agent-discovery-guide.md) — the
  `discovery.Repository`/`discovery.ToolStatus` data this package's tool
  gating consumes, and the fail-safe-default convention this package's
  unmapped-path fallback mirrors
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  the `provenance.Status`/`VerificationCategory`/`VerificationResult` types
  reused here
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  human-facing policy naming `make build`, `make lint`, `make test`, and
  `make test-arch` as required before handoff
- Jira MOD-62: the forthcoming `modulex.agent.yaml` repository contract
- Jira MOD-66: `provenance` package (`VerificationResult`, `Status`)
- Jira MOD-64: `discovery` package (`Repository.Tools`)

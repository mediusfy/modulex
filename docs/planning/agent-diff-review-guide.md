# Agent Diff Review Guide

This guide covers the `review` package: checking a changeset for boundary
violations, secret-shaped values, API compatibility breaks, and missing
changelog obligations, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P1: "Add diff review for
boundaries, secrets, API compatibility, and changelog obligations" (Jira
MOD-65), step 6 of the ADR's "Standard agent workflow":

> `modulex agent review` checks the diff for unexpected files, secrets,
> boundary violations, API compatibility changes, generated-file drift, and
> missing changelog or documentation updates.

`review` is a standalone leaf package (`github.com/mediusfy/modulex/review`)
that does **not** import the core `modulex` package — the same convention
already used by `provenance`, `discovery`, and `verify`. It depends on
`verify` for `CheckSpec`/`Run` (reusing the same tool-availability/network
gating and `sh -c` execution rather than duplicating it) and on `provenance`
for the `VerificationResult`/`VerificationCategory` types, so its output is
exactly what a future `modulex agent handoff` would consume, with no
translation layer.

## Why this package exists separately from `verify`

`verify.FullGates` already runs `check-consumer-boundary`,
`check-module-boundary`, `check-api-compat`, and `check-changelog` — but
every one of those entries carries `Category provenance.VerificationFull`,
because `verify`'s job is "what must pass before push or release," not
"what does this specific diff need reviewed."

`provenance.VerificationCategory` separately defines `VerificationBoundary`,
`VerificationCompatibility`, `VerificationSecretScan`, and
`VerificationChangelog` precisely for this package's use.
`review.Checks` re-declares the same four make targets (minus the secret
scan, which is not a make target — see below) with the category that
actually describes what each one reviews, so a caller building a diff-review
report (or a `provenance.Envelope`) can group and label results correctly
without `review` and `verify` producing conflicting categories for the same
underlying command.

| Check | Command | Category |
|---|---|---|
| `check-consumer-boundary` | `make check-consumer-boundary` | `VerificationBoundary` |
| `check-module-boundary` | `make check-module-boundary` | `VerificationBoundary` |
| `check-api-compat` | `make check-api-compat` | `VerificationCompatibility` |
| `check-changelog` | `make check-changelog` | `VerificationChangelog` |
| `check-secrets` (`ScanSecrets`, not a make target) | — | `VerificationSecretScan` |
| `check-protected-paths` (`CheckProtectedPaths`, not a make target) | — | `VerificationProtectedPaths` |

## Boundary and compatibility checks are not diff-scoped

`check-consumer-boundary` and `check-module-boundary` inspect the working
tree as it stands, not a `baseRef..headRef` diff — this matches how they
already run in CI (unconditionally, on every push/PR) and in
`verify.FullGates`. `check-api-compat` compares the working tree against the
latest git tag, not against `baseRef`. Only the secret scan is genuinely
diff-scoped, because scanning the entire repository for secret-shaped
strings on every review would flag pre-existing content unrelated to this
change and make the check impossible to act on.

## The diff-native secret scan

```go
func ScanSecrets(ctx context.Context, baseRef, headRef string) provenance.VerificationResult
```

`ScanSecrets` computes `git diff --unified=0 baseRef...headRef` (the same
triple-dot, merge-base-relative diff scope
`scripts/check-changelog.sh` uses), parses only the **added** (`+`) lines,
and runs each one through `redactLine` (unexported), which combines two
pattern sets:

1. `provenance.RedactHighConfidenceSecrets` — the precise, low-noise
   patterns (AWS credential env vars, PEM key blocks, GitHub token
   prefixes, JWT-shaped strings) from `provenance.RedactSecrets`, with its
   loose generic `key=`/`token=`/`password=`/`secret=` catch-all excluded.
2. `strictGenericSecretPattern` (`review/secrets.go`) — this package's own,
   source-code-tuned replacement for that excluded catch-all: it requires
   the assigned value to be a **quoted string literal** immediately
   following the operator (`:=`, `=`, or `:`), not a bare identifier, typed
   constant, or function call.

### Why the generic pattern needed its own, stricter variant

Smoke-testing an earlier version of this scan (using `provenance.RedactSecrets`
directly, generic catch-all included) against 30 commits of this
repository's own history produced 29 findings — a firehose of false
positives, all from provenance's generic pattern matching ordinary Go code
that merely mentions "key"/"token"/"password"/"secret":

```
token = hex.EncodeToString(buf)                              // unquoted expression
var ServiceKey = modulex.NewKey[Sender]("notification.Service") // unquoted, compound "Key" identifier
&secretService{APIKey: secretValue}                           // unquoted identifier reference
// Wrong scope with the right token: denied.                  // narrative comment
```

None of these assign a quoted literal, so `strictGenericSecretPattern`
(which does require one, including Go's `:=` short declaration — e.g.
`apiKey := "..."` — matched explicitly, not just a bare `:` or `=`) rules
all of them out. Re-running the same 30-commit smoke test after this change
dropped the count from 29 to 13, and every remaining hit is a legitimate
residual case: a test fixture in `provenance_test.go`/`approval_test.go`/
`contract_test.go` that intentionally uses a fake-but-secret-shaped literal
to assert the redaction machinery itself works, or a doc comment
illustrating the AWS pattern. See `TestScanSecrets_IgnoresUnquotedCodeAssignments`.

This is a deliberate precision-over-recall trade for the generic catch-all
specifically: `provenance`'s original, looser version remains appropriate
for its own use case (free-text command output, where an unquoted
`KEY=value` shell/env-style assignment is the norm — very different from
scanning Go source). The high-confidence, format-specific patterns (AWS,
PEM, GitHub, JWT) are unaffected either way — those are precise enough
already that quote-requiring them would only lose real recall for no
precision gain.

### The `nosecret` escape hatch

A line containing the case-insensitive substring `nosecret` anywhere is
never flagged, regardless of pattern matches — mirroring the `#nosec`/
`// nolint` convention other static-analysis tools in this ecosystem use:

```go
token := "fake-test-token-value" // nosecret: test fixture
```

This exists for the residual case pattern-tightening cannot resolve on its
own: a line that is secret-shaped **on purpose** (a test fixture asserting
redaction behavior, a doc line illustrating a pattern), which no regex can
distinguish from a real accidental secret by shape alone. See
`TestScanSecrets_NosecretMarkerSuppressesFinding`.

Like every command this repository's diff-review tooling runs, `ScanSecrets`
operates relative to the process's current working directory rather than
taking an explicit repository-root parameter — the same assumption
`verify.Run`'s `sh -c` commands make (the caller is expected to already be
running from the repository root, as an agent or CI job normally would be).

### Only added lines are scanned, and only redacted text is ever reported

Two properties make this check safe to run habitually and safe to display
in a PR comment or CI log:

1. **Diff-scoped.** A secret-shaped string already present at `baseRef` and
   left untouched is never flagged — only lines the diff actually
   introduces are scanned. See
   `TestScanSecrets_IgnoresPreexistingSecretOutsideDiff`.
2. **Redacted findings.** A finding's `Message` never contains the raw
   matched value: every reported line is passed through `redactLine`
   before being included, so only the redacted form (with `[REDACTED]` in
   place of the secret-shaped substring) ever appears. See
   `TestScanSecrets_FindsSecretAddedLine`.

Findings are capped at 20 (`maxSecretFindings`) per result, with the
remainder summarized as a count — mirroring `verify/run.go`'s
`maxOutputBytes` truncation discipline, for the same reason: a diff with an
unusually large number of matches (e.g. an accidentally committed key file)
must not make the result grow without bound.

### Failure modes

| Outcome | `Status` | When |
|---|---|---|
| No secret-shaped lines added | `StatusPass` | — |
| One or more secret-shaped lines added | `StatusFail` | `Message` lists each, redacted, as `<file>:<line>: <redacted text>` |
| `git diff` itself fails | `StatusUnavailable` | `baseRef`/`headRef` does not exist, or the working directory is not a git repository; `Reason` names the underlying error |

`StatusUnavailable` — not `StatusFail` — is deliberate: an inability to
compute the diff is not evidence either way about whether a secret was
added, mirroring `verify.Run`'s existing rule that a missing dependency is
never confused with an actual failure.

## Protected paths

```go
func ChangedFiles(ctx context.Context, dir, baseRef, headRef string) ([]string, error)
func CheckProtectedPaths(ctx context.Context, dir, baseRef, headRef string, protectedPaths []string) provenance.VerificationResult
```

`contract.Contract.ProtectedPaths` (`modulex.agent.yaml`'s `protected_paths`
list) was, until `CheckProtectedPaths` (`review/protectedpaths.go`), schema
and documentation only: `contract.RenderText` and `agentdocs.Generate` both
render a contract's protected paths, but nothing checked a real diff
against them — nothing stood between an agent and editing `go.mod` or a CI
workflow file except `CODEOWNERS` (a GitHub-side, PR-review-only control,
not something a local diff review can rely on).

`ChangedFiles` is `git diff --name-only` over the same `baseRef...headRef`
triple-dot range `ScanSecrets` uses, exported so `find_affected_modules`
(see `docs/planning/agent-mcp-server-guide.md`) can reuse it rather than
shelling out to git a second time. `CheckProtectedPaths` matches every
changed file against every pattern in `protectedPaths` with `path.Match`
(not `filepath.Match` — paths from `git diff --name-only` are always
`/`-separated regardless of host OS), which gives `*` the same
single-path-segment glob semantics `modulex.agent.yaml`'s own examples
assume (e.g. `.github/workflows/*.yml`). `contract.Contract.Validate`
rejects a malformed `protected_paths` pattern (`path.ErrBadPattern`) at
contract-load time, so a typo like an unmatched `[` is normally caught
there — but a caller can still read and use `ProtectedPaths` from a
contract that failed `Validate` for an unrelated reason (see
`tools/mcpserver`'s `reviewDiff` doc comment), so `Validate` alone does not
guarantee every caller catches the typo. `CheckProtectedPaths` itself still
treats a malformed pattern as "never matches" rather than failing the whole
check (`path.ErrBadPattern`), as a defense-in-depth fallback, not a primary
detection mechanism — a caller that cares about surfacing the typo, such as
`reviewDiff`, re-validates each pattern itself, drops the malformed ones
before calling `Review`, and reports a `StatusFail` result naming them.

| Outcome | `Status` | When |
|---|---|---|
| `protectedPaths` is empty | `StatusPass` | No contract, or a contract declaring no protected paths — a normal state, not a finding |
| No changed file matches any pattern | `StatusPass` | — |
| One or more changed files match | `StatusFail` | `Message` lists each `<file> matches protected path <pattern>` |
| `git diff` itself fails | `StatusUnavailable` | Same failure mode as `ScanSecrets` |

### `CHANGELOG.md` and `go.mod` carry file-scoped exceptions

`agent-safety-policy.md`'s protected-paths list is narrower than "any
change" for two of its six entries, and `CheckProtectedPaths` matches that
narrower scope by inspecting diff content, not just the changed-file list,
for exactly these two well-known file names:

- **`CHANGELOG.md`**: adding to `## [Unreleased]` is "expected and
  encouraged"; only already-released version-section boundaries are
  protected. `changelogEditIsWithinUnreleased` finds the `## [Unreleased]`
  section's line range in both the base and head content (via `git show
  <ref>:CHANGELOG.md`) and checks every diff hunk stays inside it on both
  sides — a hunk that touches an already-released section, or that inserts
  a new version header (a release cut), still counts as a hit.
- **`go.mod`**: only `retract` directives (and published tags, which are
  not part of a source diff) are protected; an ordinary `require`/version
  edit is not. `goModEditTouchesOnlyNonRetractLines` finds every `retract`
  directive's line range (a single `retract vX.Y.Z` line, or a
  parenthesized `retract ( ... )` block) in both base and head content, and
  checks no diff hunk overlaps one on either side.

Both functions are fail-safe: if `CHANGELOG.md`/`go.mod` can't be read at
either ref, or the single-file diff itself fails, the exception does not
apply and the file-level match stands as a hit — an inability to prove the
exception applies is never treated as the exception applying. Every other
`protectedPaths` entry (`.github/workflows/*.yml`, `SECURITY.md`,
`CODEOWNERS`, ...) keeps plain "any change to this path is a hit" matching.

`review.Review`'s `protectedPaths` parameter is `[]string`, not a
`contract.Contract`, so this package stays decoupled from `contract`'s
YAML/validation concerns — the same reason `Review` already takes
`tools []discovery.ToolStatus` rather than importing `discovery` to compute
it. The caller (`tools/mcpserver`'s `review_diff`, or a future CLI) is
responsible for reading `modulex.agent.yaml` and extracting
`ProtectedPaths` before calling `Review`.

## The boundary/compatibility/changelog checks

```go
var Checks []verify.CheckSpec
```

`review.Checks` is exported so a caller can iterate the canonical list
without hardcoding it themselves, mirroring `verify.FullGates`. It is
executed via `verify.Run`, inheriting its tool-availability and
`allowNetwork` gating, `sh -c` execution, and output truncation exactly —
see [`agent-verification-guide.md`](agent-verification-guide.md) for those
mechanics in full; this package does not re-implement or re-document them.

## `Review`: the combined entry point

```go
func Review(ctx context.Context, dir, baseRef, headRef string, tools []discovery.ToolStatus, allowNetwork bool, protectedPaths []string) []provenance.VerificationResult
```

`Review` runs `Checks` via `verify.Run(ctx, Checks, tools, allowNetwork)`
(each `CheckSpec.Dir` set to `dir`) and appends `ScanSecrets(ctx, dir,
baseRef, headRef)` and `CheckProtectedPaths(ctx, dir, baseRef, headRef,
protectedPaths)`, returning `len(Checks) + 2` results, always in that fixed
order (`Checks` order, then the secret scan, then the protected-paths
check). `dir` is the repository root every check, the secret scan's git
diff, and the protected-paths check's git diff all run against; an empty
`dir` leaves every command's working directory unset (the calling
process's own cwd). `tools` should be `discovery.Discover`'s `Tools` field
(or an equivalent slice); `allowNetwork` is accepted for symmetry with
`verify.Run` and forward compatibility, though none of `Checks` is
currently `Networked`. `protectedPaths` should be `contract.Contract.
ProtectedPaths`, if the caller has a `modulex.agent.yaml` to read one from
— see "Protected paths" above.

Because the result is `[]provenance.VerificationResult` — the same type
`verify.Run` produces — `verify.RenderText(results)` renders it as a
human-readable, per-category summary directly; `review` does not need (and
does not define) its own renderer.

## Usage example

```go
repo, err := discovery.Discover(".")
if err != nil {
    return err
}

var protectedPaths []string
if data, err := os.ReadFile("modulex.agent.yaml"); err == nil {
    var c contract.Contract
    if yaml.Unmarshal(data, &c) == nil {
        protectedPaths = c.ProtectedPaths
    }
}

results := review.Review(ctx, ".", "origin/main", "HEAD", repo.Tools, false /* allowNetwork */, protectedPaths)
fmt.Println(verify.RenderText(results))

for _, r := range results {
    if r.Status == provenance.StatusFail {
        // surface as a required-fix finding before handoff
    }
}
```

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-verification-guide.md`](agent-verification-guide.md) —
  the `verify.CheckSpec`/`Run`/`RenderText` machinery this package reuses
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  the `provenance.Status`/`VerificationCategory`/`VerificationResult` types
  reused here, and `provenance.RedactHighConfidenceSecrets`'s underlying
  pattern set
- [`docs/planning/agent-discovery-guide.md`](agent-discovery-guide.md) — the
  `discovery.Repository.Tools` data this package's tool gating (via
  `verify.Run`) consumes
- Jira MOD-63: `verify` package (`CheckSpec`, `Run`, `RenderText`)
- Jira MOD-66: `provenance` package (`VerificationResult`, `Status`,
  `RedactSecrets`, `RedactHighConfidenceSecrets`)

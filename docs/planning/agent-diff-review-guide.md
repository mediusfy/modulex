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
func Review(ctx context.Context, baseRef, headRef string, tools []discovery.ToolStatus, allowNetwork bool) []provenance.VerificationResult
```

`Review` runs `Checks` via `verify.Run(ctx, Checks, tools, allowNetwork)`
and appends `ScanSecrets(ctx, baseRef, headRef)`, returning
`len(Checks) + 1` results, always in that fixed order (`Checks` order, then
the secret scan). `tools` should be `discovery.Discover`'s `Tools` field (or
an equivalent slice); `allowNetwork` is accepted for symmetry with
`verify.Run` and forward compatibility, though none of `Checks` is
currently `Networked`.

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

results := review.Review(ctx, "origin/main", "HEAD", repo.Tools, false /* allowNetwork */)
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

# Agent Repository Contract Guide

This guide covers the `contract` package: a versioned schema for
`modulex.agent.yaml`, a repository's declared agent contract, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P0: "Define and validate the
Modulex agent repository contract" (Jira MOD-62). The ADR's "Canonical
repository contract" section is the source of truth for what such a
contract should declare:

> Repositories may provide a versioned `modulex.agent.yaml` contract. It
> should describe:
>
> - projects, Go modules, composition roots, and relevant source paths;
> - applicable instruction files and precedence rules;
> - lifecycle and module boundaries;
> - safe, mutating, networked, destructive, and approval-required
>   commands;
> - focused and repository-wide verification commands;
> - generated and protected paths;
> - required tools and optional services;
> - secret and credential requirements without storing secret values;
> - expected artifacts, reports, and handoff format.

`contract` is a standalone leaf package
(`github.com/mediusfy/modulex/contract`) that does **not** import the core
`modulex` package, `discovery`, or `verify` — the same "small, independent
package" convention already used by `provenance`, `discovery`, and
`verify`. It depends on `provenance` for `provenance.CommandClass` only, so
command classification stays consistent with the provenance/handoff schema
and `discovery.ClassifyCommand`'s rule table rather than inventing a
parallel enum.

## A declaration, not a derivation

`contract.Contract` is a human/agent-authored declaration of what *should*
be true about a repository, checked in as `modulex.agent.yaml`. This is
deliberately different from `discovery.Discover`, which *derives* similar
facts (Go modules, composition roots, instruction files) by scanning the
filesystem at runtime:

| | `contract.Contract` | `discovery.Repository` |
|---|---|---|
| Source | Hand-written YAML, checked into the repo | Computed by walking the filesystem and checking `PATH` |
| Answers | "What is this repository's policy?" | "What does this repository currently look like?" |
| Can drift from reality | Yes — a human must keep it in sync | No — always reflects the current working tree |

Neither package depends on the other. A future integration (out of scope
for this ticket) might compare the two — e.g. flagging a `Contract` project
whose `Path` no longer has a `go.mod` — but `contract` itself never reads
the filesystem beyond `os.ReadFile`/`yaml.Unmarshal` on a document a caller
hands it.

## Versioning

```go
const contract.SchemaVersion = "1.0.0"
```

Same rationale as `provenance.SchemaVersion`: a plain semver string, not a
Go module path or API version, because the schema is consumed as data —
potentially by non-Go tooling (a future `modulex agent` CLI, editor
integrations, CI steps) with no notion of Go module compatibility rules.
Bump the minor version for backward-compatible additions (new optional
fields), the major version for breaking changes (renamed, removed, or
retyped fields), and document every schema change in `CHANGELOG.md`.

## Schema fields

```go
type Contract struct {
    SchemaVersion       string
    Projects            []Project
    Instructions        InstructionPrecedence
    Boundaries          []Boundary
    Commands            []CommandDecl
    Verification        VerificationDecl
    ProtectedPaths       []string
    GeneratedPaths       []string
    RequiredTools        []string
    OptionalServices     []OptionalService
    RequiredCredentials  []string
    HandoffFormat        string
}
```

| Field | ADR-0032 item | Meaning |
|---|---|---|
| `SchemaVersion` | — | The schema version this document was written against. |
| `Projects` | "projects, Go modules, composition roots, and relevant source paths" | One entry per project: `Name`, `Path`, `ModulePath`, `Description`, `CompositionRoots`. A contract with zero projects is rejected as meaningless. |
| `Instructions` | "applicable instruction files and precedence rules" | `Files` (each an `InstructionFile{Path, Priority, Notes}`, lower `Priority` wins) plus a free-text `Rule` for anything precedence-by-number doesn't capture. |
| `Boundaries` | "lifecycle and module boundaries" | Named boundaries (`Name`, `Description`, `Paths`, `Rule`) describing what must not cross what, and how it's enforced. |
| `Commands` | "safe, mutating, networked, destructive, and approval-required commands" | `CommandDecl{Name, Command, Class, Reason}`, where `Class` is `provenance.CommandClass`. |
| `Verification` | "focused and repository-wide verification commands" | `VerificationDecl{Focused, Full}`, each a `[]CheckDecl{Name, Command, Reason, RequiredTool, Networked}` — mirrors `verify.Plan`'s split (see "Relationship to `verify.CheckSpec`" below). |
| `ProtectedPaths` | "...protected paths" | Paths agents must not modify without explicit human approval (see `agent-safety-policy.md`). Enforced by `review.CheckProtectedPaths`, wired through `review_diff` via `read_contract`. |
| `GeneratedPaths` | "generated...paths" | Paths that are machine-generated and should not be hand-edited. |
| `RequiredTools` | "required tools" | Binary names the declared commands/checks depend on (e.g. `"go"`, `"golangci-lint"`). |
| `OptionalServices` | "...optional services" | `OptionalService{Name, Description}` — external services that are nice-to-have, never a hard requirement. |
| `RequiredCredentials` | "secret and credential requirements without storing secret values" | **Names only** (e.g. `"GITHUB_TOKEN"`) — never values. See "Secrets never belong here" below. |
| `HandoffFormat` | "expected artifacts, reports, and handoff format" | A reference to a format by name (e.g. `"provenance.Envelope v1.0.0"`), not a duplicate of that schema. |

## Relationship to `verify.CheckSpec`

`CheckDecl` is deliberately shaped so it could plausibly be converted
to/from `verify.CheckSpec` by a future integration:

```go
// contract.CheckDecl
type CheckDecl struct {
    Name         string
    Command      string
    Reason       string
    RequiredTool string
    Networked    bool
}

// verify.CheckSpec (unchanged by this ticket)
type CheckSpec struct {
    Name         string
    Command      string
    Category     provenance.VerificationCategory
    Reason       string
    RequiredTool string
    Networked    bool
}
```

The only field `CheckSpec` has that `CheckDecl` doesn't is `Category`
(`provenance.VerificationFocused` / `provenance.VerificationFull`) — in
`VerificationDecl`, that distinction is implied by which of the two slices
(`Focused` or `Full`) a `CheckDecl` appears in, rather than repeated on
every entry. `contract` does not import `verify` to make this conversion
real; a data-schema package importing an executor package would be a
layering smell (the same reason `discovery` and `verify` only depend on
`provenance`, never on each other or on `modulex`).

## Command strings are data, not sanitized shell input

`CommandDecl.Command` and `CheckDecl.Command` store shell command lines as
plain strings (e.g. `"make test"`). **This package never executes them.**

The immediately preceding ticket in this ADR-0032 family (MOD-63, the
`verify` package) shipped a bug where a changed-file path was interpolated
directly into a shell command string executed via `sh -c`, and a crafted
path could run arbitrary commands — found by manual review, not by tests.
`verify.isPathSafeForCommand` fixed it by allow-listing the character set
of any value it interpolates into a `Command` before an executor touches
it.

This package has no executor, so it cannot itself repeat that bug — but it
does not guarantee its `Command` values are safe to further interpolate
either. **Any consumer that executes a declared `Command`, or builds a new
command string by appending untrusted input (a changed-file path, a
version argument, anything from outside the contract file itself) to one,
must apply the same allow-list discipline `verify.isPathSafeForCommand`
uses before doing so.** Storing a command as a string in this schema is
not an implicit safety guarantee about how it may later be used.

## Validation guarantees and their limits

```go
func (c *Contract) Validate() error
```

`Validate` returns a single error built with `errors.Join`, naming every
problem found (not just the first), or `nil` if `c` is valid.

**Structural checks:**

- `SchemaVersion` is required (non-empty).
- At least one `Project` is required — a contract describing zero
  projects is rejected as meaningless.
- Every `Project` must have a non-empty `Name` and `Path`.
- Every `CommandDecl.Class` must be one of `provenance.ClassSafe`,
  `ClassMutating`, `ClassNetworked`, `ClassDestructive`, or
  `ClassApprovalRequired`. Because `CommandClass` is a plain `string` type,
  `yaml.Unmarshal` happily accepts any string into it without error (e.g.
  `class: yolo` unmarshals just fine) — this check is what actually
  catches an unknown class, and it names both the bad value and the
  command it's on:

  ```
  commands[2] ("release"): unknown command class "yolo"; must be one of:
  safe, mutating, networked, destructive, approval_required
  ```

- Every `ProtectedPaths` entry must be a syntactically valid `path.Match`
  glob pattern. `review.CheckProtectedPaths` independently validates each
  pattern at review time and fails naming any malformed one (while still
  enforcing the valid ones), so this check and that one are two layers of
  the same defense: `Validate` catches the typo when the contract is
  loaded, `CheckProtectedPaths` when a diff is reviewed.

**Secret checks:** every free-text string field in the schema — project
names/paths/descriptions/composition-roots, instruction file paths/notes,
boundary descriptions/rules/paths, command names/commands/reasons, check
names/commands/reasons/required-tools, protected/generated paths, required
tools, optional-service names/descriptions, required-credential *names*,
and the handoff format — is scanned for secret-shaped values using a
pattern list mirroring `provenance.Envelope`'s redaction patterns (AWS
secret-key-shaped assignments, PEM private-key blocks, GitHub token
prefixes, generic `key=value`/`token:`/`password=`/`secret:` assignments,
and JWT-shaped strings). A match fails validation with an error naming the
field and the pattern category, e.g.:

```
instructions.files[0].notes: contains a value that looks like a secret
(GitHub token)
```

**This is a best-effort pattern match, not a guarantee** — the same
framing `provenance` uses for its own secret detection. It catches common,
recognizable secret shapes; it cannot catch every secret format, and it
can both miss secrets (false negatives) and flag non-secrets (false
positives, e.g. a `Reason` string that happens to contain the word
"secret" followed by a colon and a long token-like value). The only real
prevention is never putting a live credential into a checked-in
`modulex.agent.yaml` in the first place — see
[`agent-safety-policy.md`](agent-safety-policy.md).

### Why this package hard-fails instead of redacting

`provenance.Envelope` has a `Redact` method that rewrites secret-shaped
values to `"[REDACTED]"` in place, because an `Envelope` is an
ephemeral/generated artifact — redacting it before it's persisted is the
right move. `contract.Contract` has **no** `Redact`. A `modulex.agent.yaml`
is a file a human is meant to read and hand-edit; silently rewriting part
of it out from under them would be surprising and could mask the fact that
a real secret was ever there. `Validate` is the only defense here, and it
fails outright rather than "fixing" the document.

## Example: loading, validating, and rendering

```go
data, err := os.ReadFile("modulex.agent.yaml")
if err != nil {
    return err
}

var c contract.Contract
if err := yaml.Unmarshal(data, &c); err != nil {
    return fmt.Errorf("parsing modulex.agent.yaml: %w", err)
}

if err := c.Validate(); err != nil {
    return fmt.Errorf("modulex.agent.yaml is invalid: %w", err)
}

fmt.Println(contract.RenderText(c))
```

`modulex.agent.yaml` at the repository root is this repository's own real,
canonical contract — not just a worked example: its one Go module
(`github.com/mediusfy/modulex`), its five composition roots under
`examples/`, its instruction file (`AGENTS.md`), a representative command
per `CommandClass` value, its focused and full verification checks, its
protected and generated paths, required tools, optional services, and
`required_credentials: [GITHUB_TOKEN]` (name only, no value). It's what
`read_contract` and `modulex agent generate` (see `tools/agentcli`) both
read for this repository.

`contract/testdata/modulex.agent.example.yaml` mirrors it, so `contract`'s
and `agentdocs`' own tests can load a fixture with a plain relative path.
Loading and validating that fixture is itself a test
(`TestExampleContract_IsValid` in `contract/contract_test.go`), and a
second test (`TestRootContract_MatchesExample`) fails if the two files'
parsed content ever drifts apart — so the schema, the worked example, and
the real contract can never silently diverge.

## Human-readable rendering

```go
func RenderText(c Contract) string
```

`RenderText` produces a multi-line, human-readable summary — projects,
instruction precedence, boundaries, commands (grouped by class),
verification checks, protected/generated paths, required tools, optional
services, and required credential names — suitable for pasting into a PR
description or printing to a terminal. This is the "human-readable agent
guidance can be derived from the contract" piece of ADR-0032's acceptance
criteria, the same spirit as `verify.RenderText` applied to a contract's
shape instead of verification results.

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md)
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  human-facing policy this schema makes machine-readable and checkable;
  once `modulex.agent.yaml` is adopted project-wide, this document remains
  the canonical policy where the two disagree
- [`docs/planning/agent-discovery-guide.md`](agent-discovery-guide.md) —
  `discovery.Repository`, the derived (not declared) counterpart to this
  package's `Project`/`InstructionFile` concepts, and
  `discovery.ClassifyCommand`'s rule table, which classifies commands the
  same way `CommandDecl.Class` does
- [`docs/planning/agent-verification-guide.md`](agent-verification-guide.md) —
  `verify.CheckSpec`/`verify.Plan`, the executable counterpart `CheckDecl`/
  `VerificationDecl` are shaped to convert to/from
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  `provenance.CommandClass` (reused here) and the secret-detection pattern
  list this package's own patterns mirror
- Jira MOD-62: this package (`contract.Contract`, `Validate`, `RenderText`)
- Jira MOD-63: `verify` package (`CheckSpec`, `Plan`) — the security lesson
  (`isPathSafeForCommand`) referenced above
- Jira MOD-64: `discovery` package (`Repository`, `ClassifyCommand`)
- Jira MOD-67 (forthcoming): generating a portable agent instruction file
  from this contract — out of scope here

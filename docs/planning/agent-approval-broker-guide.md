# Agent Approval Broker Guide

This guide covers the `approval` package: a live, in-memory approval broker
for elevated agent actions, per
[ADR-0032](../adr/adr-0032-agent-first-development-experience.md)
("Agent-First Development Experience"), P2: "Add an approval broker for
elevated agent tools" (Jira MOD-69). The ADR's "Safety and governance"
section requires that the system:

> require human approval for push, release, deletion, infrastructure
> changes, database migrations, and external Jira/PR mutations

and [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md)'s
"Human approval boundary" section states the scope discipline this package
enforces in code, not just in prose:

> an approval covers the specific action and scope granted [...], not a
> standing blanket authorization for unrelated future actions

`approval` is a standalone leaf package (`github.com/mediusfy/modulex/approval`)
depending only on `provenance` and `contract` from this repository, and on
the standard library otherwise — the same "small, independent package"
convention already used by `provenance`, `discovery`, `verify`, and
`contract`. It uses `crypto/rand`, never `math/rand`, for token generation,
since these tokens are meant to be unguessable.

## Not yet wired into anything

**This is a design and mechanism, not an integration.** No CLI, no MCP
server, and no other call site in this repository invokes `approval` yet.
It is the trust boundary a future `modulex agent` CLI or MCP server is
expected to consult before actually running `git push`, `gh pr create`, a
Jira transition, a database migration, or an infrastructure change. Nothing
here reaches out to git, GitHub, Jira, or any other external system — it
only tracks and checks the *decision* of whether such an action is
currently authorized.

## Why this is not just `provenance.Approval`

`provenance.Approval` already exists as part of the handoff/provenance
schema:

```go
// provenance.Approval
type Approval struct {
    Action     string
    ApprovedBy string
    ApprovedAt time.Time
    Notes      string
}
```

It is a flat **record** of an approval that happened, meant to travel
inside a `provenance.Envelope` for audit continuity in a PR description or
CI artifact. Nothing consults it before an action runs, it has no scope
(just a free-text `Action` string), and it has no expiry — a `Notes` field
saying "approved for branch X" is a convention, not something anything
checks.

`approval.Grant` and `approval.Broker` are a different, more active thing:
a **live mechanism** a caller consults *before* running an elevated action.

| | `provenance.Approval` | `approval.Grant` / `approval.Broker` |
|---|---|---|
| What it is | A static record | A live, checkable authorization |
| Scope | Free-text `Action` only | `Scope{Action, Resource}`, matched on both fields |
| Expiry | None | Required, non-zero `ExpiresAt` |
| Reuse | N/A (it's just a record) | Single-use by default (see below) |
| Consulted before an action runs? | No | Yes — that's `Broker.Check`'s entire purpose |
| Where it lives | Inside a `provenance.Envelope`, JSON | In a `Broker`'s memory, checked live |

`Grant.ToProvenanceApproval()` converts a `Grant` into a
`provenance.Approval`, so a decision this package made can still be
recorded in a handoff envelope for audit continuity — the two schemas are
complementary, not competing.

## No elevated operation is approved by default

```go
b := approval.NewBroker()
```

`NewBroker` returns a `Broker` with **zero** grants. There is no
configuration, environment variable, or constructor argument that starts a
`Broker` in an already-approved state. Every `Broker.Check`/
`Broker.CheckToken`/`Broker.DryRunCheck`/`Broker.DryRunCheckToken` call
against a freshly constructed `Broker` returns
`provenance.StatusApprovalRequired` for any `Scope`, with no setup
required — `TestBroker_NewBrokerDeniesEverythingByDefault` in
`approval/broker_test.go` asserts exactly this across several representative
scopes.

## Scope: the unit of authorization

```go
type Scope struct {
    Action   string // e.g. "push", "release", "delete-branch"
    Resource string // e.g. a branch name, a PR number, "" for unscoped
}
```

**Both fields participate in every match.** A grant is never checked
against `Action` alone. Given a grant for `Scope{Action: "push", Resource:
"branch-a"}`:

- `Scope{Action: "push", Resource: "branch-b"}` — **denied** (same action,
  different resource).
- `Scope{Action: "delete", Resource: "branch-a"}` — **denied** (different
  action, same resource).
- `Scope{Action: "push", Resource: "branch-a"}` — **approved** (exact
  match).

An empty `Resource` is itself a specific value ("this action, unscoped to
any particular resource"), not a wildcard — it does not match a grant with
a non-empty `Resource` or vice versa. This is the concrete mechanism behind
the acceptance criterion "the design prevents approval reuse outside its
scope"; see `TestBroker_ScopeIsolation`.

## Grant: an approval that has actually been given

```go
type Grant struct {
    Token      string    // sensitive — see "Token sensitivity" below
    TokenHash  string    // safe to log; sha256(Token)
    Scope      Scope
    ApprovedBy string    // required — every grant must be attributable
    ApprovedAt time.Time
    ExpiresAt  time.Time // required, non-zero
    Used       bool
}
```

The **only supported way to create a `Grant`** is `Broker.Grant`:

```go
grant, err := b.Grant(
    approval.Scope{Action: "push", Resource: "release/v1.2.0"},
    "drew@jocham.io",
    10*time.Minute,
)
```

`Broker.Grant` rejects, with no partial effect:

- a non-positive `ttl` (a `Grant` is not allowed to exist without a real
  expiry);
- an empty `Scope.Action`;
- an empty `approvedBy`.

Building a `Grant{}` struct literal directly bypasses all of these checks
and is not a supported input to any `Broker` method — nothing in this
package accepts a hand-built `Grant`. As defense in depth against a `Grant`
reaching a `Broker`'s internal state some other way, every expiry check in
this package (`Grant.expired`) additionally treats a **zero-value
`ExpiresAt` as already expired**, never as "no expiry."

## Fail closed

`Broker.Check` (and `Broker.CheckToken`) has exactly **one** path that
returns approval (`provenance.StatusPass`): finding a matching, unexpired,
unused grant for the exact requested `Scope` while holding the broker's
internal lock. Every other path returns
`provenance.StatusApprovalRequired`:

- no grant exists at all;
- a grant exists for a different `Action`;
- a grant exists for a different `Resource`;
- the grant is expired;
- the grant was already consumed (`Used == true`);
- (for `CheckToken`) the presented token is empty or matches no stored
  grant.

There is no `default` branch, error return, or early exit anywhere in
`Broker.Check`/`Broker.CheckToken` that can produce anything other than
`StatusPass` or `StatusApprovalRequired` — see `broker.go`'s doc comments
and `approval/broker_test.go`'s adversarial tests (unknown scope, expired
grant, reused grant, mismatched scope, empty/forged token) for how this is
verified, not just asserted.

## Single-use grants (the decision, and why)

A `Grant` is **single-use**: the first `Broker.Check`/`Broker.CheckToken`
call that matches it marks it `Used`, and every subsequent check for that
same scope is denied — even before the grant's `ExpiresAt`.

This is the safer default for "prevents approval reuse outside its scope."
A multi-use grant would remain valid for repeated actions within its
`Scope` for its *entire* TTL, which widens what a single human approval
covers beyond what was explicitly granted (e.g. "approve one push to
branch-a" could otherwise be silently reinterpreted as "approve unlimited
pushes to branch-a for the next ten minutes"). A caller that legitimately
wants to authorize N actions should request N grants — one `Broker.Grant`
call per action — which also gives each action its own audit trail entry
(via `Grant.ToProvenanceApproval`).

Consumption is atomic under the broker's mutex: two goroutines racing to
`Check` the same single-use grant concurrently can never both succeed —
see `TestBroker_ConcurrentCheckOnlyOneWinnerForSingleUseGrant`, which is
run under `go test -race` by `make test-arch`.

## Dry runs

```go
func (b *Broker) DryRunCheck(scope Scope) provenance.Status
func (b *Broker) DryRunCheckToken(token string, scope Scope) provenance.Status
```

These answer "would this be approved right now" using the exact same
matching logic as `Check`/`CheckToken`, but never mark a matching grant as
`Used`. Use a dry run to preview a plan (e.g. "here is what would happen if
I ran `modulex agent verify` right now") without spending a single-use
grant that a real attempt will need. `TestBroker_DryRunNeverConsumes`
verifies a grant survives any number of dry runs and is still consumable
exactly once by a real `Check` afterward.

## Two ways to check: by scope, or by presented token

```go
func (b *Broker) Check(scope Scope) provenance.Status
func (b *Broker) CheckToken(token string, scope Scope) provenance.Status
```

`Check` searches every stored grant for one matching `scope`, regardless of
which token it was issued under — appropriate for a caller that already
trusts the broker's own bookkeeping (e.g. the same process that called
`Broker.Grant`).

`CheckToken` additionally requires the caller to present the *exact* token
a human was handed when the grant was created — the "explicit approval
token" flow the ADR calls for, e.g. a human runs an approval step, gets
back a token, and hands it to a separate tool invocation
(`--approval-token <token>`) that must present it to be authorized. An
empty token is always denied outright by `CheckToken` — it never silently
falls back to `Check`'s any-matching-grant search.

Both fail closed identically otherwise; from a caller's perspective, "not
approved" always looks the same (`provenance.StatusApprovalRequired`) no
matter which of the two paths, or which specific reason, produced it.

## Token sensitivity

`Grant.Token` is an unguessable, `crypto/rand`-generated bearer credential
(24 random bytes, hex-encoded — 192 bits of entropy, well above a 16-byte
minimum). **Treat it exactly like a secret**, per
[`agent-safety-policy.md`](agent-safety-policy.md#secrets-and-credentials):
never log, print, or persist it unredacted.

To make the safe behavior the default rather than something every caller
must remember:

- `Grant.Token` is tagged `json:"-"` — it is never included when a `Grant`
  is JSON-marshaled.
- `Grant.TokenHash` (the SHA-256 hex digest of `Token`) *is* marshaled, and
  is safe to log or display: it uniquely identifies the grant without
  making the token recoverable.
- `Grant.String()` — and therefore the default `%v`/`%+v`/`%s` `fmt`
  verbs, since Go's `fmt` package calls a type's `String()` method for
  those verbs — prints `TokenHash`, never `Token`. See
  `TestGrant_StringNeverIncludesRawToken`.

This does not stop a caller from explicitly doing something like
`fmt.Println(grant.Token)` — Go cannot prevent access to an exported field
— but every path this package itself controls (JSON encoding, default
formatting, `ToProvenanceApproval`) omits the raw token.

## Deriving approval policy from a repository contract

```go
func RequiresApproval(c contract.Contract, commandName string) (bool, error)
```

`RequiresApproval` answers "does this contract-declared command need
approval" purely by reading
[`contract.Contract.Commands`](agent-repository-contract-guide.md) and the
existing `CommandDecl.Class` field — this package adds **no new field** to
`contract.Contract` (modifying `contract/*.go` is out of scope for this
package):

- `provenance.ClassApprovalRequired` or `provenance.ClassDestructive` →
  `(true, nil)`.
- `provenance.ClassSafe`, `ClassMutating`, or `ClassNetworked` → `(false,
  nil)`.
- `commandName` not found in `c.Commands` at all → `(false,
  ErrCommandNotFound)`.

**The unknown-command case is the one a caller must handle carefully.**
`RequiresApproval` never itself returns `(true, err)` — the boolean result
is reserved exclusively for "the contract explicitly says this command's
class is/isn't approval-required," so it never silently asserts a policy
the contract didn't declare. Matching `discovery.ClassifyCommand` and
`verify`'s existing "unknown = safest assumption" precedent, **the caller
is responsible for treating a non-nil error as fail-closed**:

```go
needsApproval, err := approval.RequiresApproval(c, "release")
if err != nil {
    // Unknown to the contract: fail closed. Treat this exactly like
    // needsApproval == true rather than proceeding unchecked.
    needsApproval = true
}
if needsApproval {
    // consult a Broker before running "release"
}
```

A caller that checks only `if err == nil && needsApproval` and otherwise
proceeds unchecked has reintroduced the "missing approval fails open"
defect this package exists to prevent.

## Auditability

```go
func (b *Broker) ActiveGrants() []Grant
```

Returns every currently active grant (unexpired, unused), sorted
deterministically by `ApprovedAt` then `TokenHash`, as a snapshot safe to
inspect without affecting broker state. Remember each returned `Grant`
still carries its raw `Token` in memory (only encoding/formatting redacts
it) — prefer `Grant.String()` or default `%v` formatting over manually
building your own representation if you print these.

```go
func (g Grant) ToProvenanceApproval() provenance.Approval
```

Converts a grant to a `provenance.Approval` record — `Action` from
`Scope.Action`, plus `Notes` carrying the resource (if any) and
`TokenHash` (never the raw `Token`) — so a decision made by this package
can be folded into a `provenance.Envelope`'s `Approvals` list for handoff
continuity.

## No persistence (by design, not a gap)

A `Broker`'s grants live in process memory only. **A process restart
invalidates every outstanding grant.** This is intentional: an approval
broker whose grants survived a restart with no operator involvement would
be a *wider*, harder-to-audit trust boundary than one that requires
re-approval after any restart — silently persisted elevated-action
approvals are exactly the kind of thing that should require a human to
notice and re-grant, not something a crash-and-restart should hand back
for free. A durable, file- or database-backed grant store is future work,
only worth building if a real CLI/MCP integration needs approvals to
outlive a single process — this ticket is scoped to the in-memory
mechanism only.

## Example

```go
package main

import (
    "errors"
    "fmt"
    "time"

    "github.com/mediusfy/modulex/approval"
    "github.com/mediusfy/modulex/provenance"
)

func main() {
    b := approval.NewBroker()

    scope := approval.Scope{Action: "push", Resource: "release/v1.2.0"}

    // A human approves this specific action for the next 10 minutes.
    grant, err := b.Grant(scope, "drew@jocham.io", 10*time.Minute)
    if err != nil {
        panic(err)
    }
    fmt.Println("granted:", grant) // never prints the raw token

    // Later, a separate tool invocation is handed grant.Token out of band
    // and must present it to actually perform the push.
    if b.CheckToken(grant.Token, scope) != provenance.StatusPass {
        panic(errors.New("push not approved"))
    }
    // ... perform the push here ...

    // A second attempt for the same scope is denied: the grant was
    // single-use and has already been consumed.
    if b.CheckToken(grant.Token, scope) == provenance.StatusPass {
        panic("should never happen: grant was already consumed")
    }
}
```

## Related work

- [ADR-0032: Agent-First Development Experience](../adr/adr-0032-agent-first-development-experience.md) —
  P2's "Add an approval broker for elevated agent tools," and the "Safety
  and governance" section this package operationalizes.
- [`docs/planning/agent-safety-policy.md`](agent-safety-policy.md) — the
  human-facing policy this package makes checkable in code, including the
  "Human approval boundary" and "Secrets and credentials" sections.
- [`docs/planning/agent-repository-contract-guide.md`](agent-repository-contract-guide.md) —
  `contract.Contract`/`CommandDecl.Class`, which `RequiresApproval` reads.
- [`docs/planning/provenance-handoff-schema.md`](provenance-handoff-schema.md) —
  `provenance.Approval`/`provenance.Status`, which `Grant.ToProvenanceApproval`
  and `Broker.Check`'s return type reuse for consistency with the handoff
  schema.
- Jira MOD-69: this package (`approval.Broker`, `approval.Grant`,
  `approval.RequiresApproval`).
- Jira MOD-62/63/66: `contract`, `verify`, and `provenance` — the sibling
  packages this one builds on without modifying.

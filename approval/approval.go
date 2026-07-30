// Package approval implements a live, in-memory approval broker for
// elevated agent actions (network access, external mutations, push/release,
// deletion, infrastructure changes, database migrations, and Jira/PR
// operations), per ADR-0032 ("Agent-First Development Experience"), P2:
// "Add an approval broker for elevated agent tools" (Jira MOD-69). The
// ADR's "Safety and governance" section requires that the system:
//
//	require human approval for push, release, deletion, infrastructure
//	changes, database migrations, and external Jira/PR mutations
//
// and docs/planning/agent-safety-policy.md's "Human approval boundary"
// section states the scope discipline this package enforces in code:
//
//	an approval covers the specific action and scope granted [...], not a
//	standing blanket authorization for unrelated future actions
//
// # A broker, not a record
//
// [github.com/mediusfy/modulex/provenance].Approval already exists as part
// of the handoff/provenance schema: a flat record of an approval that
// happened (Action, ApprovedBy, ApprovedAt, Notes), meant to travel inside a
// provenance.Envelope for audit continuity. It has no scope or expiry field
// and nothing consults it before an action runs — it is written down after
// the fact.
//
// This package is a different, more active thing: a live mechanism a caller
// consults *before* running an elevated action to decide whether it is
// currently authorized. [Grant] adds the two properties provenance.Approval
// deliberately lacks — [Scope] (so a grant for one action/resource pair
// cannot silently cover another) and ExpiresAt (so a grant issued once does
// not remain valid forever) — and [Broker] is the concurrency-safe decision
// point that checks both. [Grant.ToProvenanceApproval] converts a Grant to a
// provenance.Approval so a decision made here can still be recorded in a
// handoff envelope.
//
// # No elevated operation is approved by default
//
// [NewBroker] starts with zero grants. [Broker.Check] and
// [Broker.DryRunCheck] deny (return provenance.StatusApprovalRequired) for
// any [Scope] until an explicit, scoped, unexpired [Broker.Grant] call has
// granted it. There is no configuration, flag, or constructor argument that
// starts a Broker in an already-approved state.
//
// # Fail closed
//
// [Broker.Check] has exactly one path that returns approval
// (provenance.StatusPass): finding a matching, unexpired, unused grant for
// the exact requested [Scope] while holding the broker's lock. Every other
// path — no grant at all, a grant for a different Action, a grant for a
// different Resource, an expired grant, an already-used single-use grant, a
// zero-value or malformed Scope, an empty token presented to
// [Broker.CheckToken] — returns provenance.StatusApprovalRequired. There is
// no default branch, error path, or early return anywhere in this package
// that produces an approval outcome other than that one matched case; see
// approval_test.go and broker_test.go for adversarial tests that attempt to
// violate this from several angles.
//
// # Single-use grants
//
// A [Grant] is consumed (marked Used) the first time [Broker.Check] or
// [Broker.CheckToken] matches it. A second Check for the same scope, even
// before expiry, is denied. This is the safer default for "prevents
// approval reuse outside its scope": a caller who wants to authorize
// several actions must request several grants (one per action), rather than
// a single grant being silently reusable an unbounded number of times
// within its TTL. [Broker.DryRunCheck] and [Broker.DryRunCheckToken] exist
// precisely so a caller can ask "would this be approved" without spending
// the grant.
//
// # Token sensitivity
//
// [Grant.Token] is an unguessable, crypto/rand-generated bearer credential:
// treat it exactly like a secret. It is deliberately excluded from Grant's
// JSON encoding (`json:"-"`) and from Grant.String()/the %v/%+v/%s fmt
// verbs, which print [Grant.TokenHash] (a SHA-256 hex digest of Token)
// instead — a stable identifier safe to log, display, or embed in a
// provenance artifact, that cannot be reversed back into the token itself.
// Do not print, log, or persist g.Token directly.
//
// # Deriving approval policy from a repository contract
//
// [RequiresApproval] answers "does this contract-declared command need
// approval" by reading
// [github.com/mediusfy/modulex/contract.Contract.Commands] and its
// existing provenance.CommandClass classification — this package adds no
// new field to contract.Contract itself (see [RequiresApproval]'s doc
// comment for the fail-closed handling of an unknown command name).
//
// # Not yet wired into anything
//
// This package is a standalone mechanism: no CLI, no MCP server, no actual
// call site in this repository invokes it yet. It is the trust boundary a
// future `modulex agent` CLI or MCP server is expected to consult before
// running push/release/delete/infrastructure/migration/Jira-PR actions. See
// docs/planning/agent-approval-broker-guide.md for a worked example and the
// full list of guarantees this package makes (and does not make).
//
// # No persistence
//
// A Broker's grants live in process memory only. A process restart
// invalidates every outstanding grant. This is intentional, not a gap: an
// approval broker that survived a restart with no operator involvement
// would be a wider, harder-to-audit trust boundary than one that requires
// re-approval after any restart. A durable grant store is future work if a
// real integration needs approvals to outlive a single process.
//
// # Example
//
//	b := approval.NewBroker()
//
//	scope := approval.Scope{Action: "push", Resource: "release/v1.2.0"}
//	grant, err := b.Grant(scope, "drew@jocham.io", 10*time.Minute)
//	if err != nil {
//	    return err
//	}
//
//	// A caller who only knows the scope (trusts the broker's own state):
//	if b.Check(scope) != provenance.StatusPass {
//	    return errors.New("push not approved")
//	}
//
//	// A caller who must present the specific token a human handed it:
//	if b.CheckToken(grant.Token, scope) != provenance.StatusPass {
//	    return errors.New("push not approved")
//	}
package approval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

// tokenBytes is the number of random bytes read from crypto/rand to build a
// Grant.Token, giving 24*8 = 192 bits of entropy — well above the 16-byte
// (128-bit) minimum a caller could reasonably want from an unguessable
// bearer credential, and encoded as hex (48 characters) so it is safe to
// pass around as a plain string (a CLI flag, a JSON field, an environment
// variable) without further escaping.
const tokenBytes = 24

// Scope identifies exactly what an approval covers: a specific action
// (e.g. "push", "release", or a provenance.CommandClass value used as a
// string) and, optionally, a specific resource that action applies to
// (e.g. a branch name or PR number). Both fields participate in every
// match a Broker performs — a Scope is never matched on Action alone. An
// empty Resource ("") is itself a specific value (meaning "this action,
// unscoped to any particular resource"), not a wildcard that matches any
// Resource: a grant for Scope{Action: "push", Resource: ""} does not
// authorize Scope{Action: "push", Resource: "branch-a"}, and vice versa.
type Scope struct {
	// Action is the specific action class or name this scope covers (e.g.
	// "push", "release", "delete-branch", or a provenance.CommandClass
	// value such as string(provenance.ClassDestructive)).
	Action string
	// Resource is what Action applies to (e.g. a branch name, a PR
	// number, a migration name), or "" if Action is not further scoped to
	// a specific resource.
	Resource string
}

// String renders scope as "action" or "action:resource", for use in error
// messages and logs. It never contains a Token, so it is always safe to
// log.
func (s Scope) String() string {
	if s.Resource == "" {
		return s.Action
	}
	return s.Action + ":" + s.Resource
}

// Grant is an approval that has actually been given: unlike
// [github.com/mediusfy/modulex/provenance.Approval] (a flat audit record),
// a Grant carries the [Scope] it covers and a required expiry, and is the
// unit [Broker] matches against and consumes. See the package doc comment's
// "A broker, not a record" and "Token sensitivity" sections.
//
// The only supported way to create a Grant is [Broker.Grant] (or
// [Broker.Check]/[Broker.DryRunCheck], which return copies of an existing
// Grant to describe a decision) — Broker.Grant is what enforces "a Grant
// cannot exist without an expiry" and "every Grant is attributable to an
// approver". A caller that builds a Grant{} struct literal directly bypasses
// those checks entirely and must not do so; nothing in this package accepts
// a hand-built Grant as input to a Broker.
type Grant struct {
	// Token is an unguessable, crypto/rand-generated bearer credential
	// identifying this grant. Treat it as sensitive: never log, print, or
	// persist it unredacted. See the package doc comment's "Token
	// sensitivity" section. Excluded from JSON encoding; see TokenHash.
	Token string `json:"-"`
	// TokenHash is the SHA-256 hex digest of Token: a stable identifier
	// that is safe to log, display, or embed in an audit artifact,
	// because it cannot be reversed back into Token. This is what is
	// JSON-marshaled and printed in place of Token.
	TokenHash string `json:"token_hash"`
	// Scope is the specific action/resource pair this grant authorizes.
	Scope Scope `json:"scope"`
	// ApprovedBy identifies who granted this approval (e.g. an email
	// address or username). Required — an unattributed approval cannot be
	// audited.
	ApprovedBy string `json:"approved_by"`
	// ApprovedAt is when the grant was created.
	ApprovedAt time.Time `json:"approved_at"`
	// ExpiresAt is when the grant stops being valid, regardless of Used.
	// Required and non-zero: [Broker.Grant] rejects any attempt to create
	// a Grant without a positive TTL, and every match performed by this
	// package additionally treats a zero-value ExpiresAt as already
	// expired (never as "no expiry"), as a defense-in-depth measure
	// against a Grant that reached a Broker's internal state some other
	// way than through Broker.Grant.
	ExpiresAt time.Time `json:"expires_at"`
	// Used records whether this grant has already been consumed by a
	// matching Broker.Check/Broker.CheckToken call. Grants in this
	// package are single-use by design — see the package doc comment's
	// "Single-use grants" section for why: it is the safer default for
	// "prevents approval reuse outside its scope", since a multi-use
	// grant would remain valid for repeated actions within its Scope for
	// its entire TTL, widening the window an approval covers beyond what
	// a human explicitly approved once. A caller who legitimately wants
	// to authorize N actions should request N grants.
	Used bool `json:"used"`
}

// String renders g without ever including the raw Token, so that %v, %+v,
// and %s (fmt's Stringer-triggering verbs) are always safe to log or print.
// Always prefer this (or the default %v/%+v formatting, which uses it
// automatically) over printing g.Token directly.
func (g Grant) String() string {
	status := "active"
	if g.Used {
		status = "used"
	}
	return fmt.Sprintf(
		"Grant{token_hash=%s scope=%s approved_by=%s approved_at=%s expires_at=%s status=%s}",
		g.TokenHash, g.Scope, g.ApprovedBy,
		g.ApprovedAt.Format(time.RFC3339), g.ExpiresAt.Format(time.RFC3339), status,
	)
}

// expired reports whether g is no longer valid at instant now, treating a
// zero-value ExpiresAt as already expired rather than as "never expires" —
// see ExpiresAt's doc comment.
func (g *Grant) expired(now time.Time) bool {
	if g.ExpiresAt.IsZero() {
		return true
	}
	return !now.Before(g.ExpiresAt)
}

// matchesScope reports whether g covers exactly scope: both Action and
// Resource must match. This is the core of "prevents approval reuse
// outside its scope" — a grant for Scope{Action: "push", Resource:
// "branch-a"} never matches Scope{Action: "push", Resource: "branch-b"} or
// Scope{Action: "delete", Resource: "branch-a"}.
func (g *Grant) matchesScope(scope Scope) bool {
	return g.Scope.Action == scope.Action && g.Scope.Resource == scope.Resource
}

// ToProvenanceApproval converts g to a
// [github.com/mediusfy/modulex/provenance.Approval] record, for continuity
// with the handoff/provenance schema (e.g. so a granted, consumed approval
// can be recorded in a provenance.Envelope's Approvals list). The resource
// (if any) and the grant's TokenHash (never the raw Token) are folded into
// Notes so the audit trail can still be tied back to this specific grant
// without exposing the bearer credential itself.
func (g Grant) ToProvenanceApproval() provenance.Approval {
	notes := "token_hash=" + g.TokenHash
	if g.Scope.Resource != "" {
		notes = "resource=" + g.Scope.Resource + " " + notes
	}
	return provenance.Approval{
		Action:     g.Scope.Action,
		ApprovedBy: g.ApprovedBy,
		ApprovedAt: g.ApprovedAt,
		Notes:      notes,
	}
}

// errEmptyAction is returned by Broker.Grant when Scope.Action is empty:
// an unscoped-to-any-action grant would be indistinguishable from a
// wildcard, which this package never allows.
var errEmptyAction = errors.New("approval: scope action must not be empty")

// errEmptyApprover is returned by Broker.Grant when approvedBy is empty:
// every grant must be attributable to an approver for auditability.
var errEmptyApprover = errors.New("approval: approvedBy must not be empty")

// errNonPositiveTTL is returned by Broker.Grant when ttl is not strictly
// positive: a Grant is not allowed to exist without a real expiry.
var errNonPositiveTTL = errors.New("approval: ttl must be positive")

// generateToken returns a new unguessable token (hex-encoded, tokenBytes
// bytes of crypto/rand output) and its SHA-256 hex digest.
func generateToken() (token, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("approval: generating token: %w", err)
	}
	token = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	return token, hash, nil
}

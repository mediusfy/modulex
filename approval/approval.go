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
	return token, hashToken(token), nil
}

// hashToken returns token's SHA-256 hex digest — the safe-to-log form used
// as Grant.TokenHash. Shared with FileStore (store.go), which persists
// Token but recomputes TokenHash on load rather than storing it twice.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

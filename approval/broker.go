package approval

import (
	"sort"
	"sync"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

// Broker is the live, in-memory decision point for whether an elevated
// action identified by a [Scope] is currently authorized. See the package
// doc comment for the guarantees it makes: nothing is approved by default,
// every decision fails closed, and a matched grant is scoped exactly (never
// by action alone) and single-use.
type Broker struct {
	mu sync.Mutex
	// grants is keyed by Grant.Token. Only Broker.Grant ever inserts into
	// this map, which is what makes Broker.Grant the sole construction
	// path for a Grant a Broker will ever recognize.
	grants map[string]*Grant
	// nowFunc returns the current time and defaults to time.Now. Tests
	// override it to get deterministic expiry behavior without
	// time.Sleep; see broker_test.go.
	nowFunc func() time.Time
}

// NewBroker returns a Broker with zero grants. No configuration,
// environment variable, or argument can start a Broker in an
// already-approved state: every [Broker.Check]/[Broker.CheckToken] call
// against a freshly constructed Broker returns provenance.StatusApprovalRequired
// until [Broker.Grant] is called explicitly. This is the concrete mechanism
// behind ADR-0032's "no elevated operation is enabled by default"
// requirement.
func NewBroker() *Broker {
	return &Broker{
		grants:  make(map[string]*Grant),
		nowFunc: time.Now,
	}
}

// now returns the broker's current time, defaulting to time.Now if nowFunc
// was never set (defends a Broker constructed via &Broker{} directly rather
// than NewBroker, though that is not the supported construction path).
func (b *Broker) now() time.Time {
	if b.nowFunc == nil {
		return time.Now()
	}
	return b.nowFunc()
}

// Grant creates and stores a new approval for scope, attributed to
// approvedBy, valid for ttl from now. ttl must be strictly positive and
// approvedBy must be non-empty; scope.Action must be non-empty. Any
// violation returns an error and stores nothing — there is no partial or
// best-effort grant creation.
//
// The returned Grant's Token is the only way to later authorize via
// [Broker.CheckToken]; the caller is responsible for handing it to whatever
// party is expected to present it, and for treating it as sensitive (see
// the package doc comment's "Token sensitivity" section).
func (b *Broker) Grant(scope Scope, approvedBy string, ttl time.Duration) (Grant, error) {
	if scope.Action == "" {
		return Grant{}, errEmptyAction
	}
	if approvedBy == "" {
		return Grant{}, errEmptyApprover
	}
	if ttl <= 0 {
		return Grant{}, errNonPositiveTTL
	}

	token, hash, err := generateToken()
	if err != nil {
		return Grant{}, err
	}

	now := b.now()
	g := &Grant{
		Token:      token,
		TokenHash:  hash,
		Scope:      scope,
		ApprovedBy: approvedBy,
		ApprovedAt: now,
		ExpiresAt:  now.Add(ttl),
	}

	b.mu.Lock()
	b.grants[token] = g
	// Copy g's fields into result while still holding the lock: g is now
	// reachable from Check/CheckToken (via b.grants), which mutate
	// g.Used under the same lock. Dereferencing *g after Unlock would race
	// with such a concurrent mutation on the exact object just stored.
	result := *g
	b.mu.Unlock()

	return result, nil
}

// Check reports whether scope is currently authorized by any stored grant,
// searching across every grant regardless of which token it was issued
// under. If exactly one matching, unexpired, unused grant exists, Check
// consumes it (marks it Used) and returns provenance.StatusPass. In every
// other case — no grant at all, a grant for a different action or
// resource, an expired grant, an already-used grant, or any state this
// package did not anticipate — Check returns
// provenance.StatusApprovalRequired. There is no code path in Check that
// can return anything other than one of these two statuses.
//
// Use [Broker.CheckToken] instead when the caller has (and should be
// required to present) the specific token a human handed it, rather than
// relying on the broker's own bookkeeping to find a matching grant.
func (b *Broker) Check(scope Scope) provenance.Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	g := b.findMatch("", scope)
	if g == nil {
		return provenance.StatusApprovalRequired
	}
	g.Used = true
	return provenance.StatusPass
}

// CheckToken reports whether token, presented as the credential for scope,
// is currently valid: token must identify a stored grant that is
// unexpired, unused, and whose Scope exactly matches scope (both Action
// and Resource). An empty token is always denied outright — it never falls
// back to Check's any-matching-grant search. On a match, the grant is
// consumed (marked Used) and CheckToken returns provenance.StatusPass;
// every other case returns provenance.StatusApprovalRequired.
func (b *Broker) CheckToken(token string, scope Scope) provenance.Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	if token == "" {
		return provenance.StatusApprovalRequired
	}
	g := b.findMatch(token, scope)
	if g == nil {
		return provenance.StatusApprovalRequired
	}
	g.Used = true
	return provenance.StatusPass
}

// DryRunCheck reports what [Broker.Check] would return for scope right now,
// without consuming a matching grant. Use this to answer "would this be
// approved" (e.g. to preview a plan before actually attempting the elevated
// action) without spending a single-use grant.
func (b *Broker) DryRunCheck(scope Scope) provenance.Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.findMatch("", scope) == nil {
		return provenance.StatusApprovalRequired
	}
	return provenance.StatusPass
}

// DryRunCheckToken reports what [Broker.CheckToken] would return for token
// and scope right now, without consuming a matching grant.
func (b *Broker) DryRunCheckToken(token string, scope Scope) provenance.Status {
	b.mu.Lock()
	defer b.mu.Unlock()

	if token == "" {
		return provenance.StatusApprovalRequired
	}
	if b.findMatch(token, scope) == nil {
		return provenance.StatusApprovalRequired
	}
	return provenance.StatusPass
}

// findMatch returns the stored grant matching scope (and, if token is
// non-empty, also matching that exact token) that is neither expired nor
// already used, or nil if none exists. Callers must hold b.mu.
//
// When token is empty, findMatch searches every stored grant; Go map
// iteration order is randomized per run, but at most one unused grant is
// ever expected to match a given scope in ordinary use (Broker.Grant does
// not deduplicate by scope, so a caller that grants the same scope twice
// will have findMatch return an arbitrary one of them — both remain
// individually subject to their own expiry and single-use consumption).
func (b *Broker) findMatch(token string, scope Scope) *Grant {
	now := b.now()

	if token != "" {
		g, ok := b.grants[token]
		if !ok || g.Used || g.expired(now) || !g.matchesScope(scope) {
			return nil
		}
		return g
	}

	for _, g := range b.grants {
		if g.Used || g.expired(now) || !g.matchesScope(scope) {
			continue
		}
		return g
	}
	return nil
}

// ActiveGrants returns every currently active grant — unexpired and unused
// — sorted deterministically by ApprovedAt then TokenHash, for auditing.
// The returned slice is a snapshot: mutating it, or the Grant values in it,
// does not affect the Broker's internal state. Always non-nil.
//
// Remember that each returned Grant still carries its raw Token; see the
// package doc comment's "Token sensitivity" section before logging or
// displaying these — prefer Grant.String()/the default %v formatting, which
// never prints Token.
func (b *Broker) ActiveGrants() []Grant {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	out := make([]Grant, 0, len(b.grants))
	for _, g := range b.grants {
		if g.Used || g.expired(now) {
			continue
		}
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ApprovedAt.Equal(out[j].ApprovedAt) {
			return out[i].ApprovedAt.Before(out[j].ApprovedAt)
		}
		return out[i].TokenHash < out[j].TokenHash
	})
	return out
}

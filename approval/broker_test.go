package approval

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

// fixedClock returns a nowFunc that always reports t, for deterministic
// expiry testing without time.Sleep. advance mutates the reported time.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFixedClock(t time.Time) *fixedClock {
	return &fixedClock{t: t}
}

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestBroker(clock *fixedClock) *Broker {
	b := NewBroker()
	b.nowFunc = clock.now
	return b
}

// --- "No elevated operation is approved by default" ---

func TestBroker_NewBrokerDeniesEverythingByDefault(t *testing.T) {
	b := NewBroker()

	scopes := []Scope{
		{},
		{Action: "push"},
		{Action: "push", Resource: "main"},
		{Action: string(provenance.ClassApprovalRequired)},
		{Action: string(provenance.ClassDestructive), Resource: "prod-db"},
		{Action: "release", Resource: "v1.0.0"},
	}
	for _, s := range scopes {
		if got := b.Check(s); got != provenance.StatusApprovalRequired {
			t.Errorf("Check(%+v) on a fresh Broker = %v, want %v", s, got, provenance.StatusApprovalRequired)
		}
		if got := b.DryRunCheck(s); got != provenance.StatusApprovalRequired {
			t.Errorf("DryRunCheck(%+v) on a fresh Broker = %v, want %v", s, got, provenance.StatusApprovalRequired)
		}
		if got := b.CheckToken("any-token-at-all", s); got != provenance.StatusApprovalRequired {
			t.Errorf("CheckToken(%+v) on a fresh Broker = %v, want %v", s, got, provenance.StatusApprovalRequired)
		}
	}
	if active := b.ActiveGrants(); len(active) != 0 {
		t.Errorf("ActiveGrants() on a fresh Broker = %v, want empty", active)
	}
}

// --- "Fail-closed on unknown scope" ---

func TestBroker_UnknownScopeDenied(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	if _, err := b.Grant(Scope{Action: "push", Resource: "branch-a"}, "drew", time.Minute); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	got := b.Check(Scope{Action: "totally-unrelated-action"})
	if got != provenance.StatusApprovalRequired {
		t.Errorf("Check() for an unknown scope = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

// --- "Fail-closed on expired grant" (fake clock, no time.Sleep) ---

func TestBroker_ExpiredGrantDenied(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "push", Resource: "branch-a"}
	if _, err := b.Grant(scope, "drew", time.Second); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	// Not yet expired: should still succeed.
	if got := b.DryRunCheck(scope); got != provenance.StatusPass {
		t.Fatalf("DryRunCheck() before expiry = %v, want %v", got, provenance.StatusPass)
	}

	clock.advance(2 * time.Second)

	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Errorf("Check() after expiry = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

func TestBroker_GrantExactlyAtExpiryBoundaryIsExpired(t *testing.T) {
	// now.Before(ExpiresAt) must be false when now == ExpiresAt, i.e. a
	// grant is not valid at the exact instant it expires (">=" is expired,
	// not ">").
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "push", Resource: "branch-a"}
	g, err := b.Grant(scope, "drew", time.Second)
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	clock.mu.Lock()
	clock.t = g.ExpiresAt
	clock.mu.Unlock()

	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Errorf("Check() at the exact expiry instant = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

// --- Scope isolation: the core "prevents approval reuse outside its scope" property ---

func TestBroker_ScopeIsolation(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	granted := Scope{Action: "push", Resource: "branch-a"}
	if _, err := b.Grant(granted, "drew", time.Minute); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	// Same action, different resource: denied.
	if got := b.Check(Scope{Action: "push", Resource: "branch-b"}); got != provenance.StatusApprovalRequired {
		t.Errorf("Check(push, branch-b) = %v, want %v (grant was for branch-a)", got, provenance.StatusApprovalRequired)
	}
	// Different action, same resource: denied.
	if got := b.Check(Scope{Action: "delete", Resource: "branch-a"}); got != provenance.StatusApprovalRequired {
		t.Errorf("Check(delete, branch-a) = %v, want %v (grant was for push)", got, provenance.StatusApprovalRequired)
	}
	// Different action, different resource: denied.
	if got := b.Check(Scope{Action: "delete", Resource: "branch-b"}); got != provenance.StatusApprovalRequired {
		t.Errorf("Check(delete, branch-b) = %v, want %v", got, provenance.StatusApprovalRequired)
	}
	// The exact granted scope: approved.
	if got := b.Check(granted); got != provenance.StatusPass {
		t.Errorf("Check(push, branch-a) = %v, want %v (this is the exact granted scope)", got, provenance.StatusPass)
	}
}

// --- Single-use enforcement ---

func TestBroker_SingleUseGrantCannotBeReused(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "push", Resource: "branch-a"}
	if _, err := b.Grant(scope, "drew", time.Minute); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	if got := b.Check(scope); got != provenance.StatusPass {
		t.Fatalf("first Check() = %v, want %v", got, provenance.StatusPass)
	}
	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Errorf("second Check() for the same scope = %v, want %v (grant already consumed)", got, provenance.StatusApprovalRequired)
	}
}

func TestBroker_CheckTokenSingleUseAndScoped(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "release", Resource: "v1.2.0"}
	g, err := b.Grant(scope, "drew", time.Minute)
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	// Wrong scope with the right token: denied.
	if got := b.CheckToken(g.Token, Scope{Action: "release", Resource: "v9.9.9"}); got != provenance.StatusApprovalRequired {
		t.Errorf("CheckToken() with mismatched resource = %v, want %v", got, provenance.StatusApprovalRequired)
	}
	// Right scope, wrong (empty) token: denied, never falls back to a
	// scope-only search.
	if got := b.CheckToken("", scope); got != provenance.StatusApprovalRequired {
		t.Errorf("CheckToken() with empty token = %v, want %v", got, provenance.StatusApprovalRequired)
	}
	// Right scope, forged/unknown token: denied.
	if got := b.CheckToken("0000000000000000000000000000000000000000000000", scope); got != provenance.StatusApprovalRequired {
		t.Errorf("CheckToken() with an unknown token = %v, want %v", got, provenance.StatusApprovalRequired)
	}
	// Correct token and scope: approved, and consumes the grant.
	if got := b.CheckToken(g.Token, scope); got != provenance.StatusPass {
		t.Fatalf("CheckToken() with correct token+scope = %v, want %v", got, provenance.StatusPass)
	}
	if got := b.CheckToken(g.Token, scope); got != provenance.StatusApprovalRequired {
		t.Errorf("CheckToken() reused after consumption = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

// --- Dry run never consumes ---

func TestBroker_DryRunNeverConsumes(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "push", Resource: "branch-a"}
	g, err := b.Grant(scope, "drew", time.Minute)
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		if got := b.DryRunCheck(scope); got != provenance.StatusPass {
			t.Fatalf("DryRunCheck() call #%d = %v, want %v", i, got, provenance.StatusPass)
		}
		if got := b.DryRunCheckToken(g.Token, scope); got != provenance.StatusPass {
			t.Fatalf("DryRunCheckToken() call #%d = %v, want %v", i, got, provenance.StatusPass)
		}
	}

	// A real Check after all those dry runs must still succeed, and must
	// then consume the grant (single-use).
	if got := b.Check(scope); got != provenance.StatusPass {
		t.Fatalf("real Check() after dry runs = %v, want %v (dry runs must not have consumed the grant)", got, provenance.StatusPass)
	}
	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Errorf("Check() after the real Check() consumed it = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

// --- Grant construction guarantees ---

func TestBroker_GrantRejectsNonPositiveTTL(t *testing.T) {
	b := NewBroker()
	scope := Scope{Action: "push", Resource: "branch-a"}

	for _, ttl := range []time.Duration{0, -time.Second, -time.Hour} {
		if _, err := b.Grant(scope, "drew", ttl); err == nil {
			t.Errorf("Grant() with ttl=%v: want an error (a Grant must not exist without a positive expiry)", ttl)
		}
	}
	if active := b.ActiveGrants(); len(active) != 0 {
		t.Errorf("a rejected Grant() call must not store anything, got %v", active)
	}
}

func TestBroker_GrantRejectsEmptyActionAndApprover(t *testing.T) {
	b := NewBroker()

	if _, err := b.Grant(Scope{Resource: "branch-a"}, "drew", time.Minute); err == nil {
		t.Error("Grant() with empty Action: want an error")
	}
	if _, err := b.Grant(Scope{Action: "push"}, "", time.Minute); err == nil {
		t.Error("Grant() with empty approvedBy: want an error")
	}
}

// --- Concurrency: no data race, no double-consumption ---

func TestBroker_ConcurrentCheckOnlyOneWinnerForSingleUseGrant(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	scope := Scope{Action: "push", Resource: "branch-a"}
	if _, err := b.Grant(scope, "drew", time.Minute); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	const workers = 50
	var successes int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if b.Check(scope) == provenance.StatusPass {
				atomic.AddInt32(&successes, 1)
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("successes = %d, want exactly 1 (single-use grant consumed by exactly one concurrent Check)", successes)
	}
}

func TestBroker_ConcurrentGrantAndCheckDoesNotRace(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		resource := "branch-" + string(rune('a'+i%26))
		go func(resource string) {
			defer wg.Done()
			_, _ = b.Grant(Scope{Action: "push", Resource: resource}, "drew", time.Minute)
		}(resource)
		go func(resource string) {
			defer wg.Done()
			_ = b.Check(Scope{Action: "push", Resource: resource})
			_ = b.DryRunCheck(Scope{Action: "push", Resource: resource})
			_ = b.ActiveGrants()
		}(resource)
	}
	wg.Wait()
	// The race detector (make test-arch) is what actually validates this
	// test; reaching here without -race flagging anything is the point.
}

// --- ActiveGrants auditability ---

func TestBroker_ActiveGrantsExcludesUsedAndExpired(t *testing.T) {
	clock := newFixedClock(time.Now())
	b := newTestBroker(clock)

	active := Scope{Action: "push", Resource: "keep"}
	used := Scope{Action: "push", Resource: "used"}
	expiring := Scope{Action: "push", Resource: "expiring"}

	if _, err := b.Grant(active, "drew", time.Hour); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if _, err := b.Grant(used, "drew", time.Hour); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if _, err := b.Grant(expiring, "drew", time.Second); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	if got := b.Check(used); got != provenance.StatusPass {
		t.Fatalf("Check(used) = %v, want %v", got, provenance.StatusPass)
	}
	clock.advance(2 * time.Second)

	grants := b.ActiveGrants()
	if len(grants) != 1 {
		t.Fatalf("ActiveGrants() = %d entries, want 1 (only %+v should remain active): %+v", len(grants), active, grants)
	}
	if grants[0].Scope != active {
		t.Errorf("ActiveGrants()[0].Scope = %+v, want %+v", grants[0].Scope, active)
	}
}

// --- RequiresApproval error handling does not accidentally return true+nil
// or false+nil for the wrong reason is covered in requires_test.go.

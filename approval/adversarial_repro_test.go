package approval_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/provenance"
)

func TestAdversarial_FreshBrokerDeniesEverything(t *testing.T) {
	b := approval.NewBroker()
	scopes := []approval.Scope{
		{Action: "push"},
		{Action: "release", Resource: "v1.0.0"},
		{Action: ""},
		{},
	}
	for _, s := range scopes {
		if got := b.Check(s); got != provenance.StatusApprovalRequired {
			t.Fatalf("fresh broker Check(%+v) = %v, want ApprovalRequired", s, got)
		}
		if got := b.DryRunCheck(s); got != provenance.StatusApprovalRequired {
			t.Fatalf("fresh broker DryRunCheck(%+v) = %v, want ApprovalRequired", s, got)
		}
		if got := b.CheckToken("guessed-token", s); got != provenance.StatusApprovalRequired {
			t.Fatalf("fresh broker CheckToken(%+v) = %v, want ApprovalRequired", s, got)
		}
	}
}

func TestAdversarial_CrossResourceReuseDenied(t *testing.T) {
	b := approval.NewBroker()
	granted := approval.Scope{Action: "push", Resource: "branch-a"}
	_, err := b.Grant(granted, "human@example.com", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	attacks := []approval.Scope{
		{Action: "push", Resource: "branch-b"},
		{Action: "delete", Resource: "branch-a"},
		{Action: "push"},
		{Action: "PUSH", Resource: "branch-a"},
	}
	for _, s := range attacks {
		if got := b.Check(s); got != provenance.StatusApprovalRequired {
			t.Fatalf("cross-scope attack Check(%+v) = %v, want ApprovalRequired (scope leaked)", s, got)
		}
	}

	if got := b.Check(granted); got != provenance.StatusPass {
		t.Fatalf("legitimate Check(%+v) = %v, want Pass", granted, got)
	}
}

func TestAdversarial_TokenGuessingDenied(t *testing.T) {
	b := approval.NewBroker()
	scope := approval.Scope{Action: "release", Resource: "v1.2.0"}
	real, err := b.Grant(scope, "human@example.com", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	guesses := []string{
		"",
		"0000000000000000000000000000000000000000000000",
		real.Token[:len(real.Token)-1] + "0",
		real.TokenHash,
	}
	for _, guess := range guesses {
		if guess == real.Token {
			continue
		}
		if got := b.CheckToken(guess, scope); got != provenance.StatusApprovalRequired {
			t.Fatalf("guessed token %q authorized scope %+v (token forgery succeeded)", guess, scope)
		}
	}

	if got := b.CheckToken(real.Token, scope); got != provenance.StatusPass {
		t.Fatalf("real token denied: got %v, want Pass", got)
	}
}

func TestAdversarial_ConcurrentDoubleSpendExactlyOneWinner(t *testing.T) {
	for trial := 0; trial < 20; trial++ {
		b := approval.NewBroker()
		scope := approval.Scope{Action: "delete", Resource: "prod-db"}
		if _, err := b.Grant(scope, "human@example.com", time.Minute); err != nil {
			t.Fatal(err)
		}

		const racers = 50
		var wins int64
		var wg sync.WaitGroup
		wg.Add(racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				<-start
				if b.Check(scope) == provenance.StatusPass {
					atomic.AddInt64(&wins, 1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if wins != 1 {
			t.Fatalf("trial %d: %d goroutines won a single-use grant race, want exactly 1", trial, wins)
		}
	}
}

func TestAdversarial_ExpiryEnforcedWithRealClock(t *testing.T) {
	b := approval.NewBroker()
	scope := approval.Scope{Action: "push", Resource: "hotfix"}
	if _, err := b.Grant(scope, "human@example.com", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := b.DryRunCheck(scope); got != provenance.StatusPass {
		t.Fatalf("immediately after grant, DryRunCheck = %v, want Pass", got)
	}
	time.Sleep(60 * time.Millisecond)
	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Fatalf("after expiry, Check = %v, want ApprovalRequired (expiry not enforced)", got)
	}
}

func TestAdversarial_ZeroTTLAndNegativeTTLRejected(t *testing.T) {
	b := approval.NewBroker()
	scope := approval.Scope{Action: "push"}
	if _, err := b.Grant(scope, "human@example.com", 0); err == nil {
		t.Fatal("Grant with ttl=0 succeeded, want error (grant without real expiry must be rejected)")
	}
	if _, err := b.Grant(scope, "human@example.com", -time.Minute); err == nil {
		t.Fatal("Grant with negative ttl succeeded, want error")
	}
	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Fatalf("after rejected Grant calls, Check = %v, want ApprovalRequired", got)
	}
}

func TestAdversarial_DryRunNeverConsumes(t *testing.T) {
	b := approval.NewBroker()
	scope := approval.Scope{Action: "release", Resource: "v2.0.0"}
	if _, err := b.Grant(scope, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if got := b.DryRunCheck(scope); got != provenance.StatusPass {
			t.Fatalf("DryRunCheck iteration %d = %v, want Pass (repeated dry runs should never consume)", i, got)
		}
	}
	if got := b.Check(scope); got != provenance.StatusPass {
		t.Fatalf("real Check after many dry runs = %v, want Pass (dry runs must not have consumed it)", got)
	}
	if got := b.Check(scope); got != provenance.StatusApprovalRequired {
		t.Fatalf("second real Check = %v, want ApprovalRequired (single-use not enforced after real consumption)", got)
	}
}

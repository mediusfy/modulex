package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/verify"
)

func TestRecommendVerification(t *testing.T) {
	t.Run("empty changed files still returns full gates", func(t *testing.T) {
		out := recommendVerification(nil)
		if len(out.Plan.FocusedChecks) != 0 {
			t.Errorf("FocusedChecks = %v, want empty for no changed files", out.Plan.FocusedChecks)
		}
		if len(out.Plan.FullGates) != len(verify.FullGates) {
			t.Errorf("len(FullGates) = %d, want %d", len(out.Plan.FullGates), len(verify.FullGates))
		}
	})

	t.Run("a changed go file recommends a focused check", func(t *testing.T) {
		out := recommendVerification([]string{"httpx/httpx.go"})
		if len(out.Plan.FocusedChecks) == 0 {
			t.Error("FocusedChecks is empty, want at least a test/vet check for httpx/httpx.go")
		}
	})
}

// Test commands below use "go vet ./..." (matches discovery.
// ClassifyCommand's `^go (build|vet|test)\b` rule, classified Safe) rather
// than a trivial "true"/"go version": since runVerification now classifies
// every Command before running it (see TestRunVerification's classification
// gate cases below), an unrecognized command like "true" would itself be
// blocked as ClassApprovalRequired (the fail-safe default) before ever
// reaching verify.Run — these tests need a Command that clears the gate to
// exercise verify.Run's own tool-availability/network/pass-fail behavior.
func TestRunVerification(t *testing.T) {
	t.Run("missing required tool is reported unavailable, never run", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{
				Name:         "fake-check",
				Command:      "go vet ./...",
				Category:     provenance.VerificationFull,
				RequiredTool: "definitely-not-a-real-tool-xyz",
			},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if len(out.Results) != 1 {
			t.Fatalf("len(Results) = %d, want 1", len(out.Results))
		}
		if out.Results[0].Status != provenance.StatusUnavailable {
			t.Errorf("Status = %q, want %q", out.Results[0].Status, provenance.StatusUnavailable)
		}
	})

	t.Run("networked check skipped without allow_network", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "networked", Command: "go vet ./...", Category: provenance.VerificationFull, Networked: true},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusSkipped {
			t.Errorf("Status = %q, want %q", out.Results[0].Status, provenance.StatusSkipped)
		}
	})

	t.Run("a trivial passing command", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "trivial", Command: "go vet ./...", Category: provenance.VerificationFocused},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusPass {
			t.Errorf("Status = %q, want %q; Message: %s", out.Results[0].Status, provenance.StatusPass, out.Results[0].Message)
		}
	})

	t.Run("invalid root returns an error", func(t *testing.T) {
		_, err := runVerification(context.Background(), approval.NewBroker(), "/does/not/exist/at/all", nil, false)
		if err == nil {
			t.Fatal("runVerification() error = nil, want an error for an invalid root")
		}
	})

	t.Run("destructive command is blocked without running", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "danger", Command: "git reset --hard", Category: provenance.VerificationFull},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusApprovalRequired {
			t.Errorf("Status = %q, want %q", out.Results[0].Status, provenance.StatusApprovalRequired)
		}
		if out.Results[0].Reason == "" {
			t.Error("Reason is empty, want ClassifyCommand's explanation")
		}
	})

	t.Run("mutating command is blocked without running, not just destructive", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "would-mutate", Command: "make fmt", Category: provenance.VerificationFull},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusApprovalRequired {
			t.Errorf("Status = %q, want %q (a read-only tool must never run a mutating command, not just destructive/approval-required ones)", out.Results[0].Status, provenance.StatusApprovalRequired)
		}
	})

	t.Run("unrecognized command defaults to approval-required, fail-safe", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "mystery", Command: "some-arbitrary-unrecognized-command", Category: provenance.VerificationFull},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusApprovalRequired {
			t.Errorf("Status = %q, want %q (unrecognized commands must fail safe, never silently pass)", out.Results[0].Status, provenance.StatusApprovalRequired)
		}
	})

	t.Run("a mix of blocked and runnable checks preserves order and count", func(t *testing.T) {
		out, err := runVerification(context.Background(), approval.NewBroker(), "../..", []CheckSpecIn{
			{Name: "first-blocked", Command: "git push origin main", Category: provenance.VerificationFull},
			{Name: "second-runs", Command: "go vet ./...", Category: provenance.VerificationFull},
			{Name: "third-blocked", Command: "git reset --hard", Category: provenance.VerificationFull},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if len(out.Results) != 3 {
			t.Fatalf("len(Results) = %d, want 3", len(out.Results))
		}
		wantNames := []string{"first-blocked", "second-runs", "third-blocked"}
		wantStatuses := []provenance.Status{provenance.StatusApprovalRequired, provenance.StatusPass, provenance.StatusApprovalRequired}
		for i, r := range out.Results {
			if r.Name != wantNames[i] {
				t.Errorf("Results[%d].Name = %q, want %q (order must match input)", i, r.Name, wantNames[i])
			}
			if r.Status != wantStatuses[i] {
				t.Errorf("Results[%d].Status = %q, want %q", i, r.Status, wantStatuses[i])
			}
		}
	})
}

// TestRunVerification_ApprovalStatusReflectsBrokerGrant is a decisive
// differential test for the broker wiring: a blocked check with a matching
// grant reports ApprovalStatus StatusPass, a blocked check without one
// reports StatusApprovalRequired, a check that runs gets no ApprovalStatus
// entry at all, and — since run_verification must use DryRunCheck, never
// Check — the grant still shows StatusPass on a second call, proving it
// was never consumed.
func TestRunVerification_ApprovalStatusReflectsBrokerGrant(t *testing.T) {
	broker := approval.NewBroker()
	grant, grantErr := broker.Grant(approval.Scope{Action: "danger"}, "tester", time.Minute)
	if grantErr != nil {
		t.Fatal(grantErr)
	}
	t.Logf("granted: %s", grant)

	checks := []CheckSpecIn{
		{Name: "danger", Command: "git reset --hard", Category: provenance.VerificationFull},
		{Name: "no-grant", Command: "git reset --hard", Category: provenance.VerificationFull},
		{Name: "runs-fine", Command: "go vet ./...", Category: provenance.VerificationFull},
	}

	for attempt := 1; attempt <= 2; attempt++ {
		out, err := runVerification(context.Background(), broker, "../..", checks, false)
		if err != nil {
			t.Fatalf("attempt %d: runVerification() error = %v", attempt, err)
		}
		if got := out.ApprovalStatus["danger"]; got != provenance.StatusPass {
			t.Errorf("attempt %d: ApprovalStatus[danger] = %q, want %q (DryRunCheck must not consume the grant)", attempt, got, provenance.StatusPass)
		}
		if got := out.ApprovalStatus["no-grant"]; got != provenance.StatusApprovalRequired {
			t.Errorf("attempt %d: ApprovalStatus[no-grant] = %q, want %q", attempt, got, provenance.StatusApprovalRequired)
		}
		if _, ok := out.ApprovalStatus["runs-fine"]; ok {
			t.Errorf("attempt %d: ApprovalStatus has an entry for a check that ran, want none", attempt)
		}
	}
}

// writeTinyGoModule writes a minimal go.mod plus one .go file (whatever
// content the caller supplies, valid or not) into dir, so "go vet ./..."
// run with dir as its working directory has something real to vet.
func writeTinyGoModule(t *testing.T, dir, goFileContent string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tmp\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(goFileContent), 0o644); err != nil {
		t.Fatalf("WriteFile main.go: %v", err)
	}
}

// TestRunVerification_UsesRootAsWorkingDirectory is a decisive differential
// regression test for root actually becoming Command's working directory
// (via verify.CheckSpec.Dir): two separate temp Go modules, one that vets
// cleanly and one with a syntax error. If root were not threaded through
// (the bug this guards against), "go vet ./..." would run against
// tools/mcpserver's own directory in both calls — which itself vets
// cleanly — so both would report StatusPass regardless of which root was
// given; only correctly honoring root as the working directory makes the
// second call fail.
func TestRunVerification_UsesRootAsWorkingDirectory(t *testing.T) {
	dirGood := t.TempDir()
	writeTinyGoModule(t, dirGood, "package tmp\n")

	dirBad := t.TempDir()
	writeTinyGoModule(t, dirBad, "package tmp\n\nfunc broken( {\n")

	check := []CheckSpecIn{{Name: "vet", Command: "go vet ./...", Category: provenance.VerificationFull}}

	goodOut, err := runVerification(context.Background(), approval.NewBroker(), dirGood, check, false)
	if err != nil {
		t.Fatalf("runVerification(dirGood) error = %v", err)
	}
	if goodOut.Results[0].Status != provenance.StatusPass {
		t.Errorf("dirGood: Status = %q, want %q; Message: %s", goodOut.Results[0].Status, provenance.StatusPass, goodOut.Results[0].Message)
	}

	badOut, err := runVerification(context.Background(), approval.NewBroker(), dirBad, check, false)
	if err != nil {
		t.Fatalf("runVerification(dirBad) error = %v", err)
	}
	if badOut.Results[0].Status != provenance.StatusFail {
		t.Errorf("dirBad: Status = %q, want %q (root must be honored as the working directory, not silently ignored); Message: %s", badOut.Results[0].Status, provenance.StatusFail, badOut.Results[0].Message)
	}
}

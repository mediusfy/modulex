package mcpserver

import (
	"context"
	"testing"

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
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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
		_, err := runVerification(context.Background(), "/does/not/exist/at/all", nil, false)
		if err == nil {
			t.Fatal("runVerification() error = nil, want an error for an invalid root")
		}
	})

	t.Run("destructive command is blocked without running", func(t *testing.T) {
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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

	t.Run("unrecognized command defaults to approval-required, fail-safe", func(t *testing.T) {
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
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

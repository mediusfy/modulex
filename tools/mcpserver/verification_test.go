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

func TestRunVerification(t *testing.T) {
	t.Run("missing required tool is reported unavailable, never run", func(t *testing.T) {
		out, err := runVerification(context.Background(), "../..", []CheckSpecIn{
			{
				Name:         "fake-check",
				Command:      "true",
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
			{Name: "networked", Command: "true", Category: provenance.VerificationFull, Networked: true},
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
			{Name: "trivial", Command: "true", Category: provenance.VerificationFocused},
		}, false)
		if err != nil {
			t.Fatalf("runVerification() error = %v", err)
		}
		if out.Results[0].Status != provenance.StatusPass {
			t.Errorf("Status = %q, want %q", out.Results[0].Status, provenance.StatusPass)
		}
	})

	t.Run("invalid root returns an error", func(t *testing.T) {
		_, err := runVerification(context.Background(), "/does/not/exist/at/all", nil, false)
		if err == nil {
			t.Fatal("runVerification() error = nil, want an error for an invalid root")
		}
	})
}

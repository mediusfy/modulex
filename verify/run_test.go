package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

func TestRun_MissingToolIsUnavailableAndNeverInvoked(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	check := CheckSpec{
		Name: "fake-check",
		// If this were ever actually executed, it would create marker.
		Command:      "touch " + marker,
		Category:     provenance.VerificationFull,
		RequiredTool: "definitely-not-a-real-tool-on-this-machine",
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusUnavailable {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusUnavailable)
	}
	if r.Reason == "" {
		t.Error("Reason is empty, want a non-empty reason naming the missing tool")
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker file exists at %s; the command was executed despite the required tool being absent", marker)
	}
}

func TestRun_NetworkedCheckSkippedWithoutAllowNetwork(t *testing.T) {
	check := CheckSpec{
		Name:      "deps",
		Command:   "go mod download",
		Category:  provenance.VerificationFull,
		Networked: true,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusSkipped {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusSkipped)
	}
	if r.Reason == "" {
		t.Error("Reason is empty, want a non-empty reason explaining the skip")
	}
}

func TestRun_NetworkedCheckRunsWithAllowNetwork(t *testing.T) {
	check := CheckSpec{
		Name:      "networked-but-actually-cheap",
		Command:   "go version",
		Category:  provenance.VerificationFull,
		Networked: true,
	}

	results := Run(context.Background(), []CheckSpec{check}, []discovery.ToolStatus{{Name: "go", Present: true}}, true)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusPass {
		t.Errorf("Status = %q, want %q (allowNetwork=true should let a Networked check actually run)", r.Status, provenance.StatusPass)
	}
}

func TestRun_RealCommandPasses(t *testing.T) {
	check := CheckSpec{
		Name:         "go-version",
		Command:      "go version",
		Category:     provenance.VerificationFocused,
		RequiredTool: "go",
	}

	results := Run(context.Background(), []CheckSpec{check}, []discovery.ToolStatus{{Name: "go", Present: true}}, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusPass {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusPass)
	}
	if r.Message == "" {
		t.Error("Message is empty, want captured command output")
	}
}

func TestRun_FailingCommandFails(t *testing.T) {
	check := CheckSpec{
		Name:     "always-fails",
		Command:  "exit 1",
		Category: provenance.VerificationFocused,
	}

	results := Run(context.Background(), []CheckSpec{check}, nil, false)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	r := results[0]

	if r.Status != provenance.StatusFail {
		t.Errorf("Status = %q, want %q", r.Status, provenance.StatusFail)
	}
}

func TestRun_EveryInputProducesExactlyOneResult(t *testing.T) {
	checks := []CheckSpec{
		{Name: "pass", Command: "true", Category: provenance.VerificationFocused},
		{Name: "fail", Command: "exit 1", Category: provenance.VerificationFocused},
		{Name: "unavailable", Command: "touch /should/not/run", Category: provenance.VerificationFull, RequiredTool: "no-such-tool"},
		{Name: "skipped", Command: "go mod download", Category: provenance.VerificationFull, Networked: true},
		{Name: "pass-2", Command: "echo ok", Category: provenance.VerificationFocused},
	}

	results := Run(context.Background(), checks, []discovery.ToolStatus{{Name: "go", Present: true}}, false)

	if len(results) != len(checks) {
		t.Fatalf("len(results) = %d, want %d (one result per input check, never fewer)", len(results), len(checks))
	}

	wantStatus := map[string]provenance.Status{
		"pass":        provenance.StatusPass,
		"fail":        provenance.StatusFail,
		"unavailable": provenance.StatusUnavailable,
		"skipped":     provenance.StatusSkipped,
		"pass-2":      provenance.StatusPass,
	}
	for _, r := range results {
		want, ok := wantStatus[r.Name]
		if !ok {
			t.Fatalf("unexpected result name %q", r.Name)
		}
		if r.Status != want {
			t.Errorf("result %q: Status = %q, want %q", r.Name, r.Status, want)
		}
		switch r.Status {
		case provenance.StatusSkipped, provenance.StatusUnavailable:
			if r.Reason == "" {
				t.Errorf("result %q: Reason is empty for status %q", r.Name, r.Status)
			}
		}
	}

	if _, err := os.Stat("/should/not/run"); !os.IsNotExist(err) {
		t.Fatal("the unavailable check's command was executed despite the missing tool")
	}
}

package verify

import (
	"strings"
	"testing"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

func TestRenderText_ContainsNameAndStatusForEachResult(t *testing.T) {
	results := []provenance.VerificationResult{
		{
			Name:     "test-httpx",
			Category: provenance.VerificationFocused,
			Status:   provenance.StatusPass,
			Duration: 120 * time.Millisecond,
		},
		{
			Name:     "vet-httpx",
			Category: provenance.VerificationFocused,
			Status:   provenance.StatusFail,
			Duration: 45 * time.Millisecond,
			Message:  "vet: some issue found",
		},
		{
			Name:     "lint",
			Category: provenance.VerificationFull,
			Status:   provenance.StatusUnavailable,
			Reason:   `required tool "golangci-lint" is not present on PATH`,
		},
		{
			Name:     "deps",
			Category: provenance.VerificationFull,
			Status:   provenance.StatusSkipped,
			Reason:   "networked check skipped: no network access in this environment",
		},
	}

	out := RenderText(results)

	for _, r := range results {
		if !strings.Contains(out, r.Name) {
			t.Errorf("rendered output missing check name %q\n---\n%s", r.Name, out)
		}
		if !strings.Contains(out, strings.ToUpper(string(r.Status))) {
			t.Errorf("rendered output missing status %q for check %q\n---\n%s", r.Status, r.Name, out)
		}
		if r.Reason != "" && !strings.Contains(out, r.Reason) {
			t.Errorf("rendered output missing reason %q for check %q\n---\n%s", r.Reason, r.Name, out)
		}
	}

	// Grouped by category: both category labels should appear.
	if !strings.Contains(out, string(provenance.VerificationFocused)) {
		t.Errorf("rendered output missing category %q\n---\n%s", provenance.VerificationFocused, out)
	}
	if !strings.Contains(out, string(provenance.VerificationFull)) {
		t.Errorf("rendered output missing category %q\n---\n%s", provenance.VerificationFull, out)
	}
}

func TestRenderText_EmptyResults(t *testing.T) {
	out := RenderText(nil)
	if out == "" {
		t.Error("RenderText(nil) returned an empty string; want an explicit human-readable message")
	}
}

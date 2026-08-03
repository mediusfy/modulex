package mcpserver

import (
	"context"
	"os/exec"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

func TestReviewDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	t.Run("no diff between HEAD and HEAD", func(t *testing.T) {
		out, err := reviewDiff(context.Background(), "../..", "HEAD", "HEAD", false)
		if err != nil {
			t.Fatalf("reviewDiff() error = %v", err)
		}
		if len(out.Results) == 0 {
			t.Fatal("Results is empty, want at least the secret-scan result")
		}

		var sawSecretScan bool
		for _, r := range out.Results {
			if r.Category == provenance.VerificationSecretScan {
				sawSecretScan = true
				if r.Status == provenance.StatusFail {
					t.Errorf("secret scan Status = fail on an empty diff: %s", r.Message)
				}
			}
		}
		if !sawSecretScan {
			t.Error("no VerificationSecretScan result found in Results")
		}
	})

	t.Run("nonexistent ref reports unavailable, not a handler error", func(t *testing.T) {
		out, err := reviewDiff(context.Background(), "../..", "this-ref-does-not-exist", "HEAD", false)
		if err != nil {
			t.Fatalf("reviewDiff() error = %v, want nil (bad refs surface inside Results)", err)
		}
		var sawUnavailable bool
		for _, r := range out.Results {
			if r.Category == provenance.VerificationSecretScan && r.Status == provenance.StatusUnavailable {
				sawUnavailable = true
			}
		}
		if !sawUnavailable {
			t.Error("expected the secret-scan result to be StatusUnavailable for a nonexistent ref")
		}
	})

	t.Run("invalid root returns a handler error", func(t *testing.T) {
		_, err := reviewDiff(context.Background(), "/does/not/exist/at/all", "HEAD", "HEAD", false)
		if err == nil {
			t.Fatal("reviewDiff() error = nil, want an error for an invalid root")
		}
	})
}

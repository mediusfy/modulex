package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

		var sawSecretScan, sawProtectedPaths bool
		for _, r := range out.Results {
			if r.Category == provenance.VerificationSecretScan {
				sawSecretScan = true
				if r.Status == provenance.StatusFail {
					t.Errorf("secret scan Status = fail on an empty diff: %s", r.Message)
				}
			}
			if r.Category == provenance.VerificationProtectedPaths {
				sawProtectedPaths = true
				if r.Status == provenance.StatusFail {
					t.Errorf("protected-paths Status = fail on an empty diff: %s", r.Message)
				}
			}
		}
		if !sawSecretScan {
			t.Error("no VerificationSecretScan result found in Results")
		}
		if !sawProtectedPaths {
			t.Error("no VerificationProtectedPaths result found in Results — this repository has a real modulex.agent.yaml with protected_paths declared")
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

// TestReviewDiff_ProtectedPathFromRealContract builds a fixture repository
// with its own modulex.agent.yaml declaring go.mod as protected, then
// changes go.mod between two commits — an end-to-end check that reviewDiff
// actually reads the contract (via readContract) and threads
// ProtectedPaths through to review.Review, not just that the two pieces
// each work in isolation.
func TestReviewDiff_ProtectedPathFromRealContract(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := newGitFixture(t)

	contractYAML := `schema_version: "1.0.0"
projects:
  - name: fixture
    path: .
protected_paths:
  - go.mod
`
	if err := os.WriteFile(filepath.Join(dir, contractFileName), []byte(contractYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", contractFileName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/fixture\n\nretract v0.1.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	runGit(t, dir, "add", contractFileName, "go.mod")
	runGit(t, dir, "commit", "--quiet", "-m", "add contract and go.mod")
	runGit(t, dir, "branch", "base")

	// go.mod carries a file-scoped exception (review.
	// goModEditTouchesOnlyNonRetractLines): only an edit that touches a
	// `retract` directive line counts as a protected-path hit, per
	// docs/planning/agent-safety-policy.md. Add a second retract entry so
	// this end-to-end test still exercises a real hit, not a false pass.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/fixture\n\nretract v0.1.0\nretract v0.2.0\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	runGit(t, dir, "commit", "--quiet", "-am", "go.mod: retract v0.2.0")

	out, err := reviewDiff(context.Background(), dir, "base", "HEAD", false)
	if err != nil {
		t.Fatalf("reviewDiff() error = %v", err)
	}

	var found bool
	for _, r := range out.Results {
		if r.Category != provenance.VerificationProtectedPaths {
			continue
		}
		found = true
		if r.Status != provenance.StatusFail {
			t.Errorf("protected-paths Status = %q, want fail (go.mod is declared protected and changed)", r.Status)
		}
	}
	if !found {
		t.Fatal("no VerificationProtectedPaths result found in Results")
	}
}

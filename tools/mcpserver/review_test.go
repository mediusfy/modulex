package mcpserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// newProtectedPathsFixture creates a git fixture repository with a
// modulex.agent.yaml declaring protectedPaths and an initial go.mod
// containing goModContent, committed and branched as "base". Shared by
// TestReviewDiff_ProtectedPathFromRealContract and
// TestReviewDiff_InvalidProtectedPathGlob, which differ only in
// protectedPaths, the initial go.mod content, and what they do with dir
// afterward.
func newProtectedPathsFixture(t *testing.T, protectedPaths []string, goModContent string) string {
	t.Helper()
	dir := newGitFixture(t)

	var contractYAML strings.Builder
	contractYAML.WriteString("schema_version: \"1.0.0\"\nprojects:\n  - name: fixture\n    path: .\n")
	if len(protectedPaths) > 0 {
		contractYAML.WriteString("protected_paths:\n")
		for _, p := range protectedPaths {
			fmt.Fprintf(&contractYAML, "  - %q\n", p)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, contractFileName), []byte(contractYAML.String()), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", contractFileName, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goModContent), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	runGit(t, dir, "add", contractFileName, "go.mod")
	runGit(t, dir, "commit", "--quiet", "-m", "add contract and go.mod")
	runGit(t, dir, "branch", "base")
	return dir
}

// commitGoMod overwrites dir's go.mod with content and commits it.
func commitGoMod(t *testing.T, dir, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	runGit(t, dir, "commit", "--quiet", "-am", message)
}

// protectedPathsResult runs reviewDiff over base..HEAD in dir and returns
// its VerificationProtectedPaths result, failing t if reviewDiff errors or
// produces no such result.
func protectedPathsResult(t *testing.T, dir string) provenance.VerificationResult {
	t.Helper()
	out, err := reviewDiff(context.Background(), dir, "base", "HEAD", false)
	if err != nil {
		t.Fatalf("reviewDiff() error = %v", err)
	}
	for _, r := range out.Results {
		if r.Category == provenance.VerificationProtectedPaths {
			return r
		}
	}
	t.Fatal("no VerificationProtectedPaths result found in Results")
	return provenance.VerificationResult{}
}

// TestReviewDiff_ProtectedPathFromRealContract checks reviewDiff actually
// reads the contract and threads ProtectedPaths through to review.Review,
// not just that the two pieces work in isolation.
func TestReviewDiff_ProtectedPathFromRealContract(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := newProtectedPathsFixture(t, []string{"go.mod"}, "module example.com/fixture\n\nretract v0.1.0\n")

	// go.mod only flags retract-directive edits as hits; add a second one
	// so this still exercises a real hit, not a false pass.
	commitGoMod(t, dir, "module example.com/fixture\n\nretract v0.1.0\nretract v0.2.0\n", "go.mod: retract v0.2.0")

	if r := protectedPathsResult(t, dir); r.Status != provenance.StatusFail {
		t.Errorf("protected-paths Status = %q, want fail (go.mod is declared protected and changed)", r.Status)
	}
}

// TestReviewDiff_InvalidProtectedPathGlob: a contract that parses fine but
// declares a malformed protected_paths glob (an unmatched "[") must not
// fail open. reviewDiff threads the patterns as-is into review.Review,
// whose CheckProtectedPaths result must (a) fail naming the bad pattern
// and (b) still enforce the other, valid protected_paths entries — this
// exercises that end-to-end through the contract file, not just the
// review package in isolation.
func TestReviewDiff_InvalidProtectedPathGlob(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := newProtectedPathsFixture(t, []string{"go.mod", ".github/workflows/[.yml"}, "module example.com/fixture\n")

	commitGoMod(t, dir, "module example.com/fixture\n\nretract v0.1.0\n", "go.mod: retract v0.1.0")

	r := protectedPathsResult(t, dir)
	if r.Status != provenance.StatusFail {
		t.Errorf("protected-paths Status = %q, want fail for a malformed glob plus a retract edit", r.Status)
	}
	if !strings.Contains(r.Message, ".github/workflows/[.yml") {
		t.Errorf("protected-paths Message = %q, want it to name the bad pattern", r.Message)
	}
	if !strings.Contains(r.Message, "go.mod") {
		t.Errorf("protected-paths Message = %q, want it to report the go.mod hit (valid entries stay enforced)", r.Message)
	}
}

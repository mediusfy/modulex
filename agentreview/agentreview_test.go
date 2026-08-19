package agentreview

import (
	"context"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/internal/gittest"
	"github.com/mediusfy/modulex/provenance"
)

// newRepoWithDiff wraps gittest.NewRepoWithDiff in a discovery.Repository
// whose Tools is nil (so review's make-based checks gate to StatusUnavailable
// without spawning a subprocess, keeping the test fast).
func newRepoWithDiff(t *testing.T) discovery.Repository {
	t.Helper()
	return discovery.Repository{Root: gittest.NewRepoWithDiff(t), IsGitRepo: true}
}

func TestReview_RunsChecksOverDiff(t *testing.T) {
	repo := newRepoWithDiff(t)

	results := Review(context.Background(), repo, "base", "HEAD", false, nil)
	if len(results) == 0 {
		t.Fatal("expected at least one verification result")
	}

	var sawSecretScan, sawProtectedPaths bool
	for _, r := range results {
		switch r.Category {
		case provenance.VerificationSecretScan:
			sawSecretScan = true
		case provenance.VerificationProtectedPaths:
			sawProtectedPaths = true
		}
	}
	if !sawSecretScan {
		t.Error("expected a secret-scan result")
	}
	if !sawProtectedPaths {
		t.Error("expected a protected-paths result")
	}
}

func TestEnvelope_ValidatesAndPopulates(t *testing.T) {
	repo := newRepoWithDiff(t)
	results := Review(context.Background(), repo, "base", "HEAD", false, nil)

	env, err := Envelope(context.Background(), repo, "claude", "mcp", results)
	if err != nil {
		t.Fatal(err)
	}

	// Envelope redacts and validates before returning, so the caller can
	// marshal it directly.
	if err := env.Validate(); err != nil {
		t.Fatalf("returned envelope must already validate: %v", err)
	}
	if env.SchemaVersion != provenance.SchemaVersion {
		t.Errorf("schema version = %q, want %q", env.SchemaVersion, provenance.SchemaVersion)
	}
	if env.Agent.Name != "claude" || env.Agent.Tool != "mcp" {
		t.Errorf("agent = %+v, want name=claude tool=mcp", env.Agent)
	}
	if env.Repository.Path != repo.Root {
		t.Errorf("repository path = %q, want %q", env.Repository.Path, repo.Root)
	}
	if env.Repository.Commit == "" {
		t.Error("repository commit must be resolved from git HEAD")
	}
	if len(env.Verification) != len(results) {
		t.Errorf("verification count = %d, want %d", len(env.Verification), len(results))
	}
}

// TestEnvelope_DetachedHeadOmitsBranch: a detached HEAD (the normal state for
// a CI checkout of refs/pull/N/merge) must leave Branch empty, not record the
// literal "HEAD" that `git rev-parse --abbrev-ref HEAD` reports there.
func TestEnvelope_DetachedHeadOmitsBranch(t *testing.T) {
	repo := newRepoWithDiff(t)
	gittest.Run(t, repo.Root, "checkout", "--detach", "--quiet")

	env, err := Envelope(context.Background(), repo, "claude", "mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.Repository.Branch != "" {
		t.Errorf("branch = %q, want empty on a detached HEAD", env.Repository.Branch)
	}
	if env.Repository.Commit == "" {
		t.Error("commit must still be resolved on a detached HEAD")
	}
}

// TestEnvelope_ValidateFailureIsSingleLine: a multi-finding Validate failure
// must surface as one "; "-joined line, not errors.Join's multi-line message
// — the invariant tools/mcpserver's create_handoff used to enforce with its
// own flattening and now inherits from here.
func TestEnvelope_ValidateFailureIsSingleLine(t *testing.T) {
	repo := newRepoWithDiff(t)
	// StatusSkipped requires a non-empty Reason, so these two results make
	// Validate report two findings.
	bad := []provenance.VerificationResult{
		{Name: "a", Category: provenance.VerificationSecretScan, Status: provenance.StatusSkipped},
		{Name: "b", Category: provenance.VerificationSecretScan, Status: provenance.StatusSkipped},
	}

	_, err := Envelope(context.Background(), repo, "claude", "mcp", bad)
	if err == nil {
		t.Fatal("expected a validation error for skipped results without a reason")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("validation error contains a newline, want one \"; \"-joined line: %q", err)
	}
	if !strings.Contains(err.Error(), "; ") {
		t.Errorf("validation error = %q, want both findings joined with \"; \"", err)
	}
}

func TestEnvelope_ErrorsOutsideGitRepo(t *testing.T) {
	repo := discovery.Repository{Root: t.TempDir()} // not a git repo

	if _, err := Envelope(context.Background(), repo, "claude", "mcp", nil); err == nil {
		t.Fatal("expected an error resolving commit outside a git repository")
	}
}

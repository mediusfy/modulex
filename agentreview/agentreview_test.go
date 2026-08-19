package agentreview

import (
	"context"
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

func TestEnvelope_ErrorsOutsideGitRepo(t *testing.T) {
	repo := discovery.Repository{Root: t.TempDir()} // not a git repo

	if _, err := Envelope(context.Background(), repo, "claude", "mcp", nil); err == nil {
		t.Fatal("expected an error resolving commit outside a git repository")
	}
}

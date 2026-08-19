package agentreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

// runGit runs a git command in dir with a fully isolated configuration (no
// user/global/system config, deterministic identity) so the fixture does not
// depend on the host's gitconfig, hooks, or commit signing.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepoWithDiff creates a temp git repo with a "base" branch and a HEAD
// commit that adds a function to app.go, returning a discovery.Repository whose
// Root points at it and whose Tools is nil (so review's make-based checks gate
// to StatusUnavailable without spawning a subprocess, keeping the test fast).
func newRepoWithDiff(t *testing.T) discovery.Repository {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")
	writeFile(t, root, "app.go", "package app\n\nfunc Hello() string { return \"hi\" }\n")
	runGit(t, root, "commit", "--quiet", "-am", "add hello")
	return discovery.Repository{Root: root, IsGitRepo: true}
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

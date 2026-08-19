package agentcli

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
// commit that adds a function to app.go, returning the repo root. The diff
// base..HEAD is benign (no secret-shaped lines, no protected paths touched).
func newRepoWithDiff(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")
	writeFile(t, root, "app.go", "package app\n\nfunc Hello() string { return \"hi\" }\n")
	runGit(t, root, "commit", "--quiet", "-am", "add hello")
	return root
}

// stubDiscovery points discoverRepo at a fixture that reports no tools — so
// the make-based checks gate to StatusUnavailable without spawning any
// subprocess — while keeping Root pointed at the real temp repo so the
// git-based checks (secret scan, protected paths) still run against it.
func stubDiscovery(t *testing.T, root string) {
	t.Helper()
	prev := discoverRepo
	discoverRepo = func(string) (discovery.Repository, error) {
		return discovery.Repository{Root: root, IsGitRepo: true}, nil
	}
	t.Cleanup(func() { discoverRepo = prev })
}

func TestLoadProtectedPaths(t *testing.T) {
	t.Run("absent contract is nil, not an error", func(t *testing.T) {
		pp, err := loadProtectedPaths(t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pp != nil {
			t.Fatalf("protected paths = %v, want nil", pp)
		}
	})

	t.Run("declared paths are returned without validating the contract", func(t *testing.T) {
		root := t.TempDir()
		// Deliberately omit required fields other than protected_paths: an
		// incomplete/invalid contract must still surface its protected paths.
		writeFile(t, root, ContractFileName, "protected_paths:\n  - SECURITY.md\n  - go.mod\n")
		pp, err := loadProtectedPaths(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(pp) != 2 || pp[0] != "SECURITY.md" || pp[1] != "go.mod" {
			t.Fatalf("protected paths = %v, want [SECURITY.md go.mod]", pp)
		}
	})

	t.Run("unparseable contract is an error", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ContractFileName, "protected_paths: [oops\n")
		if _, err := loadProtectedPaths(root); err == nil {
			t.Fatal("expected an error for malformed YAML")
		}
	})
}

func TestReview_ReturnsResultsForDiff(t *testing.T) {
	root := newRepoWithDiff(t)
	stubDiscovery(t, root)

	results, err := Review(context.Background(), root, "base", "HEAD", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one verification result")
	}

	var sawSecretScan bool
	for _, r := range results {
		if r.Category == provenance.VerificationSecretScan {
			sawSecretScan = true
		}
	}
	if !sawSecretScan {
		t.Error("expected a secret-scan result among the review results")
	}
}

func TestHandoff_ProducesValidatedEnvelope(t *testing.T) {
	root := newRepoWithDiff(t)
	stubDiscovery(t, root)

	env, err := Handoff(context.Background(), root, "claude", "base", "HEAD", false)
	if err != nil {
		t.Fatal(err)
	}

	// The returned envelope must already pass Validate — Handoff redacts and
	// validates before returning, so a caller can marshal it directly.
	if err := env.Validate(); err != nil {
		t.Fatalf("returned envelope must already validate: %v", err)
	}
	if env.SchemaVersion != provenance.SchemaVersion {
		t.Errorf("schema version = %q, want %q", env.SchemaVersion, provenance.SchemaVersion)
	}
	if env.Agent.Name != "claude" {
		t.Errorf("agent name = %q, want %q", env.Agent.Name, "claude")
	}
	if env.Agent.Tool != "modulex-cli" {
		t.Errorf("agent tool = %q, want %q", env.Agent.Tool, "modulex-cli")
	}
	if env.Repository.Commit == "" {
		t.Error("repository commit must be resolved from git HEAD")
	}
	if len(env.Verification) == 0 {
		t.Error("verification results must be present in the handoff")
	}
}

func TestHandoff_ErrorsOutsideGitRepo(t *testing.T) {
	root := t.TempDir() // no git repo here
	stubDiscovery(t, root)

	if _, err := Handoff(context.Background(), root, "claude", "base", "HEAD", false); err == nil {
		t.Fatal("expected an error resolving commit outside a git repository")
	}
}

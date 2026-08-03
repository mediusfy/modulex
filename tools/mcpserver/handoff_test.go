package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newGitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "--quiet", "-m", "initial commit")
	return dir
}

func TestBuildHandoffEnvelope(t *testing.T) {
	t.Run("valid git repo with valid verification results", func(t *testing.T) {
		dir := newGitFixture(t)

		env, err := buildHandoffEnvelope(context.Background(), dir, "claude", []provenance.VerificationResult{
			{Name: "lint", Category: provenance.VerificationFull, Status: provenance.StatusPass},
		})
		if err != nil {
			t.Fatalf("buildHandoffEnvelope() error = %v", err)
		}
		if env.Repository.Commit == "" {
			t.Error("Repository.Commit is empty, want the fixture's commit SHA")
		}
		if env.Repository.Branch == "" {
			t.Error("Repository.Branch is empty, want the fixture's branch name")
		}
		if env.Agent.Name != "claude" {
			t.Errorf("Agent.Name = %q, want \"claude\"", env.Agent.Name)
		}
		if err := env.Validate(); err != nil {
			t.Errorf("returned envelope fails Validate(): %v", err)
		}
	})

	t.Run("skipped result without reason fails validation", func(t *testing.T) {
		dir := newGitFixture(t)

		_, err := buildHandoffEnvelope(context.Background(), dir, "claude", []provenance.VerificationResult{
			{Name: "lint", Category: provenance.VerificationFull, Status: provenance.StatusSkipped},
		})
		if err == nil {
			t.Fatal("buildHandoffEnvelope() error = nil, want an error (StatusSkipped requires a non-empty Reason)")
		}
	})

	t.Run("non-git root returns an error", func(t *testing.T) {
		dir := t.TempDir()

		_, err := buildHandoffEnvelope(context.Background(), dir, "claude", nil)
		if err == nil {
			t.Fatal("buildHandoffEnvelope() error = nil, want an error (not a git repository)")
		}
	})
}

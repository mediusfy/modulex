package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRepo builds a minimal two-ref repository: base branch "main" with an
// initial commit, and HEAD on a feature branch with extra files committed.
func gitRepo(t *testing.T, headFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "feature")
	for name, content := range headFiles {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "feature")
	return dir
}

func TestModulexReview(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		headFiles     map[string]string
		wantErr       string
		wantFailCheck string
	}{
		{
			name: "pure checks only: a malicious Makefile target never executes",
			headFiles: map[string]string{
				// If any declared command ran, the marker file would exist.
				"Makefile": "check-consumer-boundary:\n\ttouch PWNED\n",
				"modulex.agent.yaml": "schema_version: \"1.0.0\"\n" +
					"verification:\n  focused: []\n",
			},
		},
		{
			name: "protected path hit is reported",
			headFiles: map[string]string{
				"CODEOWNERS": "* @nobody\n",
				"modulex.agent.yaml": "schema_version: \"1.0.0\"\n" +
					"protected_paths:\n  - CODEOWNERS\n",
			},
			wantFailCheck: "protected",
		},
		{
			name: "malformed contract fails closed",
			headFiles: map[string]string{
				"modulex.agent.yaml": ":\tthis is not yaml{{",
			},
			wantErr: "unparseable",
		},
		{
			name:      "no contract reviews without protected paths",
			headFiles: map[string]string{"pkg.go": "package x\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := gitRepo(t, tt.headFiles)
			results, err := Modulex{}.Review(ctx, dir, "main", "HEAD")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Exactly the two pure checks, never a declared command.
			if len(results) != 2 {
				t.Fatalf("got %d results, want exactly the 2 pure checks: %+v", len(results), results)
			}
			if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
				t.Fatal("declared Makefile target executed in the hosted engine")
			}
			foundFail := ""
			for _, r := range results {
				if r.Status == "fail" {
					foundFail = r.Name
				}
			}
			if tt.wantFailCheck != "" && !strings.Contains(foundFail, tt.wantFailCheck) {
				t.Fatalf("want a failing %q check, failures: %q (results %+v)", tt.wantFailCheck, foundFail, results)
			}
		})
	}
}

// Package gittest provides shared git-repository test fixtures for the test
// suites that exercise review/handoff code paths (agentreview and
// tools/agentcli). Every helper is deterministic and fully isolated from the
// host's git configuration so fixtures behave identically on any machine or CI
// runner.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run runs a git command in dir with a fully isolated configuration (no
// user/global/system config, deterministic identity) so the fixture does not
// depend on the host's gitconfig, hooks, or commit signing. GIT_DIR,
// GIT_WORK_TREE, and GIT_INDEX_FILE are stripped from the inherited
// environment: git exports them to hooks, and a fixture inheriting them (e.g.
// `go test` run from a commit hook) would operate on the host repository
// instead of dir.
func Run(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not available on PATH: %v", err)
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_DIR=") ||
			strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// WriteFile writes content to name under dir, failing the test on error.
func WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// NewRepoWithDiff creates a temp git repo with a "base" branch and a HEAD
// commit that adds a function to app.go, returning the repo root. The diff
// base..HEAD is benign (no secret-shaped lines, no protected paths touched).
func NewRepoWithDiff(t *testing.T) string {
	t.Helper()
	const quiet = "--quiet"
	const appFile = "app.go"
	root := t.TempDir()
	Run(t, root, "init", quiet)
	WriteFile(t, root, appFile, "package app\n")
	Run(t, root, "add", appFile)
	Run(t, root, "commit", quiet, "-m", "base")
	Run(t, root, "branch", "base")
	WriteFile(t, root, appFile, "package app\n\nfunc Hello() string { return \"hi\" }\n")
	Run(t, root, "commit", quiet, "-am", "add hello")
	return root
}

package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// shellQuote wraps s in single quotes for safe embedding in a POSIX shell
// script, escaping any single quote it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestGitFetcher_TokenNeverAppearsInArgv proves the installation token is
// never present in any git subprocess's command-line arguments — only in
// its environment (GIT_CONFIG_KEY_0/GIT_CONFIG_VALUE_0) — by resolving
// "git" (via PATH) to a stub script that logs its own argv before exec'ing
// the real git binary, so the fetch still runs for real against a local
// repository. argv is readable by anything else in the same process
// namespace (`ps`, /proc/<pid>/cmdline); the environment is materially more
// restricted (/proc/<pid>/environ, same user or root only).
func TestGitFetcher_TokenNeverAppearsInArgv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script PATH stub requires a POSIX shell")
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}

	repo := gitRepo(t, map[string]string{"new.txt": "x"})
	headSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "feature"))

	argvLog := filepath.Join(t.TempDir(), "argv.log")
	stubDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(argvLog) + "\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", stubDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}

	const token = "super-secret-installation-token-xyz" // nosecret: test fixture, not a real credential
	dir, cleanup, err := GitFetcher{}.Fetch(context.Background(), repo, token, "main", headSHA)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer cleanup()

	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	// The raw token is never embedded directly in argv — it rides inside a
	// base64-encoded "user:token" Basic-auth blob, which doesn't preserve
	// the raw token as a substring, so checking for the raw token alone
	// would miss the actual exposure. Check for the exact blob the Basic
	// auth header carries instead.
	authBlob := basicAuth("x-access-token", token)
	if strings.Contains(string(logged), authBlob) {
		t.Fatalf("auth header appeared in a git subprocess's argv:\n%s", logged)
	}
	if strings.Contains(string(logged), token) {
		t.Fatalf("token appeared in a git subprocess's argv:\n%s", logged)
	}
	if !strings.Contains(string(logged), "fetch") {
		t.Fatalf("stub does not appear to have been invoked for any fetch; log:\n%s", logged)
	}

	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatalf("expected the real clone to still succeed and contain new.txt: %v", err)
	}
}

// TestGitFetcher_MaterializesBlobsForOfflineDiff guards the blobless-clone
// auth gap: the hosted bot reported secret_scan "unavailable" with
// "could not read Username ... from promisor remote". The secret and
// protected-path checks — and the AI diff — run `git diff` from the review
// package with NO token in their environment, so any blob Fetch left for a
// lazy promisor fetch at diff time failed that unauthenticated fetch and
// silently disabled the checks. Fetch now runs an authenticated warm-up diff
// over prreview-base...HEAD so every changed blob is local before the checks,
// which run from this same process environment, ever diff.
//
// Lazy blobs are reproduced the only way a local repo can: a file:// origin
// with uploadpack.allowFilter actually honors --filter=blob:none, whereas a
// bare local path silently ignores it. The test then severs the promisor
// remote after Fetch and asserts a later unauthenticated diff still succeeds.
// The modified-file row is the real guard — `git checkout` materializes only
// the head side, so a file changed between base and head is exactly the case
// whose base-side blob a lazy clone would still be missing at diff time.
func TestGitFetcher_MaterializesBlobsForOfflineDiff(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}

	tests := []struct {
		name       string
		headFiles  map[string]string
		wantInDiff string
	}{
		{
			name:       "file modified between base and head needs the base-side blob",
			headFiles:  map[string]string{"README.md": "changed in head\n"},
			wantInDiff: "changed in head",
		},
		{
			name:       "file added in head",
			headFiles:  map[string]string{"added.txt": "brand new line\n"},
			wantInDiff: "brand new line",
		},
	}

	const token = "regression-fixture-token" // nosecret: test fixture, not a real credential
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origin := gitRepo(t, tc.headFiles)
			// file:// + allowFilter is what makes --filter=blob:none actually
			// defer blobs; without it the local transport copies everything
			// and the bug cannot be reproduced.
			runGit(t, origin, "config", "uploadpack.allowFilter", "true")
			runGit(t, origin, "config", "uploadpack.allowAnySHA1InWant", "true")
			headSHA := strings.TrimSpace(runGit(t, origin, "rev-parse", "feature"))

			dir, cleanup, err := GitFetcher{}.Fetch(context.Background(), "file://"+origin, token, "main", headSHA)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			defer cleanup()

			// Sever the promisor remote: any blob not already local now makes
			// an on-demand diff-time fetch fail, exactly as the unauthenticated
			// production fetch did.
			runGit(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "severed"))

			// Emulate a review check: `git diff base...HEAD` with no auth env
			// (cmd.Env defaults to this process's environment, which carries no
			// token — the condition under which the bug fired).
			cmd := exec.Command(realGit, "-C", dir, "diff", "--unified=0", BaseRefName+"...HEAD")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("offline diff after Fetch failed — changed blobs were not materialized:\n%v\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.wantInDiff) {
				t.Fatalf("diff missing %q; got:\n%s", tc.wantInDiff, out)
			}
		})
	}
}

// runGit runs git with args in dir and returns combined stdout+stderr,
// failing the test on a non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

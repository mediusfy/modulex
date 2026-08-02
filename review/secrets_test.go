package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newTestRepo lays out a fresh git repository at t.TempDir(), makes it the
// current process's working directory for the duration of the test (via
// t.Chdir, restored automatically), and returns the repo root. ScanSecrets
// (like verify.Run's "sh -c" commands) operates relative to the process
// working directory rather than taking an explicit repo-root parameter, to
// stay consistent with that existing convention.
func newTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	t.Chdir(root)
	return root
}

func TestScanSecrets_NoFindings(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "app.go", "package app\n\nfunc Hello() string { return \"hi\" }\n")
	runGit(t, root, "commit", "--quiet", "-am", "add hello")

	result := ScanSecrets(context.Background(), "base", "HEAD")

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass; Message: %s", result.Status, result.Message)
	}
	if result.Name != "check-secrets" {
		t.Errorf("Name = %q, want check-secrets", result.Name)
	}
	if result.Category != "secret_scan" {
		t.Errorf("Category = %q, want secret_scan", result.Category)
	}
}

func TestScanSecrets_FindsSecretAddedLine(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "config.go", "package config\n")
	runGit(t, root, "add", "config.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "config.go", "package config\n\nconst apiKey = \"api_key=supersecretvalue123\"\n")
	runGit(t, root, "commit", "--quiet", "-am", "oops, hardcoded a key")

	result := ScanSecrets(context.Background(), "base", "HEAD")

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail; Message: %s", result.Status, result.Message)
	}
	if strings.Contains(result.Message, "supersecretvalue123") {
		t.Fatalf("Message leaks the raw secret value: %s", result.Message)
	}
	if !strings.Contains(result.Message, "config.go:3") {
		t.Errorf("Message = %q, want it to name config.go:3", result.Message)
	}
	if !strings.Contains(result.Message, "[REDACTED]") {
		t.Errorf("Message = %q, want a redaction marker in place of the secret", result.Message)
	}
}

func TestScanSecrets_IgnoresPreexistingSecretOutsideDiff(t *testing.T) {
	root := newTestRepo(t)

	// The secret-shaped line is already present at the base commit, so it
	// must not be flagged: ScanSecrets only scans lines *added* between
	// baseRef and headRef, not the whole file content at headRef.
	writeFile(t, root, "config.go", "package config\n\nconst apiKey = \"api_key=preexistingsecret456\"\n")
	runGit(t, root, "add", "config.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "config.go", "package config\n\nconst apiKey = \"api_key=preexistingsecret456\"\n\nfunc Unrelated() {}\n")
	runGit(t, root, "commit", "--quiet", "-am", "unrelated change")

	result := ScanSecrets(context.Background(), "base", "HEAD")

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (pre-existing secret is outside the diff); Message: %s", result.Status, result.Message)
	}
}

func TestScanSecrets_BadRefReturnsUnavailable(t *testing.T) {
	newTestRepo(t)

	result := ScanSecrets(context.Background(), "this-ref-does-not-exist", "HEAD")

	if result.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", result.Status)
	}
	if result.Reason == "" {
		t.Error("Reason is empty, want an explanation naming the failed diff computation")
	}
}

func TestAddedLines(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []addedLine
	}{
		{
			name: "single file single hunk",
			diff: "diff --git a/file.go b/file.go\n" +
				"index 111..222 100644\n" +
				"--- a/file.go\n" +
				"+++ b/file.go\n" +
				"@@ -10,0 +11,2 @@ func foo() {\n" +
				"+line one\n" +
				"+line two\n",
			want: []addedLine{
				{file: "file.go", line: 11, text: "line one"},
				{file: "file.go", line: 12, text: "line two"},
			},
		},
		{
			name: "two files",
			diff: "diff --git a/a.go b/a.go\n" +
				"--- a/a.go\n" +
				"+++ b/a.go\n" +
				"@@ -1,0 +2 @@\n" +
				"+added in a\n" +
				"diff --git a/b.go b/b.go\n" +
				"--- a/b.go\n" +
				"+++ b/b.go\n" +
				"@@ -5,0 +6 @@\n" +
				"+added in b\n",
			want: []addedLine{
				{file: "a.go", line: 2, text: "added in a"},
				{file: "b.go", line: 6, text: "added in b"},
			},
		},
		{
			name: "new file",
			diff: "diff --git a/new.go b/new.go\n" +
				"new file mode 100644\n" +
				"index 0000000..abc123\n" +
				"--- /dev/null\n" +
				"+++ b/new.go\n" +
				"@@ -0,0 +1,2 @@\n" +
				"+package new\n" +
				"+\n",
			want: []addedLine{
				{file: "new.go", line: 1, text: "package new"},
				{file: "new.go", line: 2, text: ""},
			},
		},
		{
			name: "pure deletion contributes nothing",
			diff: "diff --git a/old.go b/old.go\n" +
				"deleted file mode 100644\n" +
				"index abc..000000\n" +
				"--- a/old.go\n" +
				"+++ /dev/null\n" +
				"@@ -1,2 +0,0 @@\n" +
				"-line1\n" +
				"-line2\n",
			want: nil,
		},
		{
			name: "empty diff",
			diff: "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addedLines(tt.diff)
			if len(got) != len(tt.want) {
				t.Fatalf("addedLines() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("addedLines()[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

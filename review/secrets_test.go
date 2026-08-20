package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
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
// t.Chdir, restored automatically), and returns the repo root. Tests below
// pass the returned root as ScanSecrets/Review's dir parameter explicitly
// (exercising the parameter itself); the t.Chdir is redundant with that but
// kept so ScanSecrets/gitDiff's git invocation still resolves correctly for
// any test that ends up calling git without going through dir (there are
// none today, but this keeps the fixture robust either way).
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

	result := ScanSecrets(context.Background(), root, "base", "HEAD")

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

	result := ScanSecrets(context.Background(), root, "base", "HEAD")

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

// TestScanSecrets_IgnoresUnquotedCodeAssignments covers the false-positive
// shapes found when this scan was smoke-tested against modulex's own commit
// history: plain code and narrative comments that merely contain the word
// "key", "token", "password", or "secret" followed by ':'/'=' but never
// assign a quoted string literal. See strictGenericSecretPattern's doc
// comment for why the quote requirement rules each of these out.
func TestScanSecrets_IgnoresUnquotedCodeAssignments(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "app.go", strings.Join([]string{
		"package app",
		"",
		`func genToken() string { token = hex.EncodeToString(buf); return token }`,
		`var ServiceKey = modulex.NewKey[Sender]("notification.Service")`,
		`var _ = secretService{APIKey: secretValue}`,
		"// Wrong scope with the right token: denied.",
		"",
	}, "\n"))
	runGit(t, root, "commit", "--quiet", "-am", "add code that merely mentions key/token/secret")

	result := ScanSecrets(context.Background(), root, "base", "HEAD")

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (no quoted secret-shaped literal was added); Message: %s", result.Status, result.Message)
	}
}

// TestScanSecrets_NosecretMarkerSuppressesFinding covers the escape hatch
// for a line that is secret-shaped on purpose, e.g. a test fixture
// asserting redaction behavior.
func TestScanSecrets_NosecretMarkerSuppressesFinding(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "fixture_test.go", "package fixture\n")
	runGit(t, root, "add", "fixture_test.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "fixture_test.go", "package fixture\n\n"+
		`var fakeToken = "api_key=supersecretvalue123" // nosecret: test fixture`+"\n")
	runGit(t, root, "commit", "--quiet", "-am", "add annotated test fixture")

	result := ScanSecrets(context.Background(), root, "base", "HEAD")

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (line carries a nosecret marker); Message: %s", result.Status, result.Message)
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

	result := ScanSecrets(context.Background(), root, "base", "HEAD")

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (pre-existing secret is outside the diff); Message: %s", result.Status, result.Message)
	}
}

func TestScanSecrets_BadRefReturnsUnavailable(t *testing.T) {
	root := newTestRepo(t)

	result := ScanSecrets(context.Background(), root, "this-ref-does-not-exist", "HEAD")

	if result.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", result.Status)
	}
	if result.Reason == "" {
		t.Error("Reason is empty, want an explanation naming the failed diff computation")
	}
}

// TestScanSecrets_UsesDirRegardlessOfProcessCwd is a decisive regression
// test for dir actually being honored: the process's cwd is a plain,
// non-git directory, so if ScanSecrets ever fell back to it instead of
// using dir, `git diff` would fail outright ("not a git repository") —
// there is no git history at cwd for it to silently scan by mistake. A
// separate real repo at dir carries the actual base/HEAD commits and a
// secret-shaped added line; the scan must find it there.
func TestScanSecrets_UsesDirRegardlessOfProcessCwd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	nonGitCwd := t.TempDir()
	t.Chdir(nonGitCwd)

	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	writeFile(t, repo, "config.go", "package config\n")
	runGit(t, repo, "add", "config.go")
	runGit(t, repo, "commit", "--quiet", "-m", "base")
	runGit(t, repo, "branch", "base")
	writeFile(t, repo, "config.go", "package config\n\nconst apiKey = \"api_key=supersecretvalue123\"\n")
	runGit(t, repo, "commit", "--quiet", "-am", "oops, hardcoded a key")

	result := ScanSecrets(context.Background(), repo, "base", "HEAD")

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail (dir must be honored regardless of the process's cwd); Message: %s, Reason: %s", result.Status, result.Message, result.Reason)
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

func TestRedactLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantFound bool
		wantGone  string
	}{
		{
			name:      "quoted generic secret assignment matches",
			line:      `apiKey := "supersecretvalue123"`,
			wantFound: true,
			wantGone:  "supersecretvalue123",
		},
		{
			name:      "unquoted code assignment does not match",
			line:      "token = hex.EncodeToString(buf)",
			wantFound: false,
		},
		{
			name:      "compound Key identifier without a quoted value does not match",
			line:      `var ServiceKey = modulex.NewKey[Sender]("notification.Service")`,
			wantFound: false,
		},
		{
			name:      "struct literal with identifier value does not match",
			line:      "&secretService{APIKey: secretValue}",
			wantFound: false,
		},
		{
			name:      "narrative comment does not match",
			line:      "// Wrong scope with the right token: denied.",
			wantFound: false,
		},
		{
			name:      "GitHub token prefix still matches via high-confidence patterns",
			line:      "using token ghp_1234567890abcdefghijklmnopqrstuvwxyz",
			wantFound: true,
			wantGone:  "ghp_1234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := redactLine(tt.line)
			if found != tt.wantFound {
				t.Fatalf("redactLine(%q) found = %v, want %v (got %q)", tt.line, found, tt.wantFound, got)
			}
			if tt.wantGone != "" && strings.Contains(got, tt.wantGone) {
				t.Fatalf("redactLine(%q) = %q, still contains %q", tt.line, got, tt.wantGone)
			}
		})
	}
}

func TestGitDiffRejectsMalformedRefs(t *testing.T) {
	root := newTestRepo(t)

	cases := []struct {
		name    string
		baseRef string
		headRef string
	}{
		{"empty baseRef", "", "HEAD"},
		{"empty headRef", "HEAD", ""},
		{"dash-prefixed baseRef", "-evil", "HEAD"},
		{"dash-prefixed headRef", "HEAD", "-evil"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := gitDiff(context.Background(), root, tt.baseRef, tt.headRef)
			if err == nil {
				t.Fatal("gitDiff accepted a malformed ref")
			}
		})
	}
}

func TestHasNosecretMarker(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: `token := "value" // nosecret: test fixture`, want: true},
		{line: `token := "value" // NOSECRET`, want: true},
		{line: `token := "value"`, want: false},
		{line: "an ordinary line", want: false},
	}

	for _, tt := range tests {
		if got := hasNosecretMarker(tt.line); got != tt.want {
			t.Errorf("hasNosecretMarker(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// TestScanSecrets_IdentifierLikeValuesAreNotFlagged: a short chain of
// alphanumeric words joined by -/_ assigned to a key/token-named variable is
// a human-readable identifier ("pr-42-fix"), not credential material -- found
// in practice as `ticket_key="pr-42-fix"` in a consumer repository's tests.
// A long value keeps being flagged even when separator-joined.
func TestScanSecrets_IdentifierLikeValuesAreNotFlagged(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, root, "base.txt", "base\n")
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")
	writeFile(t, root, "conf.py",
		"ticket_key=\"pr-42-fix\"\n"+
			"branch_token = \"my_branch_v2\"\n")
	runGit(t, root, "add", "conf.py")
	runGit(t, root, "commit", "--quiet", "-m", "identifier-shaped values")

	result := ScanSecrets(context.Background(), root, "base", "HEAD")
	if result.Status != provenance.StatusPass {
		t.Fatalf("Status = %q, want pass (identifier-like values are not secrets); Message: %s",
			result.Status, result.Message)
	}
}

func TestScanSecrets_LongSeparatorJoinedValueStillFlagged(t *testing.T) {
	root := newTestRepo(t)
	writeFile(t, root, "base.txt", "base\n")
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")
	writeFile(t, root, "conf.py", "api_key = \"prod-4f9a-1c2b-88ee-secret\"\n")
	runGit(t, root, "add", "conf.py")
	runGit(t, root, "commit", "--quiet", "-m", "long separator-joined secret")

	result := ScanSecrets(context.Background(), root, "base", "HEAD")
	if result.Status != provenance.StatusFail {
		t.Fatalf("Status = %q, want fail (>=16 chars stays flagged regardless of separators); Message: %s",
			result.Status, result.Message)
	}
}

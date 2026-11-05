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

// scanCase describes one ScanSecrets scenario: a base commit holding
// baseFiles, a head commit that adds or rewrites headFiles, and the
// expectations on the resulting check result.
type scanCase struct {
	name         string
	baseFiles    map[string]string
	headFiles    map[string]string
	wantStatus   provenance.Status
	wantName     string
	wantCategory string
	msgHas       []string
	msgNot       []string
}

// commitAll writes files into root, stages everything and commits it.
func commitAll(t *testing.T, root string, files map[string]string, msg string) {
	t.Helper()
	for name, content := range files {
		writeFile(t, root, name, content)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "--quiet", "-m", msg)
}

// assertScanResult checks result against the expectations in tt.
func assertScanResult(t *testing.T, tt scanCase, result provenance.VerificationResult) {
	t.Helper()
	if result.Status != tt.wantStatus {
		t.Fatalf("Status = %q, want %q; Message: %s, Reason: %s", result.Status, tt.wantStatus, result.Message, result.Reason)
	}
	if tt.wantName != "" && result.Name != tt.wantName {
		t.Errorf("Name = %q, want %s", result.Name, tt.wantName)
	}
	if tt.wantCategory != "" && string(result.Category) != tt.wantCategory {
		t.Errorf("Category = %q, want %s", result.Category, tt.wantCategory)
	}
	for _, s := range tt.msgHas {
		if !strings.Contains(result.Message, s) {
			t.Errorf("Message = %q, want it to contain %q", result.Message, s)
		}
	}
	for _, s := range tt.msgNot {
		if strings.Contains(result.Message, s) {
			t.Fatalf("Message leaks %q: %s", s, result.Message)
		}
	}
}

// runScanCases builds the base/head repo of each case and asserts on
// ScanSecrets(base, HEAD).
func runScanCases(t *testing.T, tests []scanCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRepo(t)
			commitAll(t, root, tt.baseFiles, "base")
			runGit(t, root, "branch", "base")
			commitAll(t, root, tt.headFiles, "head")

			assertScanResult(t, tt, ScanSecrets(context.Background(), root, "base", "HEAD"))
		})
	}
}

func TestScanSecrets_NoFindings(t *testing.T) {
	tests := []scanCase{
		{
			name:         "benign function added",
			baseFiles:    map[string]string{"app.go": "package app\n"},
			headFiles:    map[string]string{"app.go": "package app\n\nfunc Hello() string { return \"hi\" }\n"},
			wantStatus:   provenance.StatusPass,
			wantName:     "check-secrets",
			wantCategory: "secret_scan",
		},
	}
	runScanCases(t, tests)
}

func TestScanSecrets_FindsSecretAddedLine(t *testing.T) {
	tests := []scanCase{
		{
			name:       "hardcoded api key is flagged and redacted",
			baseFiles:  map[string]string{"config.go": "package config\n"},
			headFiles:  map[string]string{"config.go": "package config\n\nconst apiKey = \"api_key=supersecretvalue123\"\n"},
			wantStatus: provenance.StatusFail,
			msgHas:     []string{"config.go:3", "[REDACTED]"},
			msgNot:     []string{"supersecretvalue123"},
		},
	}
	runScanCases(t, tests)
}

// TestScanSecrets_IgnoresUnquotedCodeAssignments covers the false-positive
// shapes found when this scan was smoke-tested against modulex's own commit
// history: plain code and narrative comments that merely contain the word
// "key", "token", "password", or "secret" followed by ':'/'=' but never
// assign a quoted string literal. See strictGenericSecretPattern's doc
// comment for why the quote requirement rules each of these out.
func TestScanSecrets_IgnoresUnquotedCodeAssignments(t *testing.T) {
	tests := []scanCase{
		{
			name:      "code that merely mentions key/token/secret",
			baseFiles: map[string]string{"app.go": "package app\n"},
			headFiles: map[string]string{"app.go": strings.Join([]string{
				"package app",
				"",
				`func genToken() string { token = hex.EncodeToString(buf); return token }`,
				`var ServiceKey = modulex.NewKey[Sender]("notification.Service")`,
				`var _ = secretService{APIKey: secretValue}`,
				"// Wrong scope with the right token: denied.",
				"",
			}, "\n")},
			wantStatus: provenance.StatusPass,
		},
	}
	runScanCases(t, tests)
}

// TestScanSecrets_NosecretMarkerSuppressesFinding covers the escape hatch
// for a line that is secret-shaped on purpose, e.g. a test fixture
// asserting redaction behavior.
func TestScanSecrets_NosecretMarkerSuppressesFinding(t *testing.T) {
	tests := []scanCase{
		{
			name:      "annotated test fixture line",
			baseFiles: map[string]string{"fixture_test.go": "package fixture\n"},
			headFiles: map[string]string{"fixture_test.go": "package fixture\n\n" +
				`var fakeToken = "api_key=supersecretvalue123" // nosecret: test fixture` + "\n"},
			wantStatus: provenance.StatusPass,
		},
	}
	runScanCases(t, tests)
}

// The secret-shaped line is already present at the base commit, so it must
// not be flagged: ScanSecrets only scans lines *added* between baseRef and
// headRef, not the whole file content at headRef.
func TestScanSecrets_IgnoresPreexistingSecretOutsideDiff(t *testing.T) {
	tests := []scanCase{
		{
			name:       "pre-existing secret outside the diff",
			baseFiles:  map[string]string{"config.go": "package config\n\nconst apiKey = \"api_key=preexistingsecret456\"\n"},
			headFiles:  map[string]string{"config.go": "package config\n\nconst apiKey = \"api_key=preexistingsecret456\"\n\nfunc Unrelated() {}\n"},
			wantStatus: provenance.StatusPass,
		},
	}
	runScanCases(t, tests)
}

func TestScanSecrets_BadRefReturnsUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		baseRef string
	}{
		{name: "nonexistent ref", baseRef: "this-ref-does-not-exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRepo(t)

			result := ScanSecrets(context.Background(), root, tt.baseRef, "HEAD")

			if result.Status != "unavailable" {
				t.Fatalf("Status = %q, want unavailable", result.Status)
			}
			if result.Reason == "" {
				t.Error("Reason is empty, want an explanation naming the failed diff computation")
			}
		})
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
	tests := []struct {
		name       string
		baseSrc    string
		headSrc    string
		wantStatus provenance.Status
	}{
		{
			name:       "secret added in a repo other than the process cwd",
			baseSrc:    "package config\n",
			headSrc:    "package config\n\nconst apiKey = \"api_key=supersecretvalue123\"\n",
			wantStatus: provenance.StatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git not available on PATH")
			}

			nonGitCwd := t.TempDir()
			t.Chdir(nonGitCwd)

			repo := t.TempDir()
			runGit(t, repo, "init", "--quiet")
			runGit(t, repo, "config", "user.email", "test@example.com")
			runGit(t, repo, "config", "user.name", "Test")
			commitAll(t, repo, map[string]string{"config.go": tt.baseSrc}, "base")
			runGit(t, repo, "branch", "base")
			commitAll(t, repo, map[string]string{"config.go": tt.headSrc}, "oops, hardcoded a key")

			result := ScanSecrets(context.Background(), repo, "base", "HEAD")

			if result.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q (dir must be honored regardless of the process's cwd); Message: %s, Reason: %s", result.Status, tt.wantStatus, result.Message, result.Reason)
			}
		})
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
		// L7 regression: an added line whose actual content begins with
		// "++" (no space) renders, after the diff's own leading "+"
		// marker, as "+++<rest>" — which the old prefix-sniffing code
		// matched neither the file-header case ("+++ " requires a space)
		// nor the added-line case (explicitly excluded any line starting
		// with "+++"), so the line was silently dropped and the line
		// counter never advanced for it, misattributing every subsequent
		// added line in the hunk.
		{
			name: "added line content starting with ++ (no space) is still scanned and counted",
			diff: "diff --git a/weird.go b/weird.go\n" +
				"--- a/weird.go\n" +
				"+++ b/weird.go\n" +
				"@@ -0,0 +1,3 @@\n" +
				"+line zero\n" +
				"+++fakeheader content\n" +
				"+line two\n",
			want: []addedLine{
				{file: "weird.go", line: 1, text: "line zero"},
				{file: "weird.go", line: 2, text: "++fakeheader content"},
				{file: "weird.go", line: 3, text: "line two"},
			},
		},
		// L7 regression: an added line whose actual content begins with
		// "++ " (with a space) renders as "+++ <rest>", which DOES match
		// the "+++ " file-header prefix exactly — the old code misparsed
		// it as a bogus file header, corrupting the tracked `file` for
		// every subsequent line in the diff.
		{
			name: "added line content starting with ++ (with space) is not misparsed as a file header",
			diff: "diff --git a/weird2.go b/weird2.go\n" +
				"--- a/weird2.go\n" +
				"+++ b/weird2.go\n" +
				"@@ -0,0 +1,2 @@\n" +
				"+++ sneaky header line\n" +
				"+real added line\n",
			want: []addedLine{
				{file: "weird2.go", line: 1, text: "++ sneaky header line"},
				{file: "weird2.go", line: 2, text: "real added line"},
			},
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
		{line: `token := "value" // NOSECRET: uppercase marker, lowercase reason ok`, want: true},
		{line: `token := "value" //nosecret:no space before reason`, want: true},
		{line: `token := "value"`, want: false},
		{line: "an ordinary line", want: false},
		// L7 regression: a bare "// nosecret" with no reason after the
		// colon must no longer suppress a finding — the marker now
		// requires an audit-trail reason to be present.
		{line: `token := "value" // NOSECRET`, want: false},
		{line: `token := "value" // nosecret:`, want: false},
		{line: `token := "value" // nosecret :   `, want: false},
		// L7 regression: the word "nosecret" occurring anywhere in the
		// line — e.g. as part of an unrelated identifier — must not
		// suppress a finding; only the precise "// nosecret: <reason>"
		// comment form does.
		{line: `nosecretDBPassword := "supersecretvalue123"`, want: false},
		{line: `password := "supersecretvalue123" // this is nosecret-shaped on purpose`, want: false},
	}

	for _, tt := range tests {
		if got := hasNosecretMarker(tt.line); got != tt.want {
			t.Errorf("hasNosecretMarker(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// TestScanSecrets_BareNosecretSubstringNoLongerSuppresses is the L7
// regression test proving the fix end to end: previously, the word
// "nosecret" occurring anywhere in an added line — including inside an
// unrelated identifier — suppressed the finding with no audit trail. A
// secret-shaped value next to a variable name that merely contains
// "nosecret" (not a well-formed "// nosecret: <reason>" comment) must now
// still be flagged.
func TestScanSecrets_BareNosecretSubstringNoLongerSuppresses(t *testing.T) {
	tests := []scanCase{
		{
			name:      "identifier merely contains nosecret",
			baseFiles: map[string]string{"config.go": "package config\n"},
			headFiles: map[string]string{"config.go": "package config\n\n" +
				`const nosecretAPIKey = "api_key=supersecretvalue123"` + "\n"}, // nosecret: fixture proving an identifier containing "nosecret" is not an escape hatch
			wantStatus: provenance.StatusFail,
		},
	}
	runScanCases(t, tests)
}

// TestScanSecrets_IdentifierLikeValuesAreNotFlagged: a short chain of
// alphanumeric words joined by -/_ assigned to a key/token-named variable is
// a human-readable identifier ("pr-42-fix"), not credential material -- found
// in practice as `ticket_key="pr-42-fix"` in a consumer repository's tests.
// A long value keeps being flagged even when separator-joined.
func TestScanSecrets_IdentifierLikeValuesAreNotFlagged(t *testing.T) {
	tests := []scanCase{
		{
			name:      "identifier-shaped values",
			baseFiles: map[string]string{"base.txt": "base\n"},
			headFiles: map[string]string{"conf.py": "ticket_key=\"pr-42-fix\"\n" +
				"branch_token = \"my_branch_v2\"\n"},
			wantStatus: provenance.StatusPass,
		},
	}
	runScanCases(t, tests)
}

func TestScanSecrets_LongSeparatorJoinedValueStillFlagged(t *testing.T) {
	tests := []scanCase{
		{
			name:       "long separator-joined secret",
			baseFiles:  map[string]string{"base.txt": "base\n"},
			headFiles:  map[string]string{"conf.py": "api_key = \"prod-4f9a-1c2b-88ee-secret\"\n"},
			wantStatus: provenance.StatusFail,
		},
	}
	runScanCases(t, tests)
}

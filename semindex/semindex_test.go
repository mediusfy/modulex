package semindex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnose_CorrectRootIsOK(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string // directory name under the worktree
	}{
		{name: "index declares the active worktree", indexName: "codegraph", indexDir: ".codegraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			indexDir := filepath.Join(worktree, tt.indexDir)
			if err := WriteMarkerFile(indexDir, MarkerFileName, worktree); err != nil {
				t.Fatalf("WriteMarkerFile: %v", err)
			}

			d := Diagnose(worktree, indexDir, tt.indexName, DefaultMarkerReader)

			if d.Status != StatusOK {
				t.Fatalf("Status = %q, want %q (diagnosis: %+v)", d.Status, StatusOK, d)
			}
			if d.Name != tt.indexName {
				t.Errorf("Name = %q, want %q", d.Name, tt.indexName)
			}
			if d.WorktreeRoot != worktree {
				t.Errorf("WorktreeRoot = %q, want %q", d.WorktreeRoot, worktree)
			}
			if d.IndexRoot != worktree {
				t.Errorf("IndexRoot = %q, want %q", d.IndexRoot, worktree)
			}
			if d.Remediation == "" {
				t.Error("Remediation is empty, want a confirmation message")
			}
		})
	}
}

func TestDiagnose_StaleRootIsMismatch(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string
	}{
		{name: "index declares another checkout", indexName: "tokensave", indexDir: ".tokensave"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			staleRoot := t.TempDir() // a different, "other checkout" root
			indexDir := filepath.Join(worktree, tt.indexDir)
			if err := WriteMarkerFile(indexDir, MarkerFileName, staleRoot); err != nil {
				t.Fatalf("WriteMarkerFile: %v", err)
			}

			d := Diagnose(worktree, indexDir, tt.indexName, DefaultMarkerReader)

			if d.Status != StatusMismatch {
				t.Fatalf("Status = %q, want %q (diagnosis: %+v)", d.Status, StatusMismatch, d)
			}
			if d.IndexRoot != staleRoot {
				t.Errorf("IndexRoot = %q, want %q", d.IndexRoot, staleRoot)
			}
			if !strings.Contains(d.Remediation, staleRoot) {
				t.Errorf("Remediation %q does not name the stale index root %q", d.Remediation, staleRoot)
			}
			if !strings.Contains(d.Remediation, worktree) {
				t.Errorf("Remediation %q does not name the active worktree root %q", d.Remediation, worktree)
			}
		})
	}
}

func TestDiagnose_MissingIndex(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string
	}{
		{name: "index directory never created", indexName: "codegraph", indexDir: ".codegraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			indexDir := filepath.Join(worktree, tt.indexDir) // never created

			d := Diagnose(worktree, indexDir, tt.indexName, DefaultMarkerReader)

			if d.Status != StatusMissing {
				t.Fatalf("Status = %q, want %q (diagnosis: %+v)", d.Status, StatusMissing, d)
			}
			if d.IndexRoot != "" {
				t.Errorf("IndexRoot = %q, want empty for a missing index", d.IndexRoot)
			}
			if !strings.Contains(d.Remediation, indexDir) {
				t.Errorf("Remediation %q does not name the missing index dir %q", d.Remediation, indexDir)
			}
		})
	}
}

func TestDiagnose_MultipleCheckouts(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string
	}{
		{name: "two checkouts with their own indexes", indexName: "codegraph", indexDir: ".codegraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkoutA := t.TempDir()
			checkoutB := t.TempDir()

			indexA := filepath.Join(checkoutA, tt.indexDir)
			if err := WriteMarkerFile(indexA, MarkerFileName, checkoutA); err != nil {
				t.Fatalf("WriteMarkerFile(A): %v", err)
			}
			indexB := filepath.Join(checkoutB, tt.indexDir)
			if err := WriteMarkerFile(indexB, MarkerFileName, checkoutB); err != nil {
				t.Fatalf("WriteMarkerFile(B): %v", err)
			}

			// Each checkout's own index, diagnosed against its own worktree
			// root, is OK.
			dA := Diagnose(checkoutA, indexA, tt.indexName, DefaultMarkerReader)
			if dA.Status != StatusOK {
				t.Errorf("checkout A diagnosed against its own index: Status = %q, want %q", dA.Status, StatusOK)
			}
			dB := Diagnose(checkoutB, indexB, tt.indexName, DefaultMarkerReader)
			if dB.Status != StatusOK {
				t.Errorf("checkout B diagnosed against its own index: Status = %q, want %q", dB.Status, StatusOK)
			}

			// This is the observed real-world bug: checkout A's active
			// worktree, but consulting checkout B's index (e.g. because a
			// long-lived MCP server process still has B loaded). That must
			// report a mismatch.
			dCross := Diagnose(checkoutA, indexB, tt.indexName, DefaultMarkerReader)
			if dCross.Status != StatusMismatch {
				t.Fatalf("checkout A worktree against checkout B's index: Status = %q, want %q", dCross.Status, StatusMismatch)
			}
			if dCross.IndexRoot != checkoutB {
				t.Errorf("IndexRoot = %q, want %q", dCross.IndexRoot, checkoutB)
			}
			if dCross.WorktreeRoot != checkoutA {
				t.Errorf("WorktreeRoot = %q, want %q", dCross.WorktreeRoot, checkoutA)
			}
		})
	}
}

// TestDiagnose_UnverifiableNoMarkerNoReader covers every way an existing index directory can
// fail to yield a declared root: no reader at all, a reader that reports no
// root, and a reader that errors. None may be confused with OK or Mismatch.
func TestDiagnose_UnverifiableNoMarkerNoReader(t *testing.T) {
	tests := []struct {
		name   string
		reader RootReader
	}{
		{name: "no marker file and no reader", reader: nil},
		{name: "reader says not ok", reader: func(string) (string, bool, error) { return "", false, nil }},
		{name: "reader error", reader: func(string) (string, bool, error) { return "", false, errors.New("boom") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			indexDir := filepath.Join(worktree, ".codegraph")
			if err := os.MkdirAll(indexDir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			// No marker file is written.

			d := Diagnose(worktree, indexDir, "codegraph", tt.reader)

			if d.Status != StatusUnverifiable {
				t.Fatalf("Status = %q, want %q (diagnosis: %+v)", d.Status, StatusUnverifiable, d)
			}
			if d.Status == StatusOK || d.Status == StatusMismatch {
				t.Fatalf("StatusUnverifiable must never be confused with OK or Mismatch, got %q", d.Status)
			}
			if d.IndexRoot != "" {
				t.Errorf("IndexRoot = %q, want empty when unverifiable", d.IndexRoot)
			}
		})
	}
}

func TestDiagnose_SymlinkNormalization(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string
	}{
		{name: "worktree expressed via symlink", indexName: "codegraph", indexDir: ".codegraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			real := t.TempDir()
			linkParent := t.TempDir()
			link := filepath.Join(linkParent, "link-to-real")
			if err := os.Symlink(real, link); err != nil {
				t.Skipf("symlink not supported in this environment: %v", err)
			}

			indexDir := filepath.Join(real, tt.indexDir)
			// The index declares the *real* path; the "active worktree" is
			// expressed via the symlink. Textually these differ, but they
			// resolve to the same path on disk.
			if err := WriteMarkerFile(indexDir, MarkerFileName, real); err != nil {
				t.Fatalf("WriteMarkerFile: %v", err)
			}

			d := Diagnose(link, indexDir, tt.indexName, DefaultMarkerReader)

			if d.Status != StatusOK {
				t.Fatalf("Status = %q, want %q after symlink resolution (diagnosis: %+v)", d.Status, StatusOK, d)
			}
		})
	}
}

func TestDiagnose_MismatchRemediationOnlyContainsRootsAndGenericText(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		indexDir  string
	}{
		{name: "mismatch remediation text", indexName: "codegraph", indexDir: ".codegraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			staleRoot := t.TempDir()
			indexDir := filepath.Join(worktree, tt.indexDir)
			if err := WriteMarkerFile(indexDir, MarkerFileName, staleRoot); err != nil {
				t.Fatalf("WriteMarkerFile: %v", err)
			}

			d := Diagnose(worktree, indexDir, tt.indexName, DefaultMarkerReader)
			if d.Status != StatusMismatch {
				t.Fatalf("Status = %q, want %q", d.Status, StatusMismatch)
			}

			// The remediation must be built only from the two root strings
			// plus static guidance text: reconstructing it from the known
			// format string and the two roots must reproduce it exactly. A
			// remediation string containing anything else (e.g. index dir
			// contents, an unrelated placeholder) would fail this equality.
			want := "this index was built against " + staleRoot +
				" but the active worktree is " + worktree +
				"; re-run the index tool's sync/init command in this worktree, or point the tool at the correct root."
			if d.Remediation != want {
				t.Errorf("Remediation = %q, want exactly %q", d.Remediation, want)
			}

			// Sanity: it does not leak the index directory path itself (only
			// the declared *root*, which is a different string in this test)
			// or any value never passed to Diagnose.
			if strings.Contains(d.Remediation, indexDir) {
				t.Errorf("Remediation %q unexpectedly contains the index directory path", d.Remediation)
			}
		})
	}
}

func TestMarkerFileReader_NoMarkerFile(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "directory without a marker file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root, ok, err := DefaultMarkerReader(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Errorf("ok = true, want false when no marker file exists (root=%q)", root)
			}
		})
	}
}

func TestMarkerFileReader_BlankMarkerFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "only blank lines", content: "\n\n  \n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, MarkerFileName), []byte(tt.content), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			root, ok, err := DefaultMarkerReader(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok {
				t.Errorf("ok = true, want false for a marker file with no non-empty line (root=%q)", root)
			}
		})
	}
}

func TestMarkerFileReader_TrimsAndTakesFirstLine(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantRoot string
	}{
		{name: "padded first line wins", content: "  /some/root  \nignored second line\n", wantRoot: "/some/root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, MarkerFileName), []byte(tt.content), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			root, ok, err := DefaultMarkerReader(dir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok {
				t.Fatal("ok = false, want true")
			}
			if root != tt.wantRoot {
				t.Errorf("root = %q, want %q", root, tt.wantRoot)
			}
		})
	}
}

func TestResolveWorktreeRoot_GitRepo(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
	}{
		{name: "git repo with no fallback", fallback: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git not available on PATH")
			}
			dir := t.TempDir()
			if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
				t.Skipf("git init failed: %v (%s)", err, out)
			}

			root, err := ResolveWorktreeRoot(dir, tt.fallback)
			if err != nil {
				t.Fatalf("ResolveWorktreeRoot: %v", err)
			}

			gotResolved := normalizeRoot(root)
			wantResolved := normalizeRoot(dir)
			if gotResolved != wantResolved {
				t.Errorf("ResolveWorktreeRoot = %q (resolved %q), want resolved %q", root, gotResolved, wantResolved)
			}
		})
	}
}

func TestResolveWorktreeRoot_FallsBackWhenNotAGitRepo(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "non-git directory uses the fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir() // deliberately not a git repository
			fallback := t.TempDir()

			root, err := ResolveWorktreeRoot(dir, fallback)
			if err != nil {
				t.Fatalf("ResolveWorktreeRoot: %v", err)
			}
			if root != fallback {
				t.Errorf("root = %q, want fallback %q", root, fallback)
			}
		})
	}
}

func TestResolveWorktreeRoot_ErrorsWithoutFallback(t *testing.T) {
	tests := []struct {
		name     string
		fallback string
	}{
		{name: "non-git directory and no fallback", fallback: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir() // deliberately not a git repository

			_, err := ResolveWorktreeRoot(dir, tt.fallback)
			if err == nil {
				t.Fatal("expected an error when not a git repo and no fallback given")
			}
		})
	}
}

func TestEvaluateSeverity(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		policy Policy
		want   Severity
	}{
		{"ok is always ok", StatusOK, Policy{}, SeverityOK},
		{"ok ignores policy", StatusOK, Policy{TreatMismatchAsFailure: true, TreatMissingAsFailure: true, TreatUnverifiableAsFailure: true}, SeverityOK},
		{"mismatch warns by default", StatusMismatch, Policy{}, SeverityWarn},
		{"mismatch blocks when configured", StatusMismatch, Policy{TreatMismatchAsFailure: true}, SeverityBlock},
		{"missing warns by default", StatusMissing, Policy{}, SeverityWarn},
		{"missing blocks when configured", StatusMissing, Policy{TreatMissingAsFailure: true}, SeverityBlock},
		{"unverifiable warns by default", StatusUnverifiable, Policy{}, SeverityWarn},
		{"unverifiable blocks when configured", StatusUnverifiable, Policy{TreatUnverifiableAsFailure: true}, SeverityBlock},
		{"unrelated flags do not escalate mismatch", StatusMismatch, Policy{TreatMissingAsFailure: true, TreatUnverifiableAsFailure: true}, SeverityWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateSeverity(Diagnosis{Status: tt.status}, tt.policy)
			if got != tt.want {
				t.Errorf("EvaluateSeverity(%q, %+v) = %q, want %q", tt.status, tt.policy, got, tt.want)
			}
		})
	}
}

func TestReadIndexRoot_NilReaderIsUndetermined(t *testing.T) {
	tests := []struct {
		name      string
		indexName string
		dir       string
	}{
		{name: "nil reader", indexName: "codegraph", dir: "/some/dir"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ir := ReadIndexRoot(tt.indexName, tt.dir, nil)
			if ir.Determined {
				t.Errorf("Determined = true with a nil reader, want false")
			}
			if ir.Name != tt.indexName || ir.Dir != tt.dir {
				t.Errorf("ReadIndexRoot did not preserve Name/Dir: %+v", ir)
			}
		})
	}
}

func TestWriteMarkerFile_CreatesDirAndFile(t *testing.T) {
	tests := []struct {
		name     string
		subdir   []string
		declared string
	}{
		{name: "creates nested directory and file", subdir: []string{"nested", ".codegraph"}, declared: "/declared/root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			indexDir := filepath.Join(append([]string{base}, tt.subdir...)...)

			if err := WriteMarkerFile(indexDir, MarkerFileName, tt.declared); err != nil {
				t.Fatalf("WriteMarkerFile: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(indexDir, MarkerFileName))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if strings.TrimSpace(string(data)) != tt.declared {
				t.Errorf("marker file content = %q, want %q", string(data), tt.declared)
			}
		})
	}
}

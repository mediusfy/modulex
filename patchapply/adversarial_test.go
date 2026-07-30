package patchapply

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
)

// dirEntries lists every regular file under root, relative to root, for
// asserting "nothing was written anywhere" after a rejected batch. It
// intentionally does not filter anything out, so a stray write anywhere
// under root (not just at the attacked path) would be caught.
func dirEntries(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %q: %v", root, err)
	}
	return out
}

// TestApply_PathTraversalRejectedWithZeroSideEffects proves the "reject
// the whole batch, zero side effects" property for a ".." traversal
// attempt riding alongside an otherwise-legitimate change in the same
// batch.
func TestApply_PathTraversalRejectedWithZeroSideEffects(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "target")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	before := dirEntries(t, parent)

	_, err := Apply(dir, []FileChange{
		{Path: "legit.txt", NewContent: []byte("should never land")},
		{Path: "../../etc/passwd", NewContent: []byte("pwned")},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a batch containing a path-traversal attempt")
	}
	if !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("expected ErrPathTraversal, got: %v", err)
	}

	after := dirEntries(t, parent)
	if len(after) != len(before) {
		t.Fatalf("batch should have zero side effects; before=%v after=%v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("file %q changed despite rejected batch: before=%q after=%q", k, v, after[k])
		}
	}
	if _, ok := after["legit.txt"]; ok {
		t.Fatal("the OTHER, legitimate change in the same batch must not have been written either")
	}

	// And, defense in depth: confirm nothing escaped upward out of parent
	// at all (e.g. into a real /etc/passwd, which would be catastrophic
	// and is exactly what this test exists to rule out).
	if _, statErr := os.Stat(filepath.Join(parent, "..", "etc")); statErr == nil {
		t.Fatal("traversal path component should never have been created")
	}
}

// TestApply_AbsolutePathRejectedWithZeroSideEffects mirrors the traversal
// test for an absolute path instead of "..".
func TestApply_AbsolutePathRejectedWithZeroSideEffects(t *testing.T) {
	dir := t.TempDir()
	before := dirEntries(t, dir)

	victim := filepath.Join(t.TempDir(), "should-not-exist.txt")

	_, err := Apply(dir, []FileChange{
		{Path: "legit.txt", NewContent: []byte("should never land")},
		{Path: victim, NewContent: []byte("pwned")},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a batch containing an absolute path")
	}
	if !errors.Is(err, ErrAbsolutePath) {
		t.Fatalf("expected ErrAbsolutePath, got: %v", err)
	}

	after := dirEntries(t, dir)
	if len(after) != len(before) {
		t.Fatalf("batch should have zero side effects inside targetDir; before=%v after=%v", before, after)
	}
	if _, statErr := os.Stat(victim); !os.IsNotExist(statErr) {
		t.Fatalf("absolute path target must never have been written, stat err = %v", statErr)
	}
}

// TestApply_SymlinkEscapeRejected proves a symlink planted inside
// targetDir that points outside it is caught even though the file at the
// end of the symlinked path does not exist yet.
func TestApply_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation semantics differ on windows")
	}

	outside := t.TempDir()
	dir := t.TempDir()

	// dir/escape -> outside (a symlink to a directory entirely outside
	// targetDir).
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := Apply(dir, []FileChange{
		{Path: "escape/newfile.txt", NewContent: []byte("pwned")},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a path that escapes targetDir via a symlink")
	}
	if !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("expected ErrPathTraversal, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(outside, "newfile.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("file must never have been written through the symlink, stat err = %v", statErr)
	}
}

// TestApply_UnrelatedDirtyWorktreeChangeIsPreserved is the core
// "preserves unrelated dirty-worktree changes" acceptance-criterion test:
// a patch computed against a known baseline must not clobber a human's
// edit that landed on disk after the baseline was captured but before
// Apply runs.
func TestApply_UnrelatedDirtyWorktreeChangeIsPreserved(t *testing.T) {
	dir := t.TempDir()
	baseline := []byte("baseline content the patch was computed against")
	writeFile(t, dir, "shared.txt", baseline)

	// A human (or an unrelated process) edits the file after the baseline
	// was captured, before Apply is ever called.
	unrelatedEdit := []byte("a human's unrelated in-progress edit")
	writeFile(t, dir, "shared.txt", unrelatedEdit)

	_, err := Apply(dir, []FileChange{
		{
			Path:                 "shared.txt",
			NewContent:           []byte("the patch's intended new content"),
			ExpectedPriorContent: baseline,
		},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a batch whose ExpectedPriorContent no longer matches disk")
	}
	if !errors.Is(err, ErrPriorContentMismatch) {
		t.Fatalf("expected ErrPriorContentMismatch, got: %v", err)
	}

	current, ok := readFile(t, dir, "shared.txt")
	if !ok {
		t.Fatal("shared.txt should still exist")
	}
	if !bytes.Equal(current, unrelatedEdit) {
		t.Fatalf("unrelated edit must be preserved: got %q, want %q", current, unrelatedEdit)
	}
}

// TestApply_PartialFailureRollsBackAllPriorWrites engineers a REAL
// OS-level failure partway through a 3-change batch (the 2nd change's
// parent directory has its write permission removed, so the temp-file
// create genuinely fails with a permission error — nothing mocked or
// faked) and asserts all three files end up in their pre-Apply state,
// including the 1st file, whose write had already succeeded before the
// 2nd change failed.
func TestApply_PartialFailureRollsBackAllPriorWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks, which this test depends on")
	}

	dir := t.TempDir()
	writeFile(t, dir, "one.txt", []byte("one-original"))
	writeFile(t, dir, "three.txt", []byte("three-original"))

	lockedDir := filepath.Join(dir, "locked")
	if err := os.Mkdir(lockedDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Remove write permission on the directory: an existing path inside it
	// can still be *stat*ed (traversal only needs the execute bit, which
	// 0o555 retains), so Apply's step-4 read phase for a not-yet-existing
	// file inside it succeeds cleanly — but *creating* a new file inside it
	// fails with a genuine permission error at write time, which is
	// exactly the partial-failure scenario this test needs: a failure that
	// only manifests during the write phase, after earlier changes in the
	// batch already succeeded.
	if err := os.Chmod(lockedDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	_, err := Apply(dir, []FileChange{
		{Path: "one.txt", NewContent: []byte("one-updated"), ExpectedPriorContent: []byte("one-original")},
		{Path: "locked/two.txt", NewContent: []byte("two-new")},
		{Path: "three.txt", NewContent: []byte("three-updated"), ExpectedPriorContent: []byte("three-original")},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to fail when the 2nd change's directory is unwritable")
	}

	// Restore permissions before reading back, in case the failure somehow
	// left things in a state where Chmod above wasn't reached (defensive;
	// the deferred Cleanup handles the normal case).
	_ = os.Chmod(lockedDir, 0o755)

	if b, ok := readFile(t, dir, "one.txt"); !ok || string(b) != "one-original" {
		t.Fatalf("one.txt should have been rolled back to its original content: got %q, ok=%v", b, ok)
	}
	if _, ok := readFile(t, dir, "locked/two.txt"); ok {
		t.Fatal("locked/two.txt should never have been created")
	}
	if b, ok := readFile(t, dir, "three.txt"); !ok || string(b) != "three-original" {
		t.Fatalf("three.txt must remain untouched (never reached): got %q, ok=%v", b, ok)
	}
}

// TestApply_DeleteWithoutAnyBrokerIsAlwaysRejected asserts the precise
// documented rule: a Delete change with NO Broker configured at all is
// always rejected, full stop — there is no way to delete without going
// through approval.
func TestApply_DeleteWithoutAnyBrokerIsAlwaysRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", []byte("still here"))

	_, err := Apply(dir, []FileChange{
		{Path: "keep.txt", Delete: true},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected Apply to reject a Delete with no Broker configured")
	}
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got: %v", err)
	}
	if b, ok := readFile(t, dir, "keep.txt"); !ok || string(b) != "still here" {
		t.Fatalf("keep.txt must still exist untouched: got %q, ok=%v", b, ok)
	}
}

// TestApply_DeleteWithUnapprovedScopeIsRejected asserts a Broker being
// present is not enough on its own: the specific Scope presented must
// actually be approved.
func TestApply_DeleteWithUnapprovedScopeIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", []byte("still here"))

	broker := approval.NewBroker()
	// Grant an approval for a DIFFERENT resource than the one Apply will
	// be asked to check.
	if _, err := broker.Grant(approval.Scope{Action: "delete", Resource: "other.txt"}, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	_, err := Apply(dir, []FileChange{
		{Path: "keep.txt", Delete: true},
	}, ApplyOptions{
		Broker: broker,
		Scope:  approval.Scope{Action: "delete", Resource: "keep.txt"},
	})
	if err == nil {
		t.Fatal("expected Apply to reject a Delete whose scope was not approved")
	}
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got: %v", err)
	}
	if b, ok := readFile(t, dir, "keep.txt"); !ok || string(b) != "still here" {
		t.Fatalf("keep.txt must still exist untouched: got %q, ok=%v", b, ok)
	}
}

// TestApply_SecretRedactedInPriorContentMismatchError engineers a
// prior-content-mismatch failure where the on-disk (actual) content
// contains a secret-shaped value, and asserts the resulting error string
// does not contain the raw secret.
func TestApply_SecretRedactedInPriorContentMismatchError(t *testing.T) {
	dir := t.TempDir()
	fakeToken := "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	actualContent := []byte("config line before\n" + fakeToken + "\nconfig line after")
	writeFile(t, dir, "config.txt", actualContent)

	_, err := Apply(dir, []FileChange{
		{
			Path:                 "config.txt",
			NewContent:           []byte("new content"),
			ExpectedPriorContent: []byte("this does not match what's on disk"),
		},
	}, ApplyOptions{})
	if err == nil {
		t.Fatal("expected a prior-content mismatch error")
	}
	if !errors.Is(err, ErrPriorContentMismatch) {
		t.Fatalf("expected ErrPriorContentMismatch, got: %v", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte(fakeToken)) {
		t.Fatalf("error message leaked the raw secret-shaped token: %v", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte(redactionMarker)) {
		t.Fatalf("expected error message to contain the redaction marker, got: %v", err)
	}
}

// TestVerify_ReportsDriftAfterReModificationPostRollback exercises the
// "repeat verification" requirement explicitly: Verify is clean
// immediately after Rollback, then reports drift (naming the file) once
// the file is modified again.
func TestVerify_ReportsDriftAfterReModificationPostRollback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "watched.txt", []byte("original"))

	journal, err := Apply(dir, []FileChange{
		{Path: "watched.txt", NewContent: []byte("changed"), ExpectedPriorContent: []byte("original")},
	}, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := Rollback(dir, journal); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := Verify(dir, journal); err != nil {
		t.Fatalf("Verify should report no drift right after rollback: %v", err)
	}

	writeFile(t, dir, "watched.txt", []byte("drifted"))
	err = Verify(dir, journal)
	if err == nil {
		t.Fatal("Verify should report drift after a post-rollback modification")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("watched.txt")) {
		t.Fatalf("Verify error should name the drifted file: %v", err)
	}
}

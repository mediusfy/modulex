package patchapply

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/provenance"
)

// writeFile is a small test helper: writes content to targetDir/rel,
// creating parent directories as needed, and fails the test on error.
func writeFile(t *testing.T, targetDir, rel string, content []byte) {
	t.Helper()
	full := filepath.Join(targetDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// readFile is a small test helper: reads targetDir/rel, returning (nil,
// false) if it does not exist.
func readFile(t *testing.T, targetDir, rel string) ([]byte, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(targetDir, rel))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false
		}
		t.Fatalf("ReadFile: %v", err)
	}
	return b, true
}

func TestApply_SuccessThenRollbackThenVerify(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "existing.txt", []byte("original content"))

	changes := []FileChange{
		{
			Path:                 "existing.txt",
			NewContent:           []byte("updated content"),
			ExpectedPriorContent: []byte("original content"),
		},
		{
			Path:       "new/nested/file.txt",
			NewContent: []byte("brand new"),
		},
	}

	journal, err := Apply(dir, changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(journal.Entries) != 2 {
		t.Fatalf("expected 2 journal entries, got %d", len(journal.Entries))
	}

	if b, ok := readFile(t, dir, "existing.txt"); !ok || string(b) != "updated content" {
		t.Fatalf("existing.txt = %q, %v; want %q, true", b, ok, "updated content")
	}
	if b, ok := readFile(t, dir, "new/nested/file.txt"); !ok || string(b) != "brand new" {
		t.Fatalf("new/nested/file.txt = %q, %v; want %q, true", b, ok, "brand new")
	}

	// Rolling back "new/nested/file.txt" means deleting a path Apply
	// created, which (per RollbackOptions) requires an approved grant for
	// the scope Rollback derives from it, exactly like an Apply-side
	// Delete.
	rollbackBroker := approval.NewBroker()
	rollbackScope := BatchScope("delete", []string{"new/nested/file.txt"})
	if _, err := rollbackBroker.Grant(rollbackScope, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := Rollback(dir, journal, RollbackOptions{Broker: rollbackBroker, Scope: approval.Scope{Action: "delete"}}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if b, ok := readFile(t, dir, "existing.txt"); !ok || string(b) != "original content" {
		t.Fatalf("after rollback, existing.txt = %q, %v; want %q, true", b, ok, "original content")
	}
	// The file that did not exist before Apply must be gone entirely, not
	// left behind as an empty file.
	if b, ok := readFile(t, dir, "new/nested/file.txt"); ok {
		t.Fatalf("after rollback, new/nested/file.txt should not exist, found %q", b)
	} else if b != nil {
		t.Fatalf("after rollback, expected nil content for absent file, got %q", b)
	}
	// The directory chain created for the new file should also be gone.
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatalf("expected new/ directory to be removed by rollback, stat err = %v", err)
	}

	if err := Verify(dir, journal); err != nil {
		t.Fatalf("Verify after rollback: %v", err)
	}
}

func TestVerify_DetectsDriftAfterRollback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", []byte("v1"))

	journal, err := Apply(dir, []FileChange{
		{Path: "a.txt", NewContent: []byte("v2"), ExpectedPriorContent: []byte("v1")},
	}, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := Rollback(dir, journal, RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := Verify(dir, journal); err != nil {
		t.Fatalf("Verify immediately after rollback should be clean: %v", err)
	}

	// Simulate someone modifying the file again after rollback.
	writeFile(t, dir, "a.txt", []byte("modified after rollback"))

	err = Verify(dir, journal)
	if err == nil {
		t.Fatal("Verify should report drift after a post-rollback modification")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("a.txt")) {
		t.Fatalf("Verify error should name the drifted file, got: %v", err)
	}
}

func TestApply_DeleteWithApprovedBrokerSucceeds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "gone.txt", []byte("bye"))

	broker := approval.NewBroker()
	// Resource is derived by Apply from the batch itself (via BatchScope),
	// not trusted from an independently-supplied string — grant for the
	// exact scope Apply will actually check.
	scope := BatchScope("delete", []string{"gone.txt"})
	if _, err := broker.Grant(scope, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "gone.txt", Delete: true, ExpectedPriorContent: []byte("bye")},
	}, ApplyOptions{Broker: broker, Scope: approval.Scope{Action: "delete"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := readFile(t, dir, "gone.txt"); ok {
		t.Fatal("gone.txt should have been deleted")
	}

	if err := Rollback(dir, journal, RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if b, ok := readFile(t, dir, "gone.txt"); !ok || string(b) != "bye" {
		t.Fatalf("after rollback, gone.txt = %q, %v; want %q, true", b, ok, "bye")
	}
}

func TestApply_DeleteOfAlreadyAbsentPathIsNoop(t *testing.T) {
	dir := t.TempDir()

	broker := approval.NewBroker()
	scope := BatchScope("delete", []string{"never-existed.txt"})
	if _, err := broker.Grant(scope, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "never-existed.txt", Delete: true},
	}, ApplyOptions{Broker: broker, Scope: approval.Scope{Action: "delete"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(journal.Entries) != 1 || journal.Entries[0].Outcome != OutcomeNoop {
		t.Fatalf("expected a single OutcomeNoop entry, got %+v", journal.Entries)
	}
}

func TestApply_ProvenanceStatusUsedForApproval(t *testing.T) {
	// Sanity check that an unapproved scope really does return
	// provenance.StatusApprovalRequired from the broker itself, which is
	// what checkApproval relies on.
	broker := approval.NewBroker()
	status := broker.Check(approval.Scope{Action: "delete", Resource: "x"})
	if status != provenance.StatusApprovalRequired {
		t.Fatalf("expected StatusApprovalRequired for a fresh broker, got %s", status)
	}
}

func TestJournal_StringNeverIncludesContent(t *testing.T) {
	dir := t.TempDir()
	secretContent := []byte("token=abcdef123456")
	writeFile(t, dir, "secret.txt", secretContent)

	journal, err := Apply(dir, []FileChange{
		{Path: "secret.txt", NewContent: []byte("new content, also secret token=zzzzzzzzzz")},
	}, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	s := journal.String()
	if bytes.Contains([]byte(s), secretContent) {
		t.Fatalf("Journal.String() must never include file content, got: %s", s)
	}
	if bytes.Contains([]byte(s), []byte("abcdef123456")) || bytes.Contains([]byte(s), []byte("zzzzzzzzzz")) {
		t.Fatalf("Journal.String() leaked content bytes: %s", s)
	}
}

// --- M10: file mode preservation ---

// TestApply_PreservesOriginalFileModeOnWrite is a regression test for a
// real bug found in review: writeViaRename used to write through
// os.CreateTemp (which always creates at mode 0600) and rename straight
// over the original file, so every patched file silently collapsed to
// 0600 regardless of its original mode — e.g. a 0755 shell script would
// become non-executable after Apply. Apply must now stat the original
// file's mode and chmod the temp file to match before the rename.
func TestApply_PreservesOriginalFileModeOnWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not POSIX-shaped on windows")
	}
	dir := t.TempDir()
	full := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(full, []byte("#!/bin/sh\necho original\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "script.sh", NewContent: []byte("#!/bin/sh\necho updated\n")},
	}, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("after Apply, script.sh mode = %#o, want %#o (original mode must survive "+
			"a content-only write, not collapse to os.CreateTemp's own default 0600)", got, 0o755)
	}
	if journal.Entries[0].OriginalMode != 0o755 {
		t.Fatalf("journal Entries[0].OriginalMode = %#o, want %#o", journal.Entries[0].OriginalMode, 0o755)
	}
}

// TestApply_NewFileGetsDefaultModeNotCreateTempDefault proves a brand-new
// file (one that did not exist before Apply) lands at defaultNewFileMode
// (0644), not at os.CreateTemp's own 0600 default.
func TestApply_NewFileGetsDefaultModeNotCreateTempDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not POSIX-shaped on windows")
	}
	dir := t.TempDir()

	if _, err := Apply(dir, []FileChange{
		{Path: "brand-new.txt", NewContent: []byte("hello")},
	}, ApplyOptions{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "brand-new.txt"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("new file mode = %#o, want %#o (defaultNewFileMode), not os.CreateTemp's own 0600", got, 0o644)
	}
}

// TestRollback_RestoresOriginalModeNotJustContent proves Rollback restores
// the file's ORIGINAL mode, not merely its content: it deliberately drifts
// the mode after Apply (simulating exactly what the pre-fix
// os.CreateTemp-then-rename bug would have done to the file on its own)
// and confirms Rollback corrects it back to what was recorded before
// Apply ever touched the file.
func TestRollback_RestoresOriginalModeNotJustContent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not POSIX-shaped on windows")
	}
	dir := t.TempDir()
	full := filepath.Join(dir, "config.conf")
	if err := os.WriteFile(full, []byte("original config"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "config.conf", NewContent: []byte("patched config"), ExpectedPriorContent: []byte("original config")},
	}, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Simulate the mode drifting after Apply but before Rollback runs.
	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := Rollback(dir, journal, RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("after Rollback, config.conf mode = %#o, want original %#o restored, not left at the drifted 0600", got, 0o640)
	}
	if b, ok := readFile(t, dir, "config.conf"); !ok || string(b) != "original config" {
		t.Fatalf("content not restored: %q, ok=%v", b, ok)
	}
	if err := Verify(dir, journal); err != nil {
		t.Fatalf("Verify after rollback should report no drift (content or mode): %v", err)
	}
}

// --- M12: approval scope derived from the actual batch, not trusted as-is ---

// TestApply_GrantForOneBatchDoesNotAuthorizeDifferentBatch is a regression
// test for a real bug found in review: Apply used to check whatever
// ApplyOptions.Scope the caller independently supplied, with no connection
// to the actual files in the batch being applied — so a grant obtained
// for deleting "a.txt" alone could be presented (accidentally or
// maliciously) to authorize an unrelated batch that deletes OTHER files
// too, as long as the caller passed the same Scope. Apply must now derive
// the enforced Resource from the exact set of paths in the batch (see
// BatchScope), so a grant scoped to one batch can never authorize a
// different one.
func TestApply_GrantForOneBatchDoesNotAuthorizeDifferentBatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", []byte("a"))
	writeFile(t, dir, "b.txt", []byte("b"))

	broker := approval.NewBroker()
	// A human approves deleting ONLY a.txt.
	if _, err := broker.Grant(BatchScope("delete", []string{"a.txt"}), "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// A batch that ALSO deletes b.txt must not be authorized by that
	// grant, even though the caller passes the same Scope.Action.
	_, err := Apply(dir, []FileChange{
		{Path: "a.txt", Delete: true},
		{Path: "b.txt", Delete: true},
	}, ApplyOptions{Broker: broker, Scope: approval.Scope{Action: "delete"}})
	if err == nil {
		t.Fatal("a grant approved for deleting only a.txt must not authorize a batch that ALSO deletes b.txt")
	}
	if _, ok := readFile(t, dir, "a.txt"); !ok {
		t.Fatal("a.txt should still exist: the whole batch must be rejected (zero side effects), not partially applied")
	}
	if _, ok := readFile(t, dir, "b.txt"); !ok {
		t.Fatal("b.txt should still exist")
	}

	// The SAME grant must still work for the exact batch it was approved
	// for.
	if _, err := Apply(dir, []FileChange{
		{Path: "a.txt", Delete: true},
	}, ApplyOptions{Broker: broker, Scope: approval.Scope{Action: "delete"}}); err != nil {
		t.Fatalf("Apply with the correctly-scoped single-file batch failed: %v", err)
	}
	if _, ok := readFile(t, dir, "a.txt"); ok {
		t.Fatal("a.txt should now be deleted")
	}
}

// TestApply_TokenBasedCheckConsumesSpecificGrantDeterministically proves
// ApplyOptions.Token routes through approval.Broker.CheckToken, which
// consumes one specific, caller-identified grant — unlike
// approval.Broker.Check's own scope-matching search, which (before this
// package's M12 fix added deterministic ordering to Broker itself) could
// pick an arbitrary one of several matching grants.
func TestApply_TokenBasedCheckConsumesSpecificGrantDeterministically(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "x.txt", []byte("x"))

	broker := approval.NewBroker()
	scope := BatchScope("delete", []string{"x.txt"})
	// Two independent grants for the exact same scope (e.g. two different
	// approvers each approving the same thing).
	g1, err := broker.Grant(scope, "tester1", time.Minute)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	g2, err := broker.Grant(scope, "tester2", time.Minute)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	if _, err := Apply(dir, []FileChange{
		{Path: "x.txt", Delete: true},
	}, ApplyOptions{Broker: broker, Scope: approval.Scope{Action: "delete"}, Token: g2.Token}); err != nil {
		t.Fatalf("Apply with Token: %v", err)
	}

	// g2 (the token explicitly presented) must be the one consumed; g1
	// must remain untouched.
	if got := broker.DryRunCheckToken(g2.Token, scope); got != provenance.StatusApprovalRequired {
		t.Fatalf("g2 should have been consumed by the token-based check, DryRunCheckToken = %v, want ApprovalRequired", got)
	}
	if got := broker.DryRunCheckToken(g1.Token, scope); got != provenance.StatusPass {
		t.Fatalf("g1 should remain unused since Apply was told to consume g2 specifically, got %v, want Pass", got)
	}
}

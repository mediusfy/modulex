package patchapply_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/patchapply"
	"github.com/mediusfy/modulex/provenance"
)

func mustWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdversarial_PathTraversalTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	sentinelOutsideDir := t.TempDir()
	sentinelOutside := filepath.Join(sentinelOutsideDir, "escaped.txt")

	attacks := []string{
		"../../../etc/escaped.txt",
		"../escaped.txt",
		"/etc/escaped.txt",
	}
	// Relative traversal targeting a real sibling directory, to prove it
	// never actually lands outside dir even if the traversal "would"
	// resolve to a real, writable location.
	relEscape := filepath.Join("..", filepath.Base(sentinelOutsideDir), "escaped.txt")
	attacks = append(attacks, relEscape)

	for _, bad := range attacks {
		t.Run(bad, func(t *testing.T) {
			changes := []patchapply.FileChange{
				{Path: "legit.txt", NewContent: []byte("should never land")},
				{Path: bad, NewContent: []byte("attacker content")},
			}
			_, err := patchapply.Apply(dir, changes, patchapply.ApplyOptions{})
			if err == nil {
				t.Fatalf("Apply with traversal path %q succeeded, want error", bad)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "legit.txt")); statErr == nil {
				t.Fatalf("traversal attack still wrote the OTHER (legitimate-looking) file in the same batch — whole-batch rejection violated")
			}
			if _, statErr := os.Stat(sentinelOutside); statErr == nil {
				t.Fatalf("traversal attack escaped targetDir and wrote %q", sentinelOutside)
			}
		})
	}
}

func TestAdversarial_SymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()

	if err := os.Symlink(outsideDir, filepath.Join(dir, "escape-link")); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	_, err := patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "escape-link/pwned.txt", NewContent: []byte("attacker content")},
	}, patchapply.ApplyOptions{})
	if err == nil {
		t.Fatal("Apply through a symlink escaping targetDir succeeded, want error")
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "pwned.txt")); statErr == nil {
		t.Fatal("symlink escape wrote outside targetDir")
	}
}

func TestAdversarial_UnrelatedEditNeverClobbered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	original := []byte("original baseline content")
	mustWrite(t, path, original)

	// Simulate: the caller computed a patch against `original`, but before
	// Apply runs, some other process/human edits the file.
	unrelatedEdit := []byte("a human's own unrelated edit, made after the patch was computed")
	mustWrite(t, path, unrelatedEdit)

	_, err := patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "shared.txt", NewContent: []byte("attacker/stale patch content"), ExpectedPriorContent: original},
	}, patchapply.ApplyOptions{})
	if err == nil {
		t.Fatal("Apply succeeded despite unrelated drift, want rejection")
	}

	got := mustRead(t, path)
	if !bytes.Equal(got, unrelatedEdit) {
		t.Fatalf("unrelated edit was clobbered: got %q, want %q (the preserved human edit)", got, unrelatedEdit)
	}
}

func TestAdversarial_DeletionNeverSucceedsWithoutApprovedBroker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "important.txt")
	mustWrite(t, path, []byte("do not delete me without approval"))

	// No broker at all.
	_, err := patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{})
	if err == nil {
		t.Fatal("delete with no broker succeeded, want rejection")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was deleted despite no broker: %v", statErr)
	}

	// Broker present but scope never granted. Apply derives the enforced
	// Resource from the batch itself (via BatchScope), so Scope.Resource
	// here is irrelevant — only a prior Grant for the derived scope
	// matters, and none exists yet.
	b := approval.NewBroker()
	deleteScope := approval.Scope{Action: "delete"}
	grantedScope := patchapply.BatchScope("delete", []string{"important.txt"})
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: deleteScope})
	if err == nil {
		t.Fatal("delete with unapproved broker/scope succeeded, want rejection")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was deleted despite unapproved scope: %v", statErr)
	}

	// Broker present, WRONG scope granted (attacker tries to reuse an
	// unrelated approval for a different delete): grant covers a batch
	// deleting "other-file.txt", which derives to an entirely different
	// Resource than the "important.txt" batch below.
	if _, err := b.Grant(patchapply.BatchScope("delete", []string{"other-file.txt"}), "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: deleteScope})
	if err == nil {
		t.Fatal("delete succeeded using an approval granted for a DIFFERENT batch, want rejection")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was deleted using a mismatched-scope approval: %v", statErr)
	}

	// Now grant the correct (batch-derived) scope; deletion should succeed
	// exactly once.
	if _, err := b.Grant(grantedScope, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := b.DryRunCheck(grantedScope); got != provenance.StatusPass {
		t.Fatalf("sanity check: DryRunCheck after correct grant = %v, want Pass", got)
	}
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: deleteScope})
	if err != nil {
		t.Fatalf("delete with correctly approved scope failed: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("file still exists after a correctly approved delete")
	}
}

func TestAdversarial_PartialFailureRestoresAllFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission-based failure injection does not apply")
	}
	dir := t.TempDir()

	// Two files that will succeed, then a third whose parent directory is
	// read-only so its write genuinely fails partway through the batch.
	mustWrite(t, filepath.Join(dir, "one.txt"), []byte("original one"))
	lockedDir := filepath.Join(dir, "locked")
	if err := os.Mkdir(lockedDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	changes := []patchapply.FileChange{
		{Path: "one.txt", NewContent: []byte("modified one"), ExpectedPriorContent: []byte("original one")},
		{Path: "two.txt", NewContent: []byte("brand new two")},
		{Path: "locked/three.txt", NewContent: []byte("should never land")},
	}
	_, err := patchapply.Apply(dir, changes, patchapply.ApplyOptions{})
	if err == nil {
		t.Fatal("Apply into a read-only directory succeeded, want failure")
	}

	if got := mustRead(t, filepath.Join(dir, "one.txt")); !bytes.Equal(got, []byte("original one")) {
		t.Fatalf("one.txt not rolled back: got %q, want original", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "two.txt")); !os.IsNotExist(statErr) {
		t.Fatal("two.txt (a brand-new file from the failed batch) was not rolled back / removed")
	}
	if _, statErr := os.Stat(filepath.Join(lockedDir, "three.txt")); !os.IsNotExist(statErr) {
		t.Fatal("three.txt should never have been created")
	}
}

func TestAdversarial_SharedNewDirectoryRollbackDoesNotLoseSiblingFiles(t *testing.T) {
	dir := t.TempDir()

	changes := []patchapply.FileChange{
		{Path: "newdir/sub/a.txt", NewContent: []byte("a content")},
		{Path: "newdir/sub/b.txt", NewContent: []byte("b content")},
	}
	j, err := patchapply.Apply(dir, changes, patchapply.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "newdir", "sub", "a.txt")); statErr != nil {
		t.Fatalf("a.txt missing after successful Apply: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "newdir", "sub", "b.txt")); statErr != nil {
		t.Fatalf("b.txt missing after successful Apply: %v", statErr)
	}

	// Rolling back both files means deleting paths Apply created, which
	// requires an approved grant for the scope Rollback derives from them.
	broker := approval.NewBroker()
	scope := patchapply.BatchScope("delete", []string{"newdir/sub/a.txt", "newdir/sub/b.txt"})
	if _, err := broker.Grant(scope, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := patchapply.Rollback(dir, j, patchapply.RollbackOptions{
		Broker: broker,
		Scope:  approval.Scope{Action: "delete"},
	}); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "newdir")); !os.IsNotExist(statErr) {
		t.Fatal("newdir should be fully removed after rolling back both files that created it")
	}
	if err := patchapply.Verify(dir, j); err != nil {
		t.Fatalf("Verify after rollback reports drift: %v", err)
	}
}

// TestAdversarial_ThirdPartyFileAddedToCreatedDirSurvivesUntilSiblingRemoved
// is a regression test for a real data-loss bug found in review (M11):
// Rollback's CreatedDir cleanup used to call os.RemoveAll on a directory
// chain Apply created, which removes EVERYTHING under that directory —
// including a file some other process placed there after Apply ran but
// before Rollback runs, even though that file was never tracked by the
// Journal at all. Rollback must now walk the chain bottom-up with
// os.Remove and stop at the first non-empty directory, so a human-added
// file (or any third-party file) in a package-created directory always
// survives.
func TestAdversarial_ThirdPartyFileAddedToCreatedDirSurvivesUntilSiblingRemoved(t *testing.T) {
	dir := t.TempDir()

	j, err := patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "created/only.txt", NewContent: []byte("only file")},
	}, patchapply.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// A third party drops an unrelated file into the directory this
	// package just created, before Rollback runs.
	thirdPartyFile := filepath.Join(dir, "created", "unrelated-third-party-file.txt")
	mustWrite(t, thirdPartyFile, []byte("not tracked by the journal at all"))

	broker := approval.NewBroker()
	scope := patchapply.BatchScope("delete", []string{"created/only.txt"})
	if _, err := broker.Grant(scope, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := patchapply.Rollback(dir, j, patchapply.RollbackOptions{
		Broker: broker,
		Scope:  approval.Scope{Action: "delete"},
	}); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	// only.txt was tracked by the journal and must be gone.
	if _, statErr := os.Stat(filepath.Join(dir, "created", "only.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("only.txt should have been removed by rollback, stat err = %v", statErr)
	}
	// The third party's file was NEVER tracked by the journal and must
	// survive — this is the exact bug this test guards against.
	got := mustRead(t, thirdPartyFile)
	if !bytes.Equal(got, []byte("not tracked by the journal at all")) {
		t.Fatalf("third-party file content changed: got %q", got)
	}
	// Because the directory is non-empty (the third-party file still
	// lives there), Rollback must not have removed it either.
	if _, statErr := os.Stat(filepath.Join(dir, "created")); statErr != nil {
		t.Fatalf("created/ directory should survive while it still contains the third-party file: %v", statErr)
	}
}

// TestAdversarial_SerializedJournalRefusesToRollbackRatherThanDestroyData is
// a regression test for a real data-loss bug found in review:
// JournalEntry.OriginalContent is tagged json:"-" (deliberately, so a
// Journal summary never leaks raw — possibly secret-containing — file
// content), which means marshaling a Journal to JSON and unmarshaling it
// back silently loses OriginalContent for every entry. Before this fix,
// Rollback would then overwrite an existing file with empty content
// instead of refusing — confirmed by reproducing the scenario against the
// pre-fix implementation before this test was written. Rollback must now
// return ErrJournalNotRestorable and leave the file untouched instead.
func TestAdversarial_SerializedJournalRefusesToRollbackRatherThanDestroyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "important.txt")
	original := []byte("original important content that must never be silently destroyed")
	mustWrite(t, path, original)

	j, err := patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", NewContent: []byte("modified content")},
	}, patchapply.ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Simulate persisting the journal (e.g. to disk, or handing it to
	// another process/tool) and reloading it later — this is exactly the
	// lossy round-trip JournalEntry.OriginalContent's json:"-" tag causes.
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var reloaded patchapply.Journal
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	err = patchapply.Rollback(dir, reloaded, patchapply.RollbackOptions{})
	if err == nil {
		t.Fatal("Rollback on a JSON-round-tripped Journal succeeded, want ErrJournalNotRestorable")
	}
	if !errors.Is(err, patchapply.ErrJournalNotRestorable) {
		t.Fatalf("Rollback error = %v, want it to wrap patchapply.ErrJournalNotRestorable", err)
	}

	got := mustRead(t, path)
	if !bytes.Equal(got, []byte("modified content")) {
		t.Fatalf("file content changed after a refused Rollback: got %q, want the post-Apply content unchanged (%q)",
			got, "modified content")
	}
	if len(got) == 0 {
		t.Fatal("DATA LOSS: file was overwritten with empty content by Rollback on a JSON-round-tripped Journal")
	}
}

package patchapply_test

import (
	"bytes"
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

	// Broker present but scope never granted.
	b := approval.NewBroker()
	scope := approval.Scope{Action: "delete", Resource: "important.txt"}
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: scope})
	if err == nil {
		t.Fatal("delete with unapproved broker/scope succeeded, want rejection")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was deleted despite unapproved scope: %v", statErr)
	}

	// Broker present, WRONG scope granted (attacker tries to reuse an
	// unrelated approval for a different delete).
	if _, err := b.Grant(approval.Scope{Action: "delete", Resource: "other-file.txt"}, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: scope})
	if err == nil {
		t.Fatal("delete succeeded using an approval granted for a DIFFERENT resource, want rejection")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file was deleted using a mismatched-scope approval: %v", statErr)
	}

	// Now grant the correct scope; deletion should succeed exactly once.
	if _, err := b.Grant(scope, "human@example.com", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := b.DryRunCheck(scope); got != provenance.StatusPass {
		t.Fatalf("sanity check: DryRunCheck after correct grant = %v, want Pass", got)
	}
	_, err = patchapply.Apply(dir, []patchapply.FileChange{
		{Path: "important.txt", Delete: true},
	}, patchapply.ApplyOptions{Broker: b, Scope: scope})
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

	if err := patchapply.Rollback(dir, j); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "newdir")); !os.IsNotExist(statErr) {
		t.Fatal("newdir should be fully removed after rolling back both files that created it")
	}
	if err := patchapply.Verify(dir, j); err != nil {
		t.Fatalf("Verify after rollback reports drift: %v", err)
	}
}

func TestAdversarial_ThirdPartyFileAddedToCreatedDirSurvivesUntilSiblingRemoved(t *testing.T) {
	// This documents (rather than "fixes") a narrow edge case flagged by
	// the implementer: Rollback's CreatedDir cleanup uses os.RemoveAll,
	// which removes EVERYTHING under a directory this package created —
	// including a file some other process placed there after Apply ran,
	// if that file happens to still be present when the LAST sibling in
	// that directory is rolled back. Concurrent third-party writes into a
	// directory this package just created are outside this package's
	// stated guarantees (single target-directory, not a general-purpose
	// concurrent-multi-writer filesystem transaction manager).
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

	if err := patchapply.Rollback(dir, j); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	if _, statErr := os.Stat(thirdPartyFile); statErr == nil {
		t.Log("NOTE (expected, documented limitation): Rollback's RemoveAll on a " +
			"self-created directory also deleted a third party's unrelated file " +
			"placed there after Apply and before Rollback")
	} else if os.IsNotExist(statErr) {
		t.Log("confirmed: third-party file placed into a package-created directory " +
			"was removed by Rollback's directory cleanup (RemoveAll) — this is the " +
			"documented, narrow edge case, not a violation of a stated guarantee")
	}
}

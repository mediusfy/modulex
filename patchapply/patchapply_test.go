package patchapply

import (
	"bytes"
	"os"
	"path/filepath"
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

	if err := Rollback(dir, journal); err != nil {
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
	if err := Rollback(dir, journal); err != nil {
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
	scope := approval.Scope{Action: "delete", Resource: "gone.txt"}
	if _, err := broker.Grant(scope, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "gone.txt", Delete: true, ExpectedPriorContent: []byte("bye")},
	}, ApplyOptions{Broker: broker, Scope: scope})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := readFile(t, dir, "gone.txt"); ok {
		t.Fatal("gone.txt should have been deleted")
	}

	if err := Rollback(dir, journal); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if b, ok := readFile(t, dir, "gone.txt"); !ok || string(b) != "bye" {
		t.Fatalf("after rollback, gone.txt = %q, %v; want %q, true", b, ok, "bye")
	}
}

func TestApply_DeleteOfAlreadyAbsentPathIsNoop(t *testing.T) {
	dir := t.TempDir()

	broker := approval.NewBroker()
	scope := approval.Scope{Action: "delete", Resource: "never-existed.txt"}
	if _, err := broker.Grant(scope, "tester", time.Minute); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	journal, err := Apply(dir, []FileChange{
		{Path: "never-existed.txt", Delete: true},
	}, ApplyOptions{Broker: broker, Scope: scope})
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

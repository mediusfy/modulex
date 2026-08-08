package approval

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

func TestFileStore_LoadMissingFileReturnsEmptyBroker(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "approvals.json"))

	b, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := b.Check(Scope{Action: "push"}); got != provenance.StatusApprovalRequired {
		t.Errorf("Check() on a store that was never written to = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

// TestFileStore_SaveThenLoadAcrossBrokers is the decisive test for the
// whole point of FileStore: a grant created in one Broker, saved, and read
// back via a second, independent Broker (simulating two separate
// processes) is checkable there — proving the file, not shared memory, is
// what carries the grant across.
func TestFileStore_SaveThenLoadAcrossBrokers(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "nested", "approvals.json"))

	writer := NewBroker()
	grant, err := writer.Grant(Scope{Action: "push", Resource: "branch-a"}, "drew", time.Minute)
	if err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if err := store.Save(writer); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reader, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reader == writer {
		t.Fatal("Load() returned the same Broker instance as the writer, want an independent one")
	}
	if got := reader.DryRunCheck(Scope{Action: "push", Resource: "branch-a"}); got != provenance.StatusPass {
		t.Errorf("reader.DryRunCheck() = %v, want %v (grant saved by a different Broker)", got, provenance.StatusPass)
	}

	// The reconstructed Grant must still be redeemable by its original
	// token, not just visible by scope.
	if got := reader.DryRunCheckToken(grant.Token, Scope{Action: "push", Resource: "branch-a"}); got != provenance.StatusPass {
		t.Errorf("reader.DryRunCheckToken() = %v, want %v", got, provenance.StatusPass)
	}
}

func TestFileStore_UsedGrantStaysUsedAfterRoundTrip(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "approvals.json"))

	writer := NewBroker()
	if _, err := writer.Grant(Scope{Action: "push"}, "drew", time.Minute); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	if got := writer.Check(Scope{Action: "push"}); got != provenance.StatusPass {
		t.Fatalf("writer.Check() = %v, want %v", got, provenance.StatusPass)
	}
	if err := store.Save(writer); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reader, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reader.DryRunCheck(Scope{Action: "push"}); got != provenance.StatusApprovalRequired {
		t.Errorf("reader.DryRunCheck() for an already-consumed grant = %v, want %v (single-use must survive the round trip)", got, provenance.StatusApprovalRequired)
	}
}

func TestFileStore_ExpiredGrantStaysInertAfterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.json")
	// Hand-write an already-expired grant directly, so this test never
	// depends on real wall-clock timing.
	raw := `[{
		"token": "deadbeef",
		"scope": {"action": "push", "resource": ""},
		"approved_by": "drew",
		"approved_at": "2020-01-01T00:00:00Z",
		"expires_at": "2020-01-01T00:01:00Z",
		"used": false
	}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	reader, err := NewFileStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reader.DryRunCheckToken("deadbeef", Scope{Action: "push"}); got != provenance.StatusApprovalRequired {
		t.Errorf("DryRunCheckToken() for an expired grant = %v, want %v", got, provenance.StatusApprovalRequired)
	}
}

func TestFileStore_LoadCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approvals.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := NewFileStore(path).Load(); err == nil {
		t.Fatal("Load() error = nil, want an error for a corrupt file")
	}
}

func TestFileStore_SaveCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist", "yet", "approvals.json")
	store := NewFileStore(path)

	if err := store.Save(NewBroker()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(%s) error = %v, want the file to exist after Save", path, err)
	}
}

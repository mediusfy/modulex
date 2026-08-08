package agentcli

import (
	"os"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/provenance"
)

// TestApprove_VisibleToAnIndependentFileStoreLoad is the decisive test for
// Approve's whole purpose: a grant it creates must be readable by a
// completely separate approval.FileStore/Load call — simulating
// tools/mcpserver's run_verification, a different process — not just
// visible to Approve's own in-memory Broker.
func TestApprove_VisibleToAnIndependentFileStoreLoad(t *testing.T) {
	root := t.TempDir()

	grant, err := Approve(root, "make-release", "v1.2.0", "drew@jocham.io", 10*time.Minute)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if grant.Token == "" {
		t.Error("Approve() returned a Grant with an empty Token")
	}

	reader, err := approval.NewFileStore(approval.DefaultStorePath(root)).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := reader.DryRunCheck(approval.Scope{Action: "make-release", Resource: "v1.2.0"}); got != provenance.StatusPass {
		t.Errorf("independent Load().DryRunCheck() = %v, want %v", got, provenance.StatusPass)
	}
}

func TestApprove_RejectsEmptyAction(t *testing.T) {
	if _, err := Approve(t.TempDir(), "", "", "drew@jocham.io", time.Minute); err == nil {
		t.Error("Approve() with an empty action = nil error, want an error")
	}
}

func TestLoadContract_RepositoryRoot(t *testing.T) {
	c, err := LoadContract("../..")
	if err != nil {
		t.Fatalf("LoadContract(\"../..\") = %v, want nil", err)
	}
	if len(c.Projects) == 0 || c.Projects[0].ModulePath != "github.com/mediusfy/modulex" {
		t.Errorf("loaded contract does not describe this repository's module")
	}
}

// TestGeneratedFiles_MatchCheckedIn is the CI-enforceable form of
// agentdocs.Drift: it regenerates AGENTS.md and CLAUDE.md from the
// repository's real modulex.agent.yaml and fails if either checked-in file
// doesn't match byte-for-byte. A failure here means modulex.agent.yaml was
// edited without re-running `go run ./tools/agentcli/cmd/modulex agent
// generate -root ../..` (from tools/agentcli) afterward.
func TestGeneratedFiles_MatchCheckedIn(t *testing.T) {
	c, err := LoadContract("../..")
	if err != nil {
		t.Fatalf("LoadContract(\"../..\") = %v", err)
	}

	generated, err := GeneratedFiles(c)
	if err != nil {
		t.Fatalf("GeneratedFiles() = %v", err)
	}

	for _, f := range OutputFiles {
		checkedIn, err := os.ReadFile("../../" + f.name)
		if err != nil {
			t.Fatalf("reading ../../%s: %v", f.name, err)
		}
		if string(checkedIn) != generated[f.name] {
			t.Errorf("../../%s is stale relative to modulex.agent.yaml; "+
				"regenerate with `go run ./cmd/modulex agent generate -root ../..` from tools/agentcli", f.name)
		}
	}
}

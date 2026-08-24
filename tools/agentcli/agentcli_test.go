package agentcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/internal/gittest"
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

func minimalContract(t *testing.T) contract.Contract {
	t.Helper()
	c := contract.Contract{
		SchemaVersion: contract.SchemaVersion,
		Projects:      []contract.Project{{Name: "fixture", Path: "."}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("minimal contract must validate: %v", err)
	}
	return c
}

func TestCheckGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	c := minimalContract(t)

	// Nothing written yet: everything counts as drifted.
	drifted, err := CheckGeneratedFiles(root, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 2 {
		t.Fatalf("drifted = %v, want both files before any generate", drifted)
	}

	if _, err := WriteGeneratedFiles(root, c); err != nil {
		t.Fatal(err)
	}
	drifted, err = CheckGeneratedFiles(root, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 0 {
		t.Fatalf("drifted = %v, want none right after generate", drifted)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted, err = CheckGeneratedFiles(root, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 1 || drifted[0] != "AGENTS.md" {
		t.Fatalf("drifted = %v, want exactly AGENTS.md after a hand edit", drifted)
	}

	// A read failure other than absence must surface as an error, not be
	// misreported as drift: "regenerate" cannot fix an unreadable path.
	// A directory in place of the file makes os.ReadFile fail with a
	// non-ENOENT error on every platform.
	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckGeneratedFiles(root, c); err == nil {
		t.Fatal("want an error for an unreadable generated file, got drift instead")
	}
}

func TestVerify_PlansAndGatesWithoutRunningBlockedChecks(t *testing.T) {
	root := gittest.NewRepoWithDiff(t)
	stubDiscovery(t, root)

	results, err := Verify(context.Background(), root, "base", "HEAD", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for the full gate set")
	}
	for _, r := range results {
		if r.Status == provenance.StatusFail {
			t.Errorf("check %q = fail; with no tools discovered nothing should have run at all (message: %s)", r.Name, r.Message)
		}
		if r.Status == "" {
			t.Errorf("check %q has an empty status", r.Name)
		}
	}
}

func TestDoctor(t *testing.T) {
	t.Run("absent contract is normal", func(t *testing.T) {
		root := t.TempDir()
		stubDiscovery(t, root)
		rep, err := Doctor(root)
		if err != nil {
			t.Fatal(err)
		}
		if rep.ContractPresent || rep.ContractError != "" {
			t.Fatalf("report = %+v, want absent contract with no error", rep)
		}
	})

	t.Run("unparseable contract is reported, not an error", func(t *testing.T) {
		root := t.TempDir()
		stubDiscovery(t, root)
		gittest.WriteFile(t, root, ContractFileName, "projects: [oops\n")
		rep, err := Doctor(root)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.ContractPresent || !strings.HasPrefix(rep.ContractError, "parse:") {
			t.Fatalf("report = %+v, want present contract with a parse error", rep)
		}
	})

	t.Run("valid contract yields counts", func(t *testing.T) {
		root := t.TempDir()
		stubDiscovery(t, root)
		gittest.WriteFile(t, root, ContractFileName,
			"schema_version: \"1.0.0\"\nprojects:\n  - name: fixture\n    path: .\nprotected_paths:\n  - go.mod\n")
		rep, err := Doctor(root)
		if err != nil {
			t.Fatal(err)
		}
		if rep.ContractError != "" {
			t.Fatalf("contract error = %q, want none", rep.ContractError)
		}
		if rep.Projects != 1 || rep.ProtectedPaths != 1 {
			t.Fatalf("report = %+v, want 1 project and 1 protected path", rep)
		}
	})
}

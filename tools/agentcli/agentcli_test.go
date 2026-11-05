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
	tests := []struct {
		name       string
		action     string
		resource   string
		approvedBy string
		ttl        time.Duration
		want       provenance.Status
	}{
		{
			name:       "grant is visible to a separate FileStore load",
			action:     "make-release",
			resource:   "v1.2.0",
			approvedBy: "drew@jocham.io",
			ttl:        10 * time.Minute,
			want:       provenance.StatusPass,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()

			grant, err := Approve(root, tt.action, tt.resource, tt.approvedBy, tt.ttl)
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
			if got := reader.DryRunCheck(approval.Scope{Action: tt.action, Resource: tt.resource}); got != tt.want {
				t.Errorf("independent Load().DryRunCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApprove_RejectsEmptyAction(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		resource   string
		approvedBy string
		ttl        time.Duration
	}{
		{name: "empty action", action: "", resource: "", approvedBy: "drew@jocham.io", ttl: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Approve(t.TempDir(), tt.action, tt.resource, tt.approvedBy, tt.ttl); err == nil {
				t.Error("Approve() with an empty action = nil error, want an error")
			}
		})
	}
}

func TestLoadContract_RepositoryRoot(t *testing.T) {
	tests := []struct {
		name       string
		root       string
		wantModule string
	}{
		{name: "this repository", root: "../..", wantModule: "github.com/mediusfy/modulex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadContract(tt.root)
			if err != nil {
				t.Fatalf("LoadContract(%q) = %v, want nil", tt.root, err)
			}
			if len(c.Projects) == 0 || c.Projects[0].ModulePath != tt.wantModule {
				t.Errorf("loaded contract does not describe this repository's module")
			}
		})
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

	tests := make([]struct{ name string }, len(OutputFiles))
	for i, f := range OutputFiles {
		tests[i].name = f.name
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkedIn, err := os.ReadFile("../../" + tt.name)
			if err != nil {
				t.Fatalf("reading ../../%s: %v", tt.name, err)
			}
			if string(checkedIn) != generated[tt.name] {
				t.Errorf("../../%s is stale relative to modulex.agent.yaml; "+
					"regenerate with `go run ./cmd/modulex agent generate -root ../..` from tools/agentcli", tt.name)
			}
		})
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

// TestCheckGeneratedFiles walks one repository root through a sequence of
// states; each row mutates the tree and states the expected
// CheckGeneratedFiles outcome, so rows run in order and share root.
func TestCheckGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	c := minimalContract(t)

	tests := []struct {
		name        string
		mutate      func(t *testing.T)
		wantDrifted []string
		wantErr     bool
	}{
		{
			// Nothing written yet: everything counts as drifted.
			name:        "nothing written yet",
			mutate:      func(t *testing.T) {},
			wantDrifted: []string{"AGENTS.md", "CLAUDE.md"},
		},
		{
			name: "right after generate",
			mutate: func(t *testing.T) {
				if _, err := WriteGeneratedFiles(root, c); err != nil {
					t.Fatal(err)
				}
			},
			wantDrifted: nil,
		},
		{
			name: "after a hand edit",
			mutate: func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("edited by hand\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantDrifted: []string{"AGENTS.md"},
		},
		{
			// A read failure other than absence must surface as an error, not be
			// misreported as drift: "regenerate" cannot fix an unreadable path.
			// A directory in place of the file makes os.ReadFile fail with a
			// non-ENOENT error on every platform.
			name: "unreadable generated file",
			mutate: func(t *testing.T) {
				if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mutate(t)
			drifted, err := CheckGeneratedFiles(root, c)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error for an unreadable generated file, got drift instead")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(drifted) != len(tt.wantDrifted) {
				t.Fatalf("drifted = %v, want %v", drifted, tt.wantDrifted)
			}
			for i := range tt.wantDrifted {
				if drifted[i] != tt.wantDrifted[i] {
					t.Fatalf("drifted = %v, want %v", drifted, tt.wantDrifted)
				}
			}
		})
	}
}

func TestVerify_PlansAndGatesWithoutRunningBlockedChecks(t *testing.T) {
	tests := []struct {
		name         string
		baseRef      string
		headRef      string
		allowNetwork bool
		full         bool
	}{
		{name: "full gate set, no tools discovered", baseRef: "base", headRef: "HEAD", allowNetwork: false, full: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gittest.NewRepoWithDiff(t)
			stubDiscovery(t, root)

			results, err := Verify(context.Background(), root, tt.baseRef, tt.headRef, tt.allowNetwork, tt.full)
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
		})
	}
}

func TestDoctor(t *testing.T) {
	tests := []struct {
		name string
		// contractYAML is written to the contract file; empty means no file.
		contractYAML     string
		wantPresent      bool
		wantErrPrefix    string // empty means ContractError must be empty
		wantProjects     int
		wantProtectedPth int
	}{
		{name: "absent contract is normal"},
		{
			name:          "unparseable contract is reported, not an error",
			contractYAML:  "projects: [oops\n",
			wantPresent:   true,
			wantErrPrefix: "parse:",
		},
		{
			name:             "valid contract yields counts",
			contractYAML:     "schema_version: \"1.0.0\"\nprojects:\n  - name: fixture\n    path: .\nprotected_paths:\n  - go.mod\n",
			wantPresent:      true,
			wantProjects:     1,
			wantProtectedPth: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			stubDiscovery(t, root)
			if tt.contractYAML != "" {
				gittest.WriteFile(t, root, contract.FileName, tt.contractYAML)
			}
			rep, err := Doctor(root)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ContractPresent != tt.wantPresent {
				t.Fatalf("report = %+v, want ContractPresent=%v", rep, tt.wantPresent)
			}
			if tt.wantErrPrefix == "" && rep.ContractError != "" {
				t.Fatalf("contract error = %q, want none", rep.ContractError)
			}
			if tt.wantErrPrefix != "" && !strings.HasPrefix(rep.ContractError, tt.wantErrPrefix) {
				t.Fatalf("report = %+v, want a contract error prefixed %q", rep, tt.wantErrPrefix)
			}
			if rep.Projects != tt.wantProjects || rep.ProtectedPaths != tt.wantProtectedPth {
				t.Fatalf("report = %+v, want %d project(s) and %d protected path(s)", rep, tt.wantProjects, tt.wantProtectedPth)
			}
		})
	}
}

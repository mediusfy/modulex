package agentcli

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/internal/gittest"
	"github.com/mediusfy/modulex/provenance"
)

// stubDiscovery points discoverRepo at a fixture that reports no tools — so
// the make-based checks gate to StatusUnavailable without spawning any
// subprocess — while keeping Root pointed at the real temp repo so the
// git-based checks (secret scan, protected paths) still run against it.
func stubDiscovery(t *testing.T, root string) {
	t.Helper()
	prev := discoverRepo
	discoverRepo = func(string) (discovery.Repository, error) {
		return discovery.Repository{Root: root, IsGitRepo: true}, nil
	}
	t.Cleanup(func() { discoverRepo = prev })
}

func TestLoadProtectedPaths(t *testing.T) {
	const quiet = "--quiet"

	tests := []struct {
		name string
		// setup builds the repository and returns the ref to load from.
		setup   func(t *testing.T, root string) string
		want    []string
		wantNil bool
		wantErr bool
	}{
		{
			name: "absent contract is nil, not an error",
			setup: func(t *testing.T, root string) string {
				gittest.Run(t, root, "init", quiet)
				gittest.WriteFile(t, root, "app.go", "package app\n")
				gittest.Run(t, root, "add", "app.go")
				gittest.Run(t, root, "commit", quiet, "-m", "base")
				return "HEAD"
			},
			wantNil: true,
		},
		{
			name: "declared paths plus the contract file itself are returned, without validating the contract",
			setup: func(t *testing.T, root string) string {
				gittest.Run(t, root, "init", quiet)
				// Deliberately omit required fields other than protected_paths: an
				// incomplete/invalid contract must still surface its protected paths.
				gittest.WriteFile(t, root, contract.FileName, "protected_paths:\n  - SECURITY.md\n  - go.mod\n")
				gittest.Run(t, root, "add", contract.FileName)
				gittest.Run(t, root, "commit", quiet, "-m", "base")
				return "HEAD"
			},
			want: []string{"SECURITY.md", "go.mod", contract.FileName},
		},
		{
			name: "unparseable contract is an error",
			setup: func(t *testing.T, root string) string {
				gittest.Run(t, root, "init", quiet)
				gittest.WriteFile(t, root, contract.FileName, "protected_paths: [oops\n")
				gittest.Run(t, root, "add", contract.FileName)
				gittest.Run(t, root, "commit", quiet, "-m", "base")
				return "HEAD"
			},
			wantErr: true,
		},
		{
			// A PR must not be able to disarm this check by editing or deleting
			// modulex.agent.yaml in the very commit under review: baseRef
			// predates the PR and cannot have been altered by it.
			name: "H2 regression: reads the contract from baseRef, not the checked-out head",
			setup: func(t *testing.T, root string) string {
				gittest.Run(t, root, "init", quiet)
				gittest.WriteFile(t, root, contract.FileName, "protected_paths:\n  - SECURITY.md\n")
				gittest.Run(t, root, "add", contract.FileName)
				gittest.Run(t, root, "commit", quiet, "-m", "base")
				gittest.Run(t, root, "branch", "base")

				// The "PR" both drops SECURITY.md from protected_paths and deletes
				// SECURITY.md itself.
				gittest.WriteFile(t, root, contract.FileName, "protected_paths: []\n")
				gittest.Run(t, root, "add", contract.FileName)
				gittest.Run(t, root, "commit", quiet, "-am", "defang the contract and remove SECURITY.md")
				return "base"
			},
			// SECURITY.md must still be present (loaded from baseRef,
			// unaffected by the head commit that removed it).
			want: []string{"SECURITY.md", contract.FileName},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			ref := tt.setup(t, root)

			pp, err := loadProtectedPaths(context.Background(), root, ref)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error for malformed YAML")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if pp != nil {
					t.Fatalf("protected paths = %v, want nil", pp)
				}
				return
			}
			if len(pp) != len(tt.want) {
				t.Fatalf("protected paths = %v, want %v", pp, tt.want)
			}
			for i := range tt.want {
				if pp[i] != tt.want[i] {
					t.Fatalf("protected paths = %v, want %v", pp, tt.want)
				}
			}
		})
	}
}

func TestReview_ReturnsResultsForDiff(t *testing.T) {
	tests := []struct {
		name         string
		baseRef      string
		headRef      string
		allowNetwork bool
		wantCategory provenance.VerificationCategory
	}{
		{name: "secret scan present", baseRef: "base", headRef: "HEAD", wantCategory: provenance.VerificationSecretScan},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gittest.NewRepoWithDiff(t)
			stubDiscovery(t, root)

			results, err := Review(context.Background(), root, tt.baseRef, tt.headRef, tt.allowNetwork)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) == 0 {
				t.Fatal("expected at least one verification result")
			}

			var sawCategory bool
			for _, r := range results {
				if r.Category == tt.wantCategory {
					sawCategory = true
				}
			}
			if !sawCategory {
				t.Error("expected a secret-scan result among the review results")
			}
		})
	}
}

func TestHandoff_ProducesValidatedEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		wantTool  string
	}{
		{name: "claude via cli", agentName: "claude", wantTool: "modulex-cli"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := gittest.NewRepoWithDiff(t)
			stubDiscovery(t, root)

			env, err := Handoff(context.Background(), root, tt.agentName, "base", "HEAD", false)
			if err != nil {
				t.Fatal(err)
			}

			// The returned envelope must already pass Validate — Handoff redacts and
			// validates before returning, so a caller can marshal it directly.
			if err := env.Validate(); err != nil {
				t.Fatalf("returned envelope must already validate: %v", err)
			}
			if env.SchemaVersion != provenance.SchemaVersion {
				t.Errorf("schema version = %q, want %q", env.SchemaVersion, provenance.SchemaVersion)
			}
			if env.Agent.Name != tt.agentName {
				t.Errorf("agent name = %q, want %q", env.Agent.Name, tt.agentName)
			}
			if env.Agent.Tool != tt.wantTool {
				t.Errorf("agent tool = %q, want %q", env.Agent.Tool, tt.wantTool)
			}
			if env.Repository.Commit == "" {
				t.Error("repository commit must be resolved from git HEAD")
			}
			if len(env.Verification) == 0 {
				t.Error("verification results must be present in the handoff")
			}
		})
	}
}

func TestHandoff_ErrorsOutsideGitRepo(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
	}{
		{name: "no git repository", agentName: "claude"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir() // no git repo here
			stubDiscovery(t, root)

			if _, err := Handoff(context.Background(), root, tt.agentName, "base", "HEAD", false); err == nil {
				t.Fatal("expected an error resolving commit outside a git repository")
			}
		})
	}
}

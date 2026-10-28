package agentcli

import (
	"context"
	"testing"

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

	t.Run("absent contract is nil, not an error", func(t *testing.T) {
		root := t.TempDir()
		gittest.Run(t, root, "init", quiet)
		gittest.WriteFile(t, root, "app.go", "package app\n")
		gittest.Run(t, root, "add", "app.go")
		gittest.Run(t, root, "commit", quiet, "-m", "base")

		pp, err := loadProtectedPaths(context.Background(), root, "HEAD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pp != nil {
			t.Fatalf("protected paths = %v, want nil", pp)
		}
	})

	t.Run("declared paths plus the contract file itself are returned, without validating the contract", func(t *testing.T) {
		root := t.TempDir()
		gittest.Run(t, root, "init", quiet)
		// Deliberately omit required fields other than protected_paths: an
		// incomplete/invalid contract must still surface its protected paths.
		gittest.WriteFile(t, root, ContractFileName, "protected_paths:\n  - SECURITY.md\n  - go.mod\n")
		gittest.Run(t, root, "add", ContractFileName)
		gittest.Run(t, root, "commit", quiet, "-m", "base")

		pp, err := loadProtectedPaths(context.Background(), root, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"SECURITY.md", "go.mod", ContractFileName}
		if len(pp) != len(want) {
			t.Fatalf("protected paths = %v, want %v", pp, want)
		}
		for i := range want {
			if pp[i] != want[i] {
				t.Fatalf("protected paths = %v, want %v", pp, want)
			}
		}
	})

	t.Run("unparseable contract is an error", func(t *testing.T) {
		root := t.TempDir()
		gittest.Run(t, root, "init", quiet)
		gittest.WriteFile(t, root, ContractFileName, "protected_paths: [oops\n")
		gittest.Run(t, root, "add", ContractFileName)
		gittest.Run(t, root, "commit", quiet, "-m", "base")

		if _, err := loadProtectedPaths(context.Background(), root, "HEAD"); err == nil {
			t.Fatal("expected an error for malformed YAML")
		}
	})

	t.Run("H2 regression: reads the contract from baseRef, not the checked-out head", func(t *testing.T) {
		// A PR must not be able to disarm this check by editing or deleting
		// modulex.agent.yaml in the very commit under review: baseRef
		// predates the PR and cannot have been altered by it.
		root := t.TempDir()
		gittest.Run(t, root, "init", quiet)
		gittest.WriteFile(t, root, ContractFileName, "protected_paths:\n  - SECURITY.md\n")
		gittest.Run(t, root, "add", ContractFileName)
		gittest.Run(t, root, "commit", quiet, "-m", "base")
		gittest.Run(t, root, "branch", "base")

		// The "PR" both drops SECURITY.md from protected_paths and deletes
		// SECURITY.md itself.
		gittest.WriteFile(t, root, ContractFileName, "protected_paths: []\n")
		gittest.Run(t, root, "add", ContractFileName)
		gittest.Run(t, root, "commit", quiet, "-am", "defang the contract and remove SECURITY.md")

		pp, err := loadProtectedPaths(context.Background(), root, "base")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range pp {
			if p == "SECURITY.md" {
				found = true
			}
		}
		if !found {
			t.Fatalf("protected paths = %v, want SECURITY.md still present (loaded from baseRef, unaffected by the head commit that removed it)", pp)
		}
	})
}

func TestReview_ReturnsResultsForDiff(t *testing.T) {
	root := gittest.NewRepoWithDiff(t)
	stubDiscovery(t, root)

	results, err := Review(context.Background(), root, "base", "HEAD", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one verification result")
	}

	var sawSecretScan bool
	for _, r := range results {
		if r.Category == provenance.VerificationSecretScan {
			sawSecretScan = true
		}
	}
	if !sawSecretScan {
		t.Error("expected a secret-scan result among the review results")
	}
}

func TestHandoff_ProducesValidatedEnvelope(t *testing.T) {
	root := gittest.NewRepoWithDiff(t)
	stubDiscovery(t, root)

	env, err := Handoff(context.Background(), root, "claude", "base", "HEAD", false)
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
	if env.Agent.Name != "claude" {
		t.Errorf("agent name = %q, want %q", env.Agent.Name, "claude")
	}
	if env.Agent.Tool != "modulex-cli" {
		t.Errorf("agent tool = %q, want %q", env.Agent.Tool, "modulex-cli")
	}
	if env.Repository.Commit == "" {
		t.Error("repository commit must be resolved from git HEAD")
	}
	if len(env.Verification) == 0 {
		t.Error("verification results must be present in the handoff")
	}
}

func TestHandoff_ErrorsOutsideGitRepo(t *testing.T) {
	root := t.TempDir() // no git repo here
	stubDiscovery(t, root)

	if _, err := Handoff(context.Background(), root, "claude", "base", "HEAD", false); err == nil {
		t.Fatal("expected an error resolving commit outside a git repository")
	}
}

package agentreview

import (
	"context"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/internal/gittest"
	"github.com/mediusfy/modulex/provenance"
)

// newRepoWithDiff wraps gittest.NewRepoWithDiff in a discovery.Repository
// whose Tools is nil (so review's make-based checks gate to StatusUnavailable
// without spawning a subprocess, keeping the test fast).
func newRepoWithDiff(t *testing.T) discovery.Repository {
	t.Helper()
	return discovery.Repository{Root: gittest.NewRepoWithDiff(t), IsGitRepo: true}
}

func TestReview_RunsChecksOverDiff(t *testing.T) {
	tests := []struct {
		name         string
		baseRef      string
		headRef      string
		wantCategory []provenance.VerificationCategory
	}{
		{
			name:    "diff over base..HEAD runs secret scan and protected paths",
			baseRef: "base",
			headRef: "HEAD",
			wantCategory: []provenance.VerificationCategory{
				provenance.VerificationSecretScan,
				provenance.VerificationProtectedPaths,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepoWithDiff(t)

			results := Review(context.Background(), repo, tt.baseRef, tt.headRef, false, nil)
			if len(results) == 0 {
				t.Fatal("expected at least one verification result")
			}

			seen := make(map[provenance.VerificationCategory]bool)
			for _, r := range results {
				seen[r.Category] = true
			}
			for _, want := range tt.wantCategory {
				if !seen[want] {
					t.Errorf("expected a %s result", want)
				}
			}
		})
	}
}

func TestEnvelope_ValidatesAndPopulates(t *testing.T) {
	tests := []struct {
		name      string
		agentName string
		agentTool string
	}{
		{name: "claude via mcp", agentName: "claude", agentTool: "mcp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepoWithDiff(t)
			results := Review(context.Background(), repo, "base", "HEAD", false, nil)

			env, err := Envelope(context.Background(), repo, tt.agentName, tt.agentTool, results)
			if err != nil {
				t.Fatal(err)
			}

			// Envelope redacts and validates before returning, so the caller can
			// marshal it directly.
			if err := env.Validate(); err != nil {
				t.Fatalf("returned envelope must already validate: %v", err)
			}
			if env.SchemaVersion != provenance.SchemaVersion {
				t.Errorf("schema version = %q, want %q", env.SchemaVersion, provenance.SchemaVersion)
			}
			if env.Agent.Name != tt.agentName || env.Agent.Tool != tt.agentTool {
				t.Errorf("agent = %+v, want name=%s tool=%s", env.Agent, tt.agentName, tt.agentTool)
			}
			if env.Repository.Path != repo.Root {
				t.Errorf("repository path = %q, want %q", env.Repository.Path, repo.Root)
			}
			if env.Repository.Commit == "" {
				t.Error("repository commit must be resolved from git HEAD")
			}
			if len(env.Verification) != len(results) {
				t.Errorf("verification count = %d, want %d", len(env.Verification), len(results))
			}
		})
	}
}

// TestEnvelope_DetachedHeadOmitsBranch: a detached HEAD (the normal state for
// a CI checkout of refs/pull/N/merge) must leave Branch empty, not record the
// literal "HEAD" that `git rev-parse --abbrev-ref HEAD` reports there.
func TestEnvelope_DetachedHeadOmitsBranch(t *testing.T) {
	tests := []struct {
		name       string
		gitArgs    []string
		wantBranch string
	}{
		{name: "detached HEAD", gitArgs: []string{"checkout", "--detach", "--quiet"}, wantBranch: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepoWithDiff(t)
			gittest.Run(t, repo.Root, tt.gitArgs...)

			env, err := Envelope(context.Background(), repo, "claude", "mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			if env.Repository.Branch != tt.wantBranch {
				t.Errorf("branch = %q, want %q on a detached HEAD", env.Repository.Branch, tt.wantBranch)
			}
			if env.Repository.Commit == "" {
				t.Error("commit must still be resolved on a detached HEAD")
			}
		})
	}
}

// TestEnvelope_ValidateFailureIsSingleLine: a multi-finding Validate failure
// must surface as one "; "-joined line, not errors.Join's multi-line message
// — the invariant tools/mcpserver's create_handoff used to enforce with its
// own flattening and now inherits from here.
func TestEnvelope_ValidateFailureIsSingleLine(t *testing.T) {
	tests := []struct {
		name string
		// StatusSkipped requires a non-empty Reason, so these results make
		// Validate report one finding each.
		bad []provenance.VerificationResult
	}{
		{
			name: "two skipped results without a reason",
			bad: []provenance.VerificationResult{
				{Name: "a", Category: provenance.VerificationSecretScan, Status: provenance.StatusSkipped},
				{Name: "b", Category: provenance.VerificationSecretScan, Status: provenance.StatusSkipped},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newRepoWithDiff(t)

			_, err := Envelope(context.Background(), repo, "claude", "mcp", tt.bad)
			if err == nil {
				t.Fatal("expected a validation error for skipped results without a reason")
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("validation error contains a newline, want one \"; \"-joined line: %q", err)
			}
			if !strings.Contains(err.Error(), "; ") {
				t.Errorf("validation error = %q, want both findings joined with \"; \"", err)
			}
		})
	}
}

func TestEnvelope_ErrorsOutsideGitRepo(t *testing.T) {
	tests := []struct {
		name string
		repo func(t *testing.T) discovery.Repository
	}{
		{
			name: "temp dir that is not a git repo",
			repo: func(t *testing.T) discovery.Repository { return discovery.Repository{Root: t.TempDir()} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Envelope(context.Background(), tt.repo(t), "claude", "mcp", nil); err == nil {
				t.Fatal("expected an error resolving commit outside a git repository")
			}
		})
	}
}

package discovery

import (
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

func TestClassifyCommand(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want provenance.CommandClass
	}{
		// Required by ADR-0032 P0 acceptance criteria.
		{"make fmt", "make fmt", provenance.ClassMutating},
		{"make deps", "make deps", provenance.ClassNetworked},
		{"make vuln", "make vuln", provenance.ClassNetworked},
		{"make release", "make release", provenance.ClassApprovalRequired},
		{"make release with VERSION arg", "make release VERSION=v0.2.0", provenance.ClassApprovalRequired},
		{"make publish-godev (networked+approval tie-break)", "make publish-godev", provenance.ClassApprovalRequired},

		// Extended everyday-command set.
		{"git status", "git status", provenance.ClassSafe},
		{"git diff", "git diff", provenance.ClassSafe},
		{"git log", "git log", provenance.ClassSafe},
		{"git add", "git add .", provenance.ClassMutating},
		{"git commit", "git commit -m \"msg\"", provenance.ClassMutating},
		{"git push", "git push origin main", provenance.ClassApprovalRequired},
		{"git tag", "git tag -a v1.0.0 -m \"release\"", provenance.ClassApprovalRequired},
		{"git reset --hard", "git reset --hard HEAD~1", provenance.ClassDestructive},
		{"git clean -f", "git clean -f", provenance.ClassDestructive},
		{"git branch -D", "git branch -D feature-x", provenance.ClassDestructive},
		{"go build", "go build ./...", provenance.ClassSafe},
		{"go test", "go test ./...", provenance.ClassSafe},
		{"go vet", "go vet ./...", provenance.ClassSafe},
		{"go mod download", "go mod download", provenance.ClassNetworked},
		{"make build", "make build", provenance.ClassSafe},
		{"make test", "make test", provenance.ClassSafe},
		{"make test-arch", "make test-arch", provenance.ClassSafe},
		{"make lint", "make lint", provenance.ClassSafe},
		{"make check-changelog", "make check-changelog", provenance.ClassSafe},

		// Fail-safe default: unrecognized commands must never default to
		// safe.
		{"unknown command", "curl https://example.com/whatever", provenance.ClassApprovalRequired},
		{"empty command", "", provenance.ClassApprovalRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClass, gotReason := ClassifyCommand(tt.cmd)
			if gotClass != tt.want {
				t.Errorf("ClassifyCommand(%q) class = %q, want %q (reason: %q)", tt.cmd, gotClass, tt.want, gotReason)
			}
			if gotReason == "" {
				t.Errorf("ClassifyCommand(%q) returned an empty reason", tt.cmd)
			}
		})
	}
}

// TestClassifyCommand_PublishGodevTieBreakDocumented pins the specific
// tie-break behavior for a command that is genuinely both networked and
// approval-required, so a future rule-table edit cannot silently regress it
// back toward "networked" without a test failure calling it out.
func TestClassifyCommand_PublishGodevTieBreakDocumented(t *testing.T) {
	class, reason := ClassifyCommand("make publish-godev")
	if class != provenance.ClassApprovalRequired {
		t.Fatalf("ClassifyCommand(%q) class = %q, want %q (approval_required must take precedence over networked)", "make publish-godev", class, provenance.ClassApprovalRequired)
	}
	if reason == "" {
		t.Fatalf("ClassifyCommand(%q) returned an empty reason explaining the tie-break", "make publish-godev")
	}
}

func TestClassifyCommand_UnmatchedFallbackNeverSafe(t *testing.T) {
	for _, cmd := range []string{"", "  ", "rm -rf /tmp/whatever", "some-totally-unknown-tool --flag"} {
		class, _ := ClassifyCommand(cmd)
		if class == provenance.ClassSafe {
			t.Errorf("ClassifyCommand(%q) = %q, an unrecognized command must never be classified safe", cmd, class)
		}
		if class != provenance.ClassApprovalRequired {
			t.Errorf("ClassifyCommand(%q) = %q, want fail-safe default %q", cmd, class, provenance.ClassApprovalRequired)
		}
	}
}

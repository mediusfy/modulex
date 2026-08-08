package review

import (
	"context"
	"strings"
	"testing"

	"github.com/mediusfy/modulex/provenance"
)

func TestChangedFiles(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "a.go", "package a\n")
	writeFile(t, root, "b.go", "package b\n")
	runGit(t, root, "add", "a.go", "b.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "a.go", "package a\n\nfunc F() {}\n")
	runGit(t, root, "commit", "--quiet", "-am", "change a")

	got, err := ChangedFiles(context.Background(), root, "base", "HEAD")
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if len(got) != 1 || got[0] != "a.go" {
		t.Errorf("ChangedFiles() = %v, want [a.go]", got)
	}
}

func TestChangedFiles_BadRefReturnsError(t *testing.T) {
	root := newTestRepo(t)

	if _, err := ChangedFiles(context.Background(), root, "this-ref-does-not-exist", "HEAD"); err == nil {
		t.Fatal("ChangedFiles() error = nil, want an error for a nonexistent ref")
	}
}

// goModRetractOpenComment is a go.mod whose retract block's opening line
// carries a trailing comment — retractLineRanges must record the whole
// block, not just the opening line.
const goModRetractOpenComment = "module example.com/app\n\nretract ( // pre-1.0 releases had a critical bug\n\tv0.9.0\n)\n"

// changelogFixtureBase has a preamble, an "## [Unreleased]" section, and
// one already-released version section.
const changelogFixtureBase = `# Changelog

## [Unreleased]

### Added

- initial entry

## [1.0.0] - 2026-01-01

### Added

- first release
`

// TestCheckProtectedPaths_SingleFileScenarios covers plain file/glob
// matching, an unrelated change, no protectedPaths declared, and the
// CHANGELOG.md/go.mod exceptions — table-driven since every case shares the
// same repo-setup shape.
func TestCheckProtectedPaths_SingleFileScenarios(t *testing.T) {
	tests := []struct {
		name           string
		file           string
		initial        string
		updated        string
		protectedPaths []string
		wantStatus     provenance.Status
		wantMessageHas string
	}{
		{
			name:           "exact match on a file with no exception fails",
			file:           "SECURITY.md",
			initial:        "# Security\n",
			updated:        "# Security\n\nReport issues privately.\n",
			protectedPaths: []string{"SECURITY.md"},
			wantStatus:     provenance.StatusFail,
			wantMessageHas: "SECURITY.md",
		},
		{
			name:           "glob match fails",
			file:           ".github/workflows/ci.yml",
			initial:        "name: ci\n",
			updated:        "name: ci\non: push\n",
			protectedPaths: []string{".github/workflows/*.yml"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "unrelated change passes",
			file:           "app.go",
			initial:        "package app\n",
			updated:        "package app\n\nfunc Hello() {}\n",
			protectedPaths: []string{"go.mod", ".github/workflows/*.yml"},
			wantStatus:     provenance.StatusPass,
		},
		{
			name:           "no protected paths declared passes trivially",
			file:           "go.mod",
			initial:        "module example.com/app\n",
			updated:        "module example.com/app\n\nrequire nothing v0.0.0\n",
			protectedPaths: nil,
			wantStatus:     provenance.StatusPass,
		},
		{
			name:    "changelog addition within Unreleased passes",
			file:    "CHANGELOG.md",
			initial: changelogFixtureBase,
			updated: strings.Replace(changelogFixtureBase, "- initial entry\n",
				"- initial entry\n- a new entry, added like the check-changelog gate requires\n", 1),
			protectedPaths: []string{"CHANGELOG.md"},
			wantStatus:     provenance.StatusPass,
		},
		{
			name:           "changelog edit outside Unreleased fails",
			file:           "CHANGELOG.md",
			initial:        changelogFixtureBase,
			updated:        strings.Replace(changelogFixtureBase, "- first release\n", "- first release, rewritten\n", 1),
			protectedPaths: []string{"CHANGELOG.md"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:    "changelog release cut fails",
			file:    "CHANGELOG.md",
			initial: changelogFixtureBase,
			updated: strings.Replace(changelogFixtureBase, "## [Unreleased]\n",
				"## [Unreleased]\n\n## [1.1.0] - 2026-02-01\n", 1),
			protectedPaths: []string{"CHANGELOG.md"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "go.mod retract edit fails",
			file:           "go.mod",
			initial:        "module example.com/app\n\nretract v0.1.0\n",
			updated:        "module example.com/app\n\nretract v0.1.0\nretract v0.2.0\n",
			protectedPaths: []string{"go.mod"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "go.mod require edit passes",
			file:           "go.mod",
			initial:        "module example.com/app\n\nretract v0.1.0\n",
			updated:        "module example.com/app\n\nrequire example.com/dep v1.2.3\n\nretract v0.1.0\n",
			protectedPaths: []string{"go.mod"},
			wantStatus:     provenance.StatusPass,
		},
		{
			name:           "go.mod retract block edit fails even when the closing paren has a trailing comment",
			file:           "go.mod",
			initial:        "module example.com/app\n\nretract (\n\tv0.9.0\n) // pre-1.0 releases had a critical bug\n",
			updated:        "module example.com/app\n\nretract (\n\tv0.9.0\n\tv0.9.1\n) // pre-1.0 releases had a critical bug\n",
			protectedPaths: []string{"go.mod"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "go.mod retract block edit fails even when the opening paren has a trailing comment",
			file:           "go.mod",
			initial:        goModRetractOpenComment,
			updated:        strings.Replace(goModRetractOpenComment, "\tv0.9.0\n", "\tv0.9.0\n\tv0.9.1\n", 1),
			protectedPaths: []string{"go.mod"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "go.mod tab-separated retract addition fails",
			file:           "go.mod",
			initial:        "module example.com/app\n",
			updated:        "module example.com/app\n\nretract\tv0.1.0\n",
			protectedPaths: []string{"go.mod"},
			wantStatus:     provenance.StatusFail,
		},
		{
			name:           "invalid glob pattern fails naming the pattern even when no protected file changed",
			file:           "app.go",
			initial:        "package app\n",
			updated:        "package app\n\nfunc Hello() {}\n",
			protectedPaths: []string{".github/workflows/[.yml"},
			wantStatus:     provenance.StatusFail,
			wantMessageHas: `.github/workflows/[.yml`,
		},
		{
			name:           "valid patterns are still enforced alongside an invalid one",
			file:           "SECURITY.md",
			initial:        "# Security\n",
			updated:        "# Security\n\nReport issues privately.\n",
			protectedPaths: []string{"[", "SECURITY.md"},
			wantStatus:     provenance.StatusFail,
			wantMessageHas: `SECURITY.md matches protected path "SECURITY.md"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRepo(t)
			writeFile(t, root, tt.file, tt.initial)
			runGit(t, root, "add", tt.file)
			runGit(t, root, "commit", "--quiet", "-m", "base")
			runGit(t, root, "branch", "base")

			writeFile(t, root, tt.file, tt.updated)
			runGit(t, root, "commit", "--quiet", "-am", "update "+tt.file)

			result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", tt.protectedPaths)

			if result.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q; Message: %s", result.Status, tt.wantStatus, result.Message)
			}
			if tt.wantMessageHas != "" && !strings.Contains(result.Message, tt.wantMessageHas) {
				t.Errorf("Message = %q, want it to contain %q", result.Message, tt.wantMessageHas)
			}
		})
	}
}

func TestCheckProtectedPaths_BadRefReturnsUnavailable(t *testing.T) {
	root := newTestRepo(t)

	result := CheckProtectedPaths(context.Background(), root, "this-ref-does-not-exist", "HEAD", []string{"go.mod"})

	if result.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", result.Status)
	}
	if result.Reason == "" {
		t.Error("Reason is empty, want an explanation naming the failed diff computation")
	}
}

// TestCheckProtectedPaths_BadRefStillReportsInvalidGlob: a diff failure
// must not swallow the malformed-glob diagnostic — broken git state and a
// broken contract can coincide, and the unavailable Reason has to surface
// both.
func TestCheckProtectedPaths_BadRefStillReportsInvalidGlob(t *testing.T) {
	root := newTestRepo(t)

	result := CheckProtectedPaths(context.Background(), root, "this-ref-does-not-exist", "HEAD", []string{"go.mod["})

	if result.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", result.Status)
	}
	if !strings.Contains(result.Reason, `"go.mod["`) {
		t.Errorf("Reason = %q, want it to name the invalid glob pattern alongside the diff failure", result.Reason)
	}
}

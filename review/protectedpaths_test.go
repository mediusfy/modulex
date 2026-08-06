package review

import (
	"context"
	"strings"
	"testing"
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

func TestCheckProtectedPaths_NoPatternsPassesTrivially(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "go.mod", "module example.com/app\n")
	runGit(t, root, "add", "go.mod")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "go.mod", "module example.com/app\n\nrequire nothing v0.0.0\n")
	runGit(t, root, "commit", "--quiet", "-am", "touch go.mod")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", nil)

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (no protected paths declared)", result.Status)
	}
}

// TestCheckProtectedPaths_ExactMatchFails uses SECURITY.md, a protected
// file with no file-scoped exception (unlike go.mod and CHANGELOG.md — see
// the dedicated tests below), to exercise plain "any change is a hit"
// matching.
func TestCheckProtectedPaths_ExactMatchFails(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "SECURITY.md", "# Security\n")
	runGit(t, root, "add", "SECURITY.md")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "SECURITY.md", "# Security\n\nReport issues privately.\n")
	runGit(t, root, "commit", "--quiet", "-am", "touch SECURITY.md")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"SECURITY.md"})

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail; Message: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "SECURITY.md") {
		t.Errorf("Message = %q, want it to name SECURITY.md", result.Message)
	}
}

// changelogFixture is CHANGELOG.md's real structure, trimmed to what
// unreleasedSectionRange needs: a preamble, an "## [Unreleased]" section,
// and one already-released version section after it.
const changelogFixtureBase = `# Changelog

## [Unreleased]

### Added

- initial entry

## [1.0.0] - 2026-01-01

### Added

- first release
`

func TestCheckProtectedPaths_ChangelogAdditionWithinUnreleasedPasses(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "CHANGELOG.md", changelogFixtureBase)
	runGit(t, root, "add", "CHANGELOG.md")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	updated := strings.Replace(changelogFixtureBase, "- initial entry\n", "- initial entry\n- a new entry, added like the check-changelog gate requires\n", 1)
	writeFile(t, root, "CHANGELOG.md", updated)
	runGit(t, root, "commit", "--quiet", "-am", "changelog: document a change")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"CHANGELOG.md"})

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (addition stayed within ## [Unreleased]); Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_ChangelogEditOutsideUnreleasedFails(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "CHANGELOG.md", changelogFixtureBase)
	runGit(t, root, "add", "CHANGELOG.md")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	updated := strings.Replace(changelogFixtureBase, "- first release\n", "- first release, rewritten\n", 1)
	writeFile(t, root, "CHANGELOG.md", updated)
	runGit(t, root, "commit", "--quiet", "-am", "changelog: rewrite a released entry")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"CHANGELOG.md"})

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail (edit touched an already-released version section); Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_ChangelogReleaseCutFails(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "CHANGELOG.md", changelogFixtureBase)
	runGit(t, root, "add", "CHANGELOG.md")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	cut := strings.Replace(changelogFixtureBase, "## [Unreleased]\n", "## [Unreleased]\n\n## [1.1.0] - 2026-02-01\n", 1)
	writeFile(t, root, "CHANGELOG.md", cut)
	runGit(t, root, "commit", "--quiet", "-am", "changelog: cut a release")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"CHANGELOG.md"})

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail (a version-section boundary was inserted); Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_GoModRetractEditFails(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "go.mod", "module example.com/app\n\nretract v0.1.0\n")
	runGit(t, root, "add", "go.mod")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "go.mod", "module example.com/app\n\nretract v0.1.0\nretract v0.2.0\n")
	runGit(t, root, "commit", "--quiet", "-am", "go.mod: retract v0.2.0")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"go.mod"})

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail (a retract directive was added); Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_GoModRequireEditPasses(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "go.mod", "module example.com/app\n\nretract v0.1.0\n")
	runGit(t, root, "add", "go.mod")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "go.mod", "module example.com/app\n\nrequire example.com/dep v1.2.3\n\nretract v0.1.0\n")
	runGit(t, root, "commit", "--quiet", "-am", "go.mod: add a dependency")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"go.mod"})

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (only a require line changed, retract v0.1.0 untouched); Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_GlobMatchFails(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, ".github/workflows/ci.yml", "name: ci\n")
	runGit(t, root, "add", ".github/workflows/ci.yml")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, ".github/workflows/ci.yml", "name: ci\non: push\n")
	runGit(t, root, "commit", "--quiet", "-am", "touch workflow")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{".github/workflows/*.yml"})

	if result.Status != "fail" {
		t.Fatalf("Status = %q, want fail; Message: %s", result.Status, result.Message)
	}
}

func TestCheckProtectedPaths_UnrelatedChangePasses(t *testing.T) {
	root := newTestRepo(t)

	writeFile(t, root, "app.go", "package app\n")
	runGit(t, root, "add", "app.go")
	runGit(t, root, "commit", "--quiet", "-m", "base")
	runGit(t, root, "branch", "base")

	writeFile(t, root, "app.go", "package app\n\nfunc Hello() {}\n")
	runGit(t, root, "commit", "--quiet", "-am", "unrelated change")

	result := CheckProtectedPaths(context.Background(), root, "base", "HEAD", []string{"go.mod", ".github/workflows/*.yml"})

	if result.Status != "pass" {
		t.Fatalf("Status = %q, want pass (changed file matches no protected pattern); Message: %s", result.Status, result.Message)
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

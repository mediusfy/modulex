// Package verify maps a set of changed repository paths to focused
// verification checks, and pairs them with the repository's always-required
// full gates, per ADR-0032 ("Agent-First Development Experience"), P0: "Add
// focused agent verification with explicit skipped statuses" (Jira MOD-63),
// step 5 of the ADR's "Standard agent workflow":
//
//	`modulex agent verify` runs focused checks followed by required
//	repository gates and reports skipped checks separately from
//	successful checks.
//
// verify is a standalone leaf package (github.com/mediusfy/modulex/verify),
// like provenance and discovery: it does not import the core modulex
// package. It depends on provenance for the Status/VerificationCategory/
// VerificationResult types (so a future `modulex agent handoff` can consume
// this package's output directly without translation) and on discovery for
// discovery.ToolStatus (so tool-availability gating uses the same data
// discovery.Discover already produces, rather than re-probing PATH itself).
//
// # changedFiles is untrusted input
//
// changedFiles typically comes from a diff (e.g. `git diff --name-only`)
// and may include paths from an external contribution this agent did not
// author. PlanFor never uses a changed path to build a CheckSpec.Command
// unless the path passes isPathSafeForCommand (letters, digits, '_', '-',
// '.', '/' only, no ".." traversal segment) — a path containing shell
// metacharacters is routed to fallbackToFullGates instead, so it can never
// inject shell syntax into a Command that Run later executes via "sh -c".
// See TestPlanFor_RejectsShellMetacharactersInPath for the regression test.
//
// # Focused vs. full: two mandatory outputs, not alternatives
//
// PlanFor produces a Plan with two fields:
//
//   - FocusedChecks: checks recommended because of what actually changed —
//     a spot-check, cheap to run, scoped to the affected package(s).
//   - FullGates: the fixed, complete list of this repository's required
//     gates (make build, test, test-arch, lint, and the boundary/
//     compatibility/changelog scripts), unconditionally, every time.
//
// FullGates is never a function of changedFiles. This is deliberate: per
// ADR-0032's acceptance criterion "Full gates remain required before push
// or release," a caller must never be able to skip the full gate set just
// because the focused checks looked sufficient. Nothing in this package's
// API lets FocusedChecks stand in for FullGates.
//
// # Why PlanFor, not Plan
//
// The ticket's sketch named the function Plan, returning a type also named
// Plan. Go does not allow a function and a type to share one identifier in
// the same package, so the function is named PlanFor instead. PlanFor takes
// only changedFiles, not a discovery.Repository: every mapping rule below is
// derived from path shape alone (path prefixes/suffixes), never from
// repository contents, so no repository context is needed to select
// focused checks. (Run, by contrast, does need discovery.ToolStatus data,
// because tool availability is an environment fact PlanFor cannot know from
// paths alone.)
//
// # The tool/network fields on CheckSpec
//
// CheckSpec carries RequiredTool and Networked fields beyond the ticket's
// minimal sketch (Name, Command, Category, Reason). This keeps the "what
// does this check need" decision entirely inside this package (made once,
// here, when a CheckSpec is constructed) rather than making Run re-parse or
// pattern-match Command strings to guess at a dependency — Run stays a
// generic executor that works for any CheckSpec, from any caller, without
// needing to understand this repository's specific command vocabulary.
//
// # Known gaps (documented, not engineered around)
//
// The per-file mapping in focusedChecksForFile is a reasonable rule table
// for this repository's actual layout, not an exhaustive or fully general
// solution:
//
//   - A changed file inside a nested Go module other than the root module
//     (examples/external-consumer/*, tools/*/*) is mapped to that module's
//     own checks, run from the module's root via `go -C <dir>` (see
//     nestedModuleDir). The nested-module set is a hardcoded convention —
//     every direct child of tools/ plus examples/external-consumer — not
//     discovered from go.mod files on disk, so a new nested module outside
//     that convention would silently fall back to root-module mapping. A
//     future revision could consult discovery.Repository.Modules instead;
//     PlanFor's signature deliberately leaves room for that (see "Why
//     PlanFor, not Plan" above) without requiring it today.
//   - The examples/ rule identifies "the specific example's own tests" by
//     taking the first path segment after examples/; it does not verify
//     that segment is actually one of discovery.Repository.CompositionRoots.
package verify

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// safePathPattern matches the character set every legitimate path in this
// repository is expected to use: letters, digits, '_', '-', '.', and '/' as
// a segment separator. changedFiles ultimately comes from a diff (e.g. `git
// diff --name-only`) and is not necessarily trustworthy — a crafted path
// (say, from a file added in an external contribution) must never be able
// to inject shell syntax into a CheckSpec.Command that Run later executes
// via "sh -c". Any path containing a character outside this set is treated
// as unmapped and routed to fallbackToFullGates rather than used to build a
// command string; see isPathSafeForCommand's callers.
var safePathPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)

// isPathSafeForCommand reports whether path is safe to interpolate into (or
// use directly as) a CheckSpec.Command string that Run will execute via
// "sh -c". A path is unsafe if it is empty, contains a character outside
// safePathPattern, or contains ".." (which could escape the intended
// directory even though it wouldn't enable shell injection on its own).
func isPathSafeForCommand(path string) bool {
	if path == "" || strings.Contains(path, "..") {
		return false
	}
	return safePathPattern.MatchString(path)
}

// CheckSpec describes one verification check: a human-readable name, the
// shell command that performs it, the provenance category it belongs to,
// and why it was selected. The same type is used for both Plan.
// FocusedChecks and Plan.FullGates/FullGates — a full gate is just a
// CheckSpec whose Category is provenance.VerificationFull instead of
// provenance.VerificationFocused.
type CheckSpec struct {
	// Name is a short, stable identifier for this check (e.g. "test-httpx",
	// "lint"), suitable as a map key or a provenance.VerificationResult.Name.
	Name string
	// Command is the shell command line that performs this check (e.g. "go
	// test ./httpx/..."), executed by Run via "sh -c".
	Command  string
	Category provenance.VerificationCategory
	// Reason explains why this check was selected (for a focused check) or
	// why it is always required (for a full gate).
	Reason string
	// RequiredTool is the discovery.ToolStatus.Name this check depends on
	// (e.g. "golangci-lint", "go", "git"), or "" if the check has no such
	// dependency Run should verify before attempting it.
	RequiredTool string
	// Networked marks a check that performs network I/O. Run skips these
	// when its caller's allowNetwork argument is false.
	Networked bool
	// Dir, if non-empty, is the working directory Command runs in (set as
	// the spawned process's exec.Cmd.Dir). Empty means Run's own process's
	// current working directory — today's behavior, unchanged for every
	// existing caller (PlanFor, FullGates, review.Checks) that leaves this
	// unset. A caller that resolves Command against a specific repository
	// root not necessarily equal to the calling process's cwd (e.g.
	// tools/mcpserver, whose callers pass an explicit root per MCP call)
	// should set Dir to that root.
	Dir string
}

// Plan is the output of mapping a set of changed files to focused checks,
// paired with the repository's always-required full gates. See the package
// doc comment's "Focused vs. full" section for why both fields are always
// present and neither can substitute for the other.
type Plan struct {
	// FocusedChecks are the checks PlanFor recommends given changedFiles.
	// Always non-nil (possibly empty — see the package doc's ".github/
	// workflows" note in focusedChecksForFile) and deterministically
	// ordered (sorted by Category then Name), so two calls with the same
	// changedFiles in a different order produce identical output.
	FocusedChecks []CheckSpec
	// FullGates is always a copy of the package-level FullGates slice,
	// regardless of changedFiles.
	FullGates []CheckSpec
}

// FullGates is the fixed, complete list of this repository's required gates
// before push or release, per AGENTS.md ("`make test-arch`, `make build`,
// `make lint`, and `make test` must all pass locally before pushing") and
// the boundary/compatibility/changelog scripts already wired into the
// Makefile. Exported so a caller (or CI) can iterate the canonical list
// without hardcoding it themselves.
//
// Treat this as read-only. PlanFor and callers that want their own copy
// should use a defensive copy (PlanFor does this internally); mutating an
// element of this slice in place would affect every future PlanFor call.
var FullGates = []CheckSpec{
	{
		Name:         "build",
		Command:      "make build",
		Category:     provenance.VerificationFull,
		Reason:       "compiles all packages and examples; required before any push or release per AGENTS.md",
		RequiredTool: "go",
	},
	{
		Name:         "test",
		Command:      "make test",
		Category:     provenance.VerificationFull,
		Reason:       "runs the full test suite; required before any push or release per AGENTS.md",
		RequiredTool: "go",
	},
	{
		Name:         "test-arch",
		Command:      "make test-arch",
		Category:     provenance.VerificationFull,
		Reason:       "runs the full test suite under the race detector; required before any push or release per AGENTS.md",
		RequiredTool: "go",
	},
	{
		Name:         "lint",
		Command:      "make lint",
		Category:     provenance.VerificationFull,
		Reason:       "runs golangci-lint across the repository; required before any push or release per AGENTS.md",
		RequiredTool: "golangci-lint",
	},
	{
		Name:         "check-consumer-boundary",
		Command:      "make check-consumer-boundary",
		Category:     provenance.VerificationFull,
		Reason:       "verifies a consumer importing only the core package does not compile in an integration adapter as a build dependency",
		RequiredTool: "go",
	},
	{
		Name:         "check-module-boundary",
		Command:      "make check-module-boundary",
		Category:     provenance.VerificationFull,
		Reason:       "runs the modboundary analyzer against examples/deployment to enforce feature-module boundaries",
		RequiredTool: "go",
	},
	{
		Name:         "check-api-compat",
		Command:      "make check-api-compat",
		Category:     provenance.VerificationFull,
		Reason:       "reports public API changes since the latest git tag",
		RequiredTool: "go",
	},
	{
		Name:         "check-changelog",
		Command:      "make check-changelog",
		Category:     provenance.VerificationFull,
		Reason:       "verifies CHANGELOG.md is updated when required (PR diff vs origin/main)",
		RequiredTool: "git",
	},
}

// PlanFor maps changedFiles (paths relative to the repository root, e.g.
// "httpx/httpx.go") to a Plan: focused checks recommended by what changed,
// plus the repository's full gates, unconditionally.
//
// See the package doc comment for the reasoning behind this name (not
// Plan), the focused/full distinction, and known gaps in the mapping rules.
func PlanFor(changedFiles []string) Plan {
	var focused []CheckSpec
	for _, f := range changedFiles {
		focused = append(focused, focusedChecksForFile(f)...)
	}

	return Plan{
		FocusedChecks: dedupeAndSort(focused),
		FullGates:     copyFullGates(),
	}
}

// copyFullGates returns a defensive copy of the package-level FullGates
// slice, so a caller mutating Plan.FullGates never affects FullGates itself
// or a different caller's Plan.
func copyFullGates() []CheckSpec {
	out := make([]CheckSpec, len(FullGates))
	copy(out, FullGates)
	return out
}

// focusedChecksForFile returns the focused checks recommended for one
// changed path, or nil if this path contributes none on its own (either
// because it is a case documented below as intentionally focused-check-free,
// or because it fell through to the unmapped fallback, which returns a
// non-nil, non-empty result — see the "no rule matched" case).
//
// Rules are evaluated in order, first match wins; see the package doc's
// "Known gaps" section for what this deliberately does not attempt to
// handle precisely.
func focusedChecksForFile(path string) []CheckSpec {
	path = strings.ReplaceAll(path, "\\", "/")

	// A path containing shell-significant characters (or a "..' traversal
	// segment) is never used to build a command string: doing so would let
	// a crafted changed-file path inject arbitrary shell syntax into a
	// CheckSpec.Command that Run executes via "sh -c". Route it to the same
	// fail-safe fallback used for any other unmapped path instead of
	// special-casing it as an error, since "recommend the full gate set" is
	// already the correct, safe response to a path this rule table cannot
	// confidently interpret.
	if !isPathSafeForCommand(path) {
		return fallbackToFullGates(path)
	}

	switch {
	case isWorkflowFile(path):
		// CI configuration changes are too consequential to spot-check: no
		// focused shortcut is offered here. This is an intentional, empty
		// contribution — distinct from the "no rule matched" fallback case
		// below — because FullGates (always present in Plan regardless)
		// is deliberately the *only* recommended response to a CI change,
		// not a fallback of last resort.
		return nil

	case isCheckScript(path):
		return []CheckSpec{
			{
				Name:     path,
				Command:  "./" + path,
				Category: provenance.VerificationFocused,
				Reason:   fmt.Sprintf("%s itself changed; run it directly rather than only its make wrapper", path),
			},
		}

	case strings.HasPrefix(path, "examples/"):
		return focusedChecksForExample(path)

	case path == "go.mod" || path == "go.sum":
		return []CheckSpec{
			{
				Name:         "check-api-compat",
				Command:      "make check-api-compat",
				Category:     provenance.VerificationFocused,
				Reason:       fmt.Sprintf("%s changed; dependency changes can affect the public API surface", path),
				RequiredTool: "go",
			},
			{
				Name:         "go-mod-verify",
				Command:      "go mod verify",
				Category:     provenance.VerificationFocused,
				Reason:       fmt.Sprintf("%s changed; verify downloaded modules match go.sum checksums", path),
				RequiredTool: "go",
			},
		}

	case path == "CHANGELOG.md":
		return []CheckSpec{
			{
				Name:         "check-changelog",
				Command:      "make check-changelog",
				Category:     provenance.VerificationFocused,
				Reason:       "CHANGELOG.md changed; trivially fast to re-check now rather than only at full-gate time",
				RequiredTool: "git",
			},
		}

	case strings.HasSuffix(path, ".go"):
		return focusedChecksForGoFile(path)

	default:
		// A non-.go file inside a nested Go module (its go.mod/go.sum, a
		// script, testdata) still identifies that module as the changed
		// unit: recommend the module's own checks rather than the full
		// gates, which are root-module-only and would never exercise it.
		if dir := nestedModuleDir(path); dir != "" {
			return nestedModuleChecks(path, dir)
		}
		return fallbackToFullGates(path)
	}
}

// nestedModuleDir returns the repository-relative directory of the nested
// Go module path belongs to, or "" when path is part of the root module.
// The repository's nested-module convention is fixed: every direct child of
// tools/ is its own Go module, as is examples/external-consumer (each has
// its own go.mod plus a replace directive toward the root). A root-module
// `./tools/...` package pattern therefore matches nothing — go exits
// nonzero with "matched no packages" — so checks for these paths must run
// from the nested module's own root via `go -C`. PlanFor stays a pure path
// mapping, so this encodes the convention rather than reading go.mod files
// off disk.
func nestedModuleDir(path string) string {
	if strings.HasPrefix(path, "tools/") {
		if sub := topLevelSegment(strings.TrimPrefix(path, "tools/")); sub != "" {
			return "tools/" + sub
		}
	}
	if strings.HasPrefix(path, "examples/external-consumer/") {
		return "examples/external-consumer"
	}
	return ""
}

// nestedModuleChecks returns the focused test+vet pair for a file in the
// nested module at dir, running from that module's root.
func nestedModuleChecks(path, dir string) []CheckSpec {
	return []CheckSpec{
		{
			Name:         "test-" + dir,
			Command:      fmt.Sprintf("go -C %s test ./...", dir),
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s changed; %s is its own Go module, so its tests run from that module's root", path, dir),
			RequiredTool: "go",
		},
		{
			Name:         "vet-" + dir,
			Command:      fmt.Sprintf("go -C %s vet ./...", dir),
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s changed; %s is its own Go module, so vet runs from that module's root", path, dir),
			RequiredTool: "go",
		},
	}
}

// focusedChecksForExample handles a changed path under examples/: a build
// of every example (cheap insurance that nothing else broke), plus, when
// the path identifies one specific example directory, that example's own
// tests. examples/external-consumer is its own Go module, so it gets
// nestedModuleChecks instead of a root-module per-example test pattern.
func focusedChecksForExample(path string) []CheckSpec {
	checks := []CheckSpec{
		{
			Name:         "build-examples",
			Command:      "go build ./examples/...",
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s changed under examples/; ensure all examples still build", path),
			RequiredTool: "go",
		},
	}

	if nested := nestedModuleDir(path); nested != "" {
		return append(checks, nestedModuleChecks(path, nested)...)
	}
	rest := strings.TrimPrefix(path, "examples/")
	if dir := topLevelSegment(rest); dir != "" {
		examplePath := "examples/" + dir
		checks = append(checks, CheckSpec{
			Name:         "test-" + examplePath,
			Command:      fmt.Sprintf("go test ./%s/...", examplePath),
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s changed; run its own tests", examplePath),
			RequiredTool: "go",
		})
	}
	return checks
}

// focusedChecksForGoFile handles a changed .go file outside examples/: a
// package directory (e.g. "httpx/httpx.go" -> "httpx") gets go test/go vet
// scoped to that package; a file directly at the repository root (e.g.
// "modulex.go", no "/" in path) gets go test/go vet scoped to the root
// package.
func focusedChecksForGoFile(path string) []CheckSpec {
	if dir := nestedModuleDir(path); dir != "" {
		return nestedModuleChecks(path, dir)
	}
	if strings.HasPrefix(path, "tools/") {
		// A .go file directly under tools/ (no subdirectory) belongs to no
		// root-module package — every tools/* child is its own module — so
		// a root-module `./tools/...` pattern would match no packages and
		// always fail. There is no focused command to offer; recommend the
		// full gates.
		return fallbackToFullGates(path)
	}
	pkg := topLevelSegment(path)
	if pkg == "" {
		return []CheckSpec{
			{
				Name:         "test-root",
				Command:      "go test .",
				Category:     provenance.VerificationFocused,
				Reason:       fmt.Sprintf("%s changed; it belongs to the root package", path),
				RequiredTool: "go",
			},
			{
				Name:         "vet-root",
				Command:      "go vet .",
				Category:     provenance.VerificationFocused,
				Reason:       fmt.Sprintf("%s changed; it belongs to the root package", path),
				RequiredTool: "go",
			},
		}
	}
	return []CheckSpec{
		{
			Name:         "test-" + pkg,
			Command:      fmt.Sprintf("go test ./%s/...", pkg),
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s/ changed", pkg),
			RequiredTool: "go",
		},
		{
			Name:         "vet-" + pkg,
			Command:      fmt.Sprintf("go vet ./%s/...", pkg),
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("%s/ changed", pkg),
			RequiredTool: "go",
		},
	}
}

// fallbackToFullGates handles a changed path that matched no specific rule
// above. Per the ticket's requirement ("files with no specific mapping
// should fall back to recommending the full gate set rather than silently
// recommending nothing") and mirroring discovery's fail-safe-to-approval-
// required philosophy, an unmapped change must never result in zero
// recommended checks: it recommends the entire full gate set as focused
// checks, each carrying a Reason that names the unmapped path so a human
// reading the plan understands why the "focused" recommendation is, in this
// case, everything.
func fallbackToFullGates(path string) []CheckSpec {
	out := make([]CheckSpec, 0, len(FullGates))
	for _, g := range FullGates {
		out = append(out, CheckSpec{
			Name:         g.Name,
			Command:      g.Command,
			Category:     provenance.VerificationFocused,
			Reason:       fmt.Sprintf("no focused-check rule matched %q; falling back to the full gate set out of caution rather than recommending nothing", path),
			RequiredTool: g.RequiredTool,
			Networked:    g.Networked,
		})
	}
	return out
}

// isWorkflowFile reports whether path is a GitHub Actions workflow file.
func isWorkflowFile(path string) bool {
	if !strings.HasPrefix(path, ".github/workflows/") {
		return false
	}
	return strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")
}

// isCheckScript reports whether path is one of scripts/check-*.sh.
func isCheckScript(path string) bool {
	if !strings.HasPrefix(path, "scripts/check-") {
		return false
	}
	return strings.HasSuffix(path, ".sh")
}

// topLevelSegment returns the first "/"-separated segment of path, or ""
// if path contains no "/" (i.e. path is itself at the top level).
func topLevelSegment(path string) string {
	idx := strings.Index(path, "/")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

// dedupeAndSort removes duplicate checks (same Name and Command) and sorts
// the result deterministically by Category then Name, so PlanFor's output
// does not depend on the order changedFiles was given in. Always returns a
// non-nil slice, even for empty input, matching the sorted-never-nil
// discipline discovery.Repository's slice fields already follow.
func dedupeAndSort(checks []CheckSpec) []CheckSpec {
	seen := make(map[string]bool, len(checks))
	out := make([]CheckSpec, 0, len(checks))
	for _, c := range checks {
		key := string(c.Category) + "\x00" + c.Name + "\x00" + c.Command
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

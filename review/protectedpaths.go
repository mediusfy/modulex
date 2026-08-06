package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// gitOutput resolves an absolute path to the git executable via
// exec.LookPath — rather than letting exec.Command search PATH implicitly
// at invocation time (SonarQube go:S4036 / CWE-427, "uncontrolled search
// path element": an attacker able to prepend a malicious "git" earlier in
// PATH could otherwise have their binary invoked instead of the real one) —
// then runs `git [-C dir] args...` and returns stdout. dir, if non-empty,
// is the repository the command runs against; empty leaves off -C
// entirely, so git resolves the repository from the calling process's own
// current working directory.
//
// Uses an argv slice (exec.CommandContext), never "sh -c", so dir/args —
// any of which a caller might derive from CI event data or an MCP tool
// call — cannot inject shell syntax regardless of content; a malformed
// argument simply makes the git invocation itself fail, returned as an
// error with git's stderr attached when non-empty. Shared by every git
// invocation in this file (ChangedFiles, gitShow, singleFileDiff) so the
// argv-building/stderr-capture/error-wrap logic exists exactly once.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}

	fullArgs := make([]string, 0, len(args)+2)
	if dir != "" {
		fullArgs = append(fullArgs, "-C", dir)
	}
	fullArgs = append(fullArgs, args...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, gitPath, fullArgs...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return string(out), nil
}

// ChangedFiles returns the list of file paths changed between baseRef and
// headRef (git's "A...B" triple-dot form — the same diff scope gitDiff uses
// for ScanSecrets), via `git diff --name-only`. Exported so other packages
// (CheckProtectedPaths below, and tools/mcpserver's find_affected_modules)
// can compute "what changed" once and reuse it, rather than each shelling
// out to git separately.
func ChangedFiles(ctx context.Context, dir, baseRef, headRef string) ([]string, error) {
	out, err := gitOutput(ctx, dir, "diff", "--name-only", "--no-color", fmt.Sprintf("%s...%s", baseRef, headRef))
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// CheckProtectedPaths reports whether any file changed between baseRef and
// headRef matches one of protectedPaths (contract.Contract.ProtectedPaths —
// passed as []string rather than a contract.Contract so this package stays
// decoupled from contract's YAML/validation concerns, the same way Review
// already takes tools []discovery.ToolStatus rather than importing
// discovery to compute it), returning one provenance.VerificationResult
// with Category VerificationProtectedPaths.
//
// Each pattern is matched with path.Match, not filepath.Match: changed-file
// paths from `git diff --name-only` are always "/"-separated regardless of
// the host OS, and path.Match's "*" (matching within one path segment, not
// across "/") is exactly the glob semantics
// docs/planning/agent-safety-policy.md's protected-paths examples assume
// (e.g. ".github/workflows/*.yml"). A malformed pattern (path.ErrBadPattern)
// is treated as never matching that one pattern rather than failing the
// whole check — contract.Contract.Validate rejects a malformed pattern at
// the source, so this is a defense-in-depth fallback for a contract that
// bypassed validation, not the primary guard against a silent typo.
//
// # File-scoped exceptions for CHANGELOG.md and go.mod
//
// docs/planning/agent-safety-policy.md's protected-paths list is narrower
// than "any change" for two of its entries: adding to CHANGELOG.md's
// "## [Unreleased]" section is "expected and encouraged" (only its
// already-released version-section boundaries are protected), and go.mod's
// protection is scoped to "retract directives and any published version's
// git tag" (an ordinary require/version edit is not protected). A
// file-level match on either name — no line or section granularity — would
// flag routine, policy-permitted edits (this package's own CHANGELOG.md
// entries, a dependency bump) as violations on every such change, training
// reviewers to ignore or override the check. changelogEditIsWithinUnreleased
// and goModEditTouchesOnlyNonRetractLines inspect the actual diff content
// for exactly these two well-known file names to apply the same exception
// the policy document grants; every other protectedPaths entry keeps the
// simple "any change to this path is a hit" semantics.
//
// Empty protectedPaths reports StatusPass trivially: no contract, or a
// contract that declares no protected paths, is a normal state, not a
// finding to flag (mirroring ScanSecrets and the rest of this package's
// "absence is not failure" discipline). A `git diff` failure reports
// StatusUnavailable, distinguishing "couldn't compute the diff" from "no
// protected path was touched."
func CheckProtectedPaths(ctx context.Context, dir, baseRef, headRef string, protectedPaths []string) provenance.VerificationResult {
	const name = "check-protected-paths"

	if len(protectedPaths) == 0 {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationProtectedPaths,
			Status:   provenance.StatusPass,
			Reason:   "no protected_paths declared in modulex.agent.yaml",
		}
	}

	changed, err := ChangedFiles(ctx, dir, baseRef, headRef)
	if err != nil {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationProtectedPaths,
			Status:   provenance.StatusUnavailable,
			Reason:   fmt.Sprintf("could not compute diff %s...%s: %v", baseRef, headRef, err),
		}
	}

	var hits []string
	for _, file := range changed {
		for _, pattern := range protectedPaths {
			matched, matchErr := path.Match(pattern, file)
			if matchErr != nil || !matched {
				continue
			}
			if file == "CHANGELOG.md" && changelogEditIsWithinUnreleased(ctx, dir, baseRef, headRef) {
				break
			}
			if file == "go.mod" && goModEditTouchesOnlyNonRetractLines(ctx, dir, baseRef, headRef) {
				break
			}
			hits = append(hits, fmt.Sprintf("%s matches protected path %q", file, pattern))
			break
		}
	}

	if len(hits) == 0 {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationProtectedPaths,
			Status:   provenance.StatusPass,
		}
	}

	// changed (and hits, since at most one entry per changed file is
	// appended, in changed's iteration order) is already in git's sorted
	// path order — ChangedFiles' `git diff --name-only` output is
	// lexically sorted — so no separate sort is needed here.
	return provenance.VerificationResult{
		Name:     name,
		Category: provenance.VerificationProtectedPaths,
		Status:   provenance.StatusFail,
		Message: fmt.Sprintf("%d changed file(s) touch a protected path (requires explicit human approval, see docs/planning/agent-safety-policy.md):\n%s",
			len(hits), strings.Join(hits, "\n")),
	}
}

// gitShow returns the content of file as it exists at ref, in the
// repository at dir (or the calling process's cwd if dir is empty).
func gitShow(ctx context.Context, dir, ref, file string) (string, error) {
	return gitOutput(ctx, dir, "show", ref+":"+file)
}

// singleFileDiff returns the --unified=0 unified diff for exactly file
// between baseRef and headRef, in the repository at dir. Scoping the
// pathspec (`-- file`) keeps this cheap even on a large changeset, since
// it's only called for the two file names (CHANGELOG.md, go.mod) that need
// content-aware exception handling, and only once each has already been
// confirmed present in ChangedFiles' output.
func singleFileDiff(ctx context.Context, dir, baseRef, headRef, file string) (string, error) {
	return gitOutput(ctx, dir, "diff", "--unified=0", "--no-color", fmt.Sprintf("%s...%s", baseRef, headRef), "--", file)
}

// diffHunk is one @@ ... @@ hunk from a --unified=0 unified diff: the
// 1-indexed starting line and line count on each side. A count of 0 means
// that side contributes no lines (a pure insertion has oldCount 0; a pure
// deletion has newCount 0).
type diffHunk struct {
	oldStart, oldCount int
	newStart, newCount int
}

// fullHunkHeaderPattern captures all four @@ -oldStart[,oldCount]
// +newStart[,newCount] @@ fields; a missing ",count" means count 1 (git's
// own convention for a single-line side).
var fullHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseHunks extracts every hunk header from a single-file --unified=0 diff
// (as produced by singleFileDiff).
func parseHunks(diff string) []diffHunk {
	var hunks []diffHunk
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		m := fullHunkHeaderPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}
		hunks = append(hunks, diffHunk{oldStart, oldCount, newStart, newCount})
	}
	return hunks
}

// splitLines splits content into 1-indexed lines (lines[i-1] is line i),
// dropping the single trailing empty element strings.Split produces for
// content ending in "\n" — so len(lines) matches the line count git itself
// uses in hunk headers, rather than being off by one for any file with a
// trailing newline (the common case).
func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// versionSectionHeaderPattern matches a Keep a Changelog-style version
// section header, e.g. "## [Unreleased]" or "## [1.2.0] - 2026-01-01".
var versionSectionHeaderPattern = regexp.MustCompile(`^## \[`)

// unreleasedSectionRange returns the 1-indexed [start,end] line range of
// content's "## [Unreleased]" section: the header line itself through the
// line before the next version-section header, or through the end of the
// file if it's the last section. The zero value ([2]int{}) means no
// "## [Unreleased]" header was found at all.
func unreleasedSectionRange(content string) [2]int {
	lines := splitLines(content)
	for i, line := range lines {
		if !strings.HasPrefix(line, "## [Unreleased]") {
			continue
		}
		start := i + 1
		for j := i + 1; j < len(lines); j++ {
			if versionSectionHeaderPattern.MatchString(lines[j]) {
				return [2]int{start, j}
			}
		}
		return [2]int{start, len(lines)}
	}
	return [2]int{}
}

// rangeWithin reports whether the 1-indexed [start, start+count-1] span is
// entirely inside r. A count of 0 (nothing on that side of a diff hunk) is
// vacuously within any range, including the zero range. A non-empty span is
// never within the zero range (no "## [Unreleased]" section was found).
func rangeWithin(start, count int, r [2]int) bool {
	if count == 0 {
		return true
	}
	if r == ([2]int{}) {
		return false
	}
	end := start + count - 1
	return start >= r[0] && end <= r[1]
}

// changelogEditIsWithinUnreleased reports whether every hunk in CHANGELOG.md's
// diff between baseRef and headRef falls entirely within the "## [Unreleased]"
// section on both sides — the exception docs/planning/agent-safety-policy.md
// grants ("adding to `## [Unreleased]` is expected and encouraged", only
// "version-section boundaries for already-released versions" are
// protected). Returns false (no exception — CheckProtectedPaths should
// treat the change as a hit) whenever the answer can't be established
// safely: CHANGELOG.md missing from either ref, the diff itself failing, or
// any hunk touching content outside the Unreleased section on either side
// (including a hunk that touches the "## [Unreleased]" header line itself,
// e.g. cutting a release) — fail-safe by design, matching this package's
// existing failure-mode discipline (an inability to prove the exception
// applies is not the same as the exception applying).
func changelogEditIsWithinUnreleased(ctx context.Context, dir, baseRef, headRef string) bool {
	const file = "CHANGELOG.md"

	baseContent, err := gitShow(ctx, dir, baseRef, file)
	if err != nil {
		return false
	}
	headContent, err := gitShow(ctx, dir, headRef, file)
	if err != nil {
		return false
	}
	diff, err := singleFileDiff(ctx, dir, baseRef, headRef, file)
	if err != nil {
		return false
	}

	baseRange := unreleasedSectionRange(baseContent)
	headRange := unreleasedSectionRange(headContent)

	hunks := parseHunks(diff)
	if len(hunks) == 0 {
		return true
	}
	for _, h := range hunks {
		if !rangeWithin(h.oldStart, h.oldCount, baseRange) {
			return false
		}
		if !rangeWithin(h.newStart, h.newCount, headRange) {
			return false
		}
	}
	return true
}

// retractLineRanges returns the 1-indexed [start,end] line ranges in a
// go.mod file's content that belong to a `retract` directive: either a
// single line (`retract v1.2.3 // reason`, or bare `retract vX.Y.Z`) or a
// parenthesized block:
//
//	retract (
//	    v1.2.3 // reason
//	)
//
// covering the `retract (` line through the matching `)` line.
func retractLineRanges(content string) [][2]int {
	var ranges [][2]int
	lines := splitLines(content)

	inBlock := false
	blockStart := 0
	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)
		if inBlock {
			if trimmed == ")" {
				ranges = append(ranges, [2]int{blockStart, lineNo})
				inBlock = false
			}
			continue
		}
		if trimmed == "retract (" {
			inBlock = true
			blockStart = lineNo
			continue
		}
		if trimmed == "retract" || strings.HasPrefix(trimmed, "retract ") {
			ranges = append(ranges, [2]int{lineNo, lineNo})
		}
	}
	return ranges
}

// rangeIntersectsAny reports whether the 1-indexed [start, start+count-1]
// span overlaps any range in ranges. A count of 0 (nothing on that side of
// a diff hunk) never overlaps anything.
func rangeIntersectsAny(start, count int, ranges [][2]int) bool {
	if count == 0 {
		return false
	}
	end := start + count - 1
	for _, r := range ranges {
		if start <= r[1] && end >= r[0] {
			return true
		}
	}
	return false
}

// goModEditTouchesOnlyNonRetractLines reports whether every hunk in
// go.mod's diff between baseRef and headRef avoids every `retract`
// directive line on both sides — the exception
// docs/planning/agent-safety-policy.md grants ("go.mod `retract` directives
// ... are protected"; an ordinary require/version edit is not). Returns
// false (no exception — CheckProtectedPaths should treat the change as a
// hit) whenever the answer can't be established safely: go.mod missing
// from either ref, the diff itself failing, or any hunk overlapping a
// `retract` directive's line range on either side — fail-safe by design,
// mirroring changelogEditIsWithinUnreleased.
func goModEditTouchesOnlyNonRetractLines(ctx context.Context, dir, baseRef, headRef string) bool {
	const file = "go.mod"

	baseContent, err := gitShow(ctx, dir, baseRef, file)
	if err != nil {
		return false
	}
	headContent, err := gitShow(ctx, dir, headRef, file)
	if err != nil {
		return false
	}
	diff, err := singleFileDiff(ctx, dir, baseRef, headRef, file)
	if err != nil {
		return false
	}

	baseRanges := retractLineRanges(baseContent)
	headRanges := retractLineRanges(headContent)

	hunks := parseHunks(diff)
	if len(hunks) == 0 {
		return true
	}
	for _, h := range hunks {
		if rangeIntersectsAny(h.oldStart, h.oldCount, baseRanges) {
			return false
		}
		if rangeIntersectsAny(h.newStart, h.newCount, headRanges) {
			return false
		}
	}
	return true
}

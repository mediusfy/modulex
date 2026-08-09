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

// errInvalidRef is returned when a git ref argument is empty or starts with
// '-', which would be misinterpreted by git as a command-line option.
var errInvalidRef = fmt.Errorf("git ref must not be empty or start with '-'")

// validateRef returns errInvalidRef if ref is empty or starts with a '-'
// character, preventing a malicious or malformed ref from being parsed by git
// as a command-line option.
func validateRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return errInvalidRef
	}
	return nil
}

// gitOutput resolves git via exec.LookPath (rather than an unqualified
// "git", which SonarQube's go:S4036/CWE-427 flags as a PATH-search risk)
// and runs `git [-C dir] args...`, returning stdout. Shared by every git
// invocation in this package, including secrets.go's gitDiff.
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

// ChangedFiles returns the file paths changed between baseRef and headRef
// (`git diff --name-only`). Exported so other packages (CheckProtectedPaths
// below, find_affected_modules) can reuse it.
func ChangedFiles(ctx context.Context, dir, baseRef, headRef string) ([]string, error) {
	if err := validateRef(baseRef); err != nil {
		return nil, err
	}
	if err := validateRef(headRef); err != nil {
		return nil, err
	}
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
// headRef matches one of protectedPaths (contract.Contract.ProtectedPaths),
// returning one provenance.VerificationResult with Category
// VerificationProtectedPaths.
//
// Patterns are matched with path.Match against "/"-separated paths, giving
// "*" single-segment glob semantics. CHANGELOG.md and go.mod get a
// file-scoped exception matching docs/planning/agent-safety-policy.md:
// adding to CHANGELOG.md's "## [Unreleased]" section is allowed, and only
// go.mod's `retract` directives are protected — see
// changelogEditIsWithinUnreleased and goModEditTouchesOnlyNonRetractLines.
// Every other path keeps plain "any change is a hit" matching.
//
// A pattern that fails to compile as a path.Match glob (an unmatched "[")
// cannot protect anything, so it is reported as a StatusFail naming the
// pattern rather than silently treated as "never matches" — the remaining
// valid patterns are still enforced in the same pass.
//
// Empty protectedPaths passes trivially (no contract, or none declared). A
// `git diff` failure reports StatusUnavailable, not a pass or fail.
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

	validPatterns, invalidPatterns := partitionGlobs(protectedPaths)
	problems := make([]string, 0, len(invalidPatterns))
	for _, pattern := range invalidPatterns {
		problems = append(problems, fmt.Sprintf("protected path %q is not a valid glob pattern and cannot be enforced (fix modulex.agent.yaml)", pattern))
	}

	changed, err := ChangedFiles(ctx, dir, baseRef, headRef)
	if err != nil {
		// A diff failure must not swallow the malformed-glob diagnostic —
		// broken git state and a broken contract can coincide, and the
		// caller needs to see both.
		reason := fmt.Sprintf("could not compute diff %s...%s: %v", baseRef, headRef, err)
		if len(problems) > 0 {
			reason += "; " + strings.Join(problems, "; ")
		}
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationProtectedPaths,
			Status:   provenance.StatusUnavailable,
			Reason:   reason,
		}
	}

	var hits []string
	for _, file := range changed {
		for _, pattern := range validPatterns {
			if matched, _ := path.Match(pattern, file); !matched {
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

	// changed is already git-sorted, and at most one hit is appended per
	// file, so hits needs no separate sort.
	problems = append(problems, hits...)

	if len(problems) == 0 {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationProtectedPaths,
			Status:   provenance.StatusPass,
		}
	}

	return provenance.VerificationResult{
		Name:     name,
		Category: provenance.VerificationProtectedPaths,
		Status:   provenance.StatusFail,
		Message: fmt.Sprintf("%d protected-paths problem(s) (a protected-path change requires explicit human approval, see docs/planning/agent-safety-policy.md):\n%s",
			len(problems), strings.Join(problems, "\n")),
	}
}

// partitionGlobs splits patterns into those that compile as path.Match
// globs and those that don't (path.ErrBadPattern) — Match validates the
// full pattern syntax even when the name doesn't match, so "" suffices as
// the probe name.
func partitionGlobs(patterns []string) (valid, invalid []string) {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			invalid = append(invalid, p)
		} else {
			valid = append(valid, p)
		}
	}
	return valid, invalid
}

// gitShow returns file's content as of ref.
func gitShow(ctx context.Context, dir, ref, file string) (string, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}
	return gitOutput(ctx, dir, "show", ref+":"+file)
}

// singleFileDiff returns the --unified=0 diff for exactly file between
// baseRef and headRef.
func singleFileDiff(ctx context.Context, dir, baseRef, headRef, file string) (string, error) {
	if err := validateRef(baseRef); err != nil {
		return "", err
	}
	if err := validateRef(headRef); err != nil {
		return "", err
	}
	return gitOutput(ctx, dir, "diff", "--unified=0", "--no-color", fmt.Sprintf("%s...%s", baseRef, headRef), "--", file)
}

// diffHunk is one @@ ... @@ hunk: the 1-indexed starting line and line
// count on each side. Count 0 means that side contributes no lines.
type diffHunk struct {
	oldStart, oldCount int
	newStart, newCount int
}

// fullHunkHeaderPattern matches @@ -oldStart[,oldCount] +newStart[,newCount] @@.
var fullHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseHunks extracts every hunk header from a --unified=0 diff.
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

// splitLines splits content into 1-indexed lines, dropping the trailing
// empty element a trailing "\n" produces so len(lines) matches git's own
// line count.
func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// versionSectionHeaderPattern matches a Keep a Changelog version header,
// e.g. "## [Unreleased]" or "## [1.2.0] - 2026-01-01".
var versionSectionHeaderPattern = regexp.MustCompile(`^## \[`)

// unreleasedSectionRange returns the 1-indexed [start,end] line range of
// content's "## [Unreleased]" section, or the zero value if not found.
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

// rangeWithin reports whether [start, start+count-1] is entirely inside r.
// count 0 is vacuously within any range; a non-empty span is never within
// the zero range.
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

// fileExceptionApplies fetches file's content at baseRef and headRef and
// their diff, then reports whether hunkIsSafe holds for every hunk. Returns
// false (fail-safe) if file can't be read at either ref or the diff fails.
// Shared by changelogEditIsWithinUnreleased and
// goModEditTouchesOnlyNonRetractLines.
func fileExceptionApplies(ctx context.Context, dir, baseRef, headRef, file string, hunkIsSafe func(h diffHunk, baseContent, headContent string) bool) bool {
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

	for _, h := range parseHunks(diff) {
		if !hunkIsSafe(h, baseContent, headContent) {
			return false
		}
	}
	return true
}

// changelogEditIsWithinUnreleased reports whether every hunk in
// CHANGELOG.md's diff stays within the "## [Unreleased]" section on both
// sides — docs/planning/agent-safety-policy.md's exception for CHANGELOG.md.
func changelogEditIsWithinUnreleased(ctx context.Context, dir, baseRef, headRef string) bool {
	return fileExceptionApplies(ctx, dir, baseRef, headRef, "CHANGELOG.md", func(h diffHunk, baseContent, headContent string) bool {
		return rangeWithin(h.oldStart, h.oldCount, unreleasedSectionRange(baseContent)) &&
			rangeWithin(h.newStart, h.newCount, unreleasedSectionRange(headContent))
	})
}

// retractLineRanges returns the 1-indexed [start,end] ranges in go.mod
// content belonging to a `retract` directive — a single line, or a
// `retract ( ... )` block.
func retractLineRanges(content string) [][2]int {
	var ranges [][2]int
	lines := splitLines(content)

	inBlock := false
	blockStart := 0
	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)
		if inBlock {
			if isRetractBlockClose(trimmed) {
				ranges = append(ranges, [2]int{blockStart, lineNo})
				inBlock = false
			}
			continue
		}
		rest, isRetract := cutRetractKeyword(trimmed)
		if !isRetract {
			continue
		}
		if isRetractBlockOpen(rest) {
			inBlock = true
			blockStart = lineNo
			continue
		}
		ranges = append(ranges, [2]int{lineNo, lineNo})
	}
	return ranges
}

// cutRetractKeyword reports whether a trimmed line is a `retract`
// directive — the keyword alone or followed by whitespace or "(" (the
// modfile lexer treats "(" as punctuation, so `retract(` is also a block
// open) — returning what follows the keyword with surrounding whitespace
// removed. Identifiers merely sharing the prefix ("retracted") don't count.
func cutRetractKeyword(trimmed string) (rest string, ok bool) {
	rest, ok = strings.CutPrefix(trimmed, "retract")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '(') {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// isRetractBlockOpen reports whether rest (what follows the `retract`
// keyword) opens a `retract ( ... )` block: a "(" alone or followed only
// by a trailing line comment, e.g. `retract ( // pre-1.0 was broken`.
func isRetractBlockOpen(rest string) bool {
	after, ok := strings.CutPrefix(rest, "(")
	if !ok {
		return false
	}
	after = strings.TrimSpace(after)
	return after == "" || strings.HasPrefix(after, "//")
}

// isRetractBlockClose reports whether a trimmed line closes a `retract (
// ... )` block: a bare ")" or a ")" followed only by a trailing line
// comment, e.g. `) // pre-1.0 releases had a critical bug`.
func isRetractBlockClose(trimmed string) bool {
	if !strings.HasPrefix(trimmed, ")") {
		return false
	}
	rest := strings.TrimSpace(trimmed[1:])
	return rest == "" || strings.HasPrefix(rest, "//")
}

// rangeIntersectsAny reports whether [start, start+count-1] overlaps any
// range in ranges. count 0 never overlaps.
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
// go.mod's diff avoids every `retract` directive line on both sides —
// docs/planning/agent-safety-policy.md's exception for go.mod.
func goModEditTouchesOnlyNonRetractLines(ctx context.Context, dir, baseRef, headRef string) bool {
	return fileExceptionApplies(ctx, dir, baseRef, headRef, "go.mod", func(h diffHunk, baseContent, headContent string) bool {
		return !rangeIntersectsAny(h.oldStart, h.oldCount, retractLineRanges(baseContent)) &&
			!rangeIntersectsAny(h.newStart, h.newCount, retractLineRanges(headContent))
	})
}

package review

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// maxSecretFindings bounds how many findings ScanSecrets includes in its
// VerificationResult.Message, so a diff with many secret-shaped additions
// (e.g. an accidentally committed key file, one match per line) cannot make
// the message grow without limit. Mirrors verify/run.go's maxOutputBytes
// truncation discipline for the same reason.
const maxSecretFindings = 20

// hunkHeaderPattern extracts the starting line number of the "new" (+) side
// of a unified diff hunk header, e.g. "@@ -10,0 +11,2 @@ func foo() {"
// captures "11". The count and any trailing function-context text (added by
// git, not requested here) are ignored.
var hunkHeaderPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ScanSecrets runs a best-effort secret scan over the lines added between
// baseRef and headRef (git's "A...B" triple-dot form: everything reachable
// from headRef but not from baseRef's merge-base — the same diff scope
// scripts/check-changelog.sh uses), returning one provenance.
// VerificationResult with Category VerificationSecretScan.
//
// Only added (+) lines are scanned, using provenance.RedactSecrets — the
// same best-effort, pattern-based detection provenance.Envelope.Redact uses
// internally (see its doc comment for what it does and does not catch).
// Scanning is diff-scoped deliberately: see the package doc comment's
// "Boundary and compatibility checks are not diff-scoped" for why a
// repository-wide scan would be worse, not better, here.
//
// A finding's Message never contains the raw secret value: each reported
// line is passed through RedactSecrets before being included, so only the
// redacted form is ever recorded. Findings are capped at maxSecretFindings;
// a diff with more reports the first maxSecretFindings plus a count of the
// rest.
//
// If `git diff` itself fails (e.g. baseRef or headRef does not exist, or
// this is not a git repository), ScanSecrets returns
// provenance.StatusUnavailable rather than treating an inability to compute
// the diff as either a pass or a fail.
func ScanSecrets(ctx context.Context, baseRef, headRef string) provenance.VerificationResult {
	const name = "check-secrets"

	diff, err := gitDiff(ctx, baseRef, headRef)
	if err != nil {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationSecretScan,
			Status:   provenance.StatusUnavailable,
			Reason:   fmt.Sprintf("could not compute diff %s...%s: %v", baseRef, headRef, err),
		}
	}

	var findings []string
	for _, l := range addedLines(diff) {
		redacted, found := provenance.RedactSecrets(l.text)
		if !found {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s:%d: %s", l.file, l.line, redacted))
	}

	if len(findings) == 0 {
		return provenance.VerificationResult{
			Name:     name,
			Category: provenance.VerificationSecretScan,
			Status:   provenance.StatusPass,
		}
	}

	shown := findings
	suffix := ""
	if len(shown) > maxSecretFindings {
		shown = shown[:maxSecretFindings]
		suffix = fmt.Sprintf("\n... and %d more", len(findings)-maxSecretFindings)
	}
	return provenance.VerificationResult{
		Name:     name,
		Category: provenance.VerificationSecretScan,
		Status:   provenance.StatusFail,
		Message:  fmt.Sprintf("found %d secret-shaped value(s) added in this diff (values redacted):\n%s%s", len(findings), strings.Join(shown, "\n"), suffix),
	}
}

// gitDiff returns the unified diff (context-free: --unified=0, so every
// output line is either a hunk header or an actual addition/removal) for
// everything headRef introduced since it diverged from baseRef. It shells
// out via exec.CommandContext with an argv slice (no "sh -c"), so baseRef
// and headRef — which a caller might derive from CI event data — cannot
// inject shell syntax regardless of content; a malformed ref simply makes
// the git invocation itself fail, which ScanSecrets reports as
// StatusUnavailable.
func gitDiff(ctx context.Context, baseRef, headRef string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "diff", "--unified=0", "--no-color", fmt.Sprintf("%s...%s", baseRef, headRef))
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

// addedLine is one "+" line from a unified diff, with the file and
// (post-change) line number it was added at, recovered from the diff's
// "+++ " and "@@ " header lines.
type addedLine struct {
	file string
	line int
	text string
}

// addedLines extracts every added (+) line from diff (as produced by
// gitDiff), tracking the current file (from "+++ b/<path>" lines) and line
// number (from "@@ -a,b +c,d @@" hunk headers, incrementing per added line)
// as it scans. A line whose file is "/dev/null" (the "new" side of a pure
// deletion — --unified=0 never actually emits a "+" line under it, but this
// is guarded defensively) is skipped.
func addedLines(diff string) []addedLine {
	var out []addedLine
	file := ""
	nextLine := 0

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "@@ "):
			if m := hunkHeaderPattern.FindStringSubmatch(line); m != nil {
				nextLine, _ = strconv.Atoi(m[1])
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if file != "" && file != "/dev/null" {
				out = append(out, addedLine{file: file, line: nextLine, text: line[1:]})
			}
			nextLine++
		}
	}
	return out
}

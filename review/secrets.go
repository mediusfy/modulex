package review

import (
	"context"
	"fmt"
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

// strictGenericSecretPattern is this package's own, source-code-tuned
// variant of provenance's genericAssignmentPattern (the key/token/password/
// secret catch-all excluded from provenance.RedactHighConfidenceSecrets —
// see that pattern's doc comment). It requires the assigned value to be a
// quoted string literal immediately following the operator (':=', '=', or
// ':') — not a bare identifier, typed constant, or function call — which
// eliminates the dominant class of false positive found scanning real Go
// source:
//
//	token = hex.EncodeToString(buf)           // unquoted expression
//	var ServiceKey = modulex.NewKey[Sender](…) // unquoted, compound "Key" identifier
//	&secretService{APIKey: secretValue}        // unquoted identifier reference
//	// ... the right token: denied.            // narrative comment, no literal
//
// none of which assign a quoted literal, so none match. A genuinely
// hardcoded secret in source code almost always does appear as a quoted
// string literal (including via Go's ":=" short variable declaration, e.g.
// `apiKey := "..."`, hence matching ":=" explicitly rather than only a
// single ':' or '=' character), so this trade favors precision over recall
// deliberately; see ScanSecrets' doc comment for the "nosecret" escape
// hatch covering cases this still over- or under-catches.
var strictGenericSecretPattern = regexp.MustCompile(`(?i)(key|token|password|secret)\s*(:=|[:=])\s*(['"])[^\s'"]{6,}['"]`)

// nosecretMarker, when present anywhere in an added line (case-insensitive),
// suppresses that line from ScanSecrets' findings regardless of pattern
// matches — the escape hatch for lines that are secret-shaped on purpose
// (a test fixture asserting redaction behavior, a doc line illustrating a
// pattern) rather than by accident. Mirrors the `#nosec`/`// nolint`
// convention other static-analysis tools in this ecosystem use. Example:
//
//	token := "fake-test-token-value" // nosecret: test fixture
const nosecretMarker = "nosecret"

// hasNosecretMarker reports whether line contains nosecretMarker,
// case-insensitively.
func hasNosecretMarker(line string) bool {
	return strings.Contains(strings.ToLower(line), nosecretMarker)
}

// redactLine runs both provenance.RedactHighConfidenceSecrets (the precise,
// non-generic patterns: AWS/PEM/GitHub/JWT) and strictGenericSecretPattern
// against line, returning the fully redacted text and whether either
// matched.
func redactLine(line string) (string, bool) {
	redacted, found := provenance.RedactHighConfidenceSecrets(line)
	if strictGenericSecretPattern.MatchString(redacted) {
		redacted = strictGenericSecretPattern.ReplaceAllString(redacted, provenance.RedactionMarker)
		found = true
	}
	return redacted, found
}

// ScanSecrets runs a best-effort secret scan over the lines added between
// baseRef and headRef (git's "A...B" triple-dot form: everything reachable
// from headRef but not from baseRef's merge-base — the same diff scope
// scripts/check-changelog.sh uses), returning one provenance.
// VerificationResult with Category VerificationSecretScan.
//
// Only added (+) lines are scanned, using redactLine: provenance.
// RedactHighConfidenceSecrets (AWS/PEM/GitHub/JWT — precise, low-noise
// shapes) plus this package's own strictGenericSecretPattern (a
// quote-required variant of provenance's looser generic catch-all, tuned
// for source code rather than command output; see that pattern's doc
// comment for why). Scanning is diff-scoped deliberately: see the package
// doc comment's "Boundary and compatibility checks are not diff-scoped" for
// why a repository-wide scan would be worse, not better, here.
//
// A line containing nosecretMarker ("nosecret", case-insensitive) is never
// flagged, regardless of pattern matches — see its doc comment.
//
// A finding's Message never contains the raw secret value: each reported
// line is passed through redactLine before being included, so only the
// redacted form is ever recorded. Findings are capped at maxSecretFindings;
// a diff with more reports the first maxSecretFindings plus a count of the
// rest.
//
// If `git diff` itself fails (e.g. baseRef or headRef does not exist, or
// this is not a git repository), ScanSecrets returns
// provenance.StatusUnavailable rather than treating an inability to compute
// the diff as either a pass or a fail.
//
// dir, if non-empty, is the repository gitDiff runs `git -C dir diff ...`
// against; empty leaves off -C entirely, so git resolves the repository
// from ScanSecrets' own calling process's current working directory —
// unchanged from this function's behavior before dir existed as a
// parameter.
func ScanSecrets(ctx context.Context, dir, baseRef, headRef string) provenance.VerificationResult {
	const name = "check-secrets"

	diff, err := gitDiff(ctx, dir, baseRef, headRef)
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
		if hasNosecretMarker(l.text) {
			continue
		}
		redacted, found := redactLine(l.text)
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
// everything headRef introduced since it diverged from baseRef, in the
// repository at dir (or ScanSecrets' calling process's cwd if dir is
// empty). Delegates to gitOutput (protectedpaths.go) for the actual
// invocation, so this gets the same exec.LookPath resolution — rather than
// letting exec.Command search PATH implicitly — and the same argv-slice (no
// "sh -c") shell-injection immunity every other git invocation in this
// package shares; a malformed ref or dir simply makes the git invocation
// itself fail, which ScanSecrets reports as StatusUnavailable.
func gitDiff(ctx context.Context, dir, baseRef, headRef string) (string, error) {
	return gitOutput(ctx, dir, "diff", "--unified=0", "--no-color", fmt.Sprintf("%s...%s", baseRef, headRef))
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

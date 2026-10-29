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

// hunkHeaderPattern extracts the old-side line count and the new-side
// starting line and count from a unified diff hunk header, e.g.
// "@@ -10,0 +11,2 @@ func foo() {" captures oldCount="0", newStart="11",
// newCount="2" (a count form with no explicit ",N" defaults to 1, per the
// unified diff format). Any trailing function-context text (added by git,
// not requested here) is ignored. addedLines uses the old+new counts to
// consume exactly a hunk's body by position rather than by re-matching
// each body line's own prefix (see addedLines' doc comment for why that
// distinction matters).
var hunkHeaderPattern = regexp.MustCompile(`^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

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
var strictGenericSecretPattern = regexp.MustCompile(`(?i)(key|token|password|secret)\s*(:=|[:=])\s*['"]([^\s'"]{6,})['"]`)

// identifierLikeValue matches a short chain of alphanumeric words joined by
// '-' or '_' — the shape of a human-readable identifier ("pr-42-fix",
// "my_feature_branch"), not credential material. Real secrets are either
// long (so the < 16 length guard in secretShapedValue keeps them flagged) or
// a single unbroken run of characters (no separators, so this never
// matches). Found in practice: `ticket_key="pr-42-fix"` in a consumer
// repository's tests flagged by the generic pattern above.
var identifierLikeValue = regexp.MustCompile(`^[A-Za-z0-9]+(?:[-_][A-Za-z0-9]+)+$`)

// secretShapedValue reports whether a value captured by
// strictGenericSecretPattern actually looks like credential material: short
// separator-joined word chains are identifiers, everything else keeps the
// pattern's precision-tuned verdict. The "nosecret" marker remains the
// escape hatch for deliberate secret-shaped fixtures this heuristic still
// flags.
func secretShapedValue(value string) bool {
	return len(value) >= 16 || !identifierLikeValue.MatchString(value)
}

// nosecretMarkerPattern matches a well-formed, trailing "// nosecret: <reason>"
// line comment (case-insensitive; the word "nosecret" exactly, not merely as
// a substring of some other identifier) that suppresses that line from
// ScanSecrets' findings regardless of pattern matches — the escape hatch for
// lines that are secret-shaped on purpose (a test fixture asserting
// redaction behavior, a doc line illustrating a pattern) rather than by
// accident. Requiring the "//" comment form and a non-empty reason after the
// colon means the marker can't be accidentally or maliciously triggered by
// an unrelated identifier that merely happens to contain "nosecret" (e.g. a
// variable named nosecretDBPassword), and it leaves an audit trail of why
// the suppression exists — a bare "// nosecret" with no reason does not
// match. Mirrors the `#nosec`/`// nolint` convention other static-analysis
// tools in this ecosystem use. Example:
//
//	token := "fake-test-token-value" // nosecret: test fixture
var nosecretMarkerPattern = regexp.MustCompile(`(?i)//\s*nosecret\b\s*:\s*\S`)

// hasNosecretMarker reports whether line carries a well-formed
// "// nosecret: <reason>" marker; see nosecretMarkerPattern.
func hasNosecretMarker(line string) bool {
	return nosecretMarkerPattern.MatchString(line)
}

// redactLine runs both provenance.RedactHighConfidenceSecrets (the precise,
// non-generic patterns: AWS/PEM/GitHub/JWT) and strictGenericSecretPattern
// against line, returning the fully redacted text and whether either
// matched.
func redactLine(line string) (string, bool) {
	redacted, found := provenance.RedactHighConfidenceSecrets(line)
	redacted = strictGenericSecretPattern.ReplaceAllStringFunc(redacted, func(m string) string {
		sub := strictGenericSecretPattern.FindStringSubmatch(m)
		if !secretShapedValue(sub[3]) {
			return m
		}
		found = true
		return provenance.RedactionMarker
	})
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
// A line carrying a well-formed "// nosecret: <reason>" marker (see
// nosecretMarkerPattern) is never flagged, regardless of pattern matches.
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
	if err := validateRef(baseRef); err != nil {
		return "", err
	}
	if err := validateRef(headRef); err != nil {
		return "", err
	}
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
// gitDiff's --unified=0 invocation), tracking the current file (from
// "+++ b/<path>" lines) as it scans and, once inside a hunk, consuming
// exactly that hunk's body by the line counts in its "@@ -a,b +c,d @@"
// header rather than by re-inspecting each body line's own prefix.
//
// That positional consumption matters specifically because --unified=0
// emits no context lines, so a hunk body is made up entirely of "+"/"-"
// lines whose stripped content is never re-validated against "+++ "/"@@ "
// once the scanner is inside the hunk: an added line whose actual content
// begins with "++" renders, after the diff's own leading "+" marker, as
// "+++..." — which the old prefix-sniffing approach either misparsed as a
// bogus "+++ " file header (silently corrupting file attribution for every
// line after it) or, lacking a space, matched neither the file-header nor
// the added-line case and dropped entirely without advancing the line
// counter (misattributing every subsequent added line's reported number).
// Consuming the hunk body by its declared length sidesteps both failure
// modes: what a body line's content happens to look like is irrelevant to
// whether it's treated as part of the hunk.
//
// A line whose file is "/dev/null" (the "new" side of a pure deletion —
// --unified=0 never actually emits a "+" line under it, but this is
// guarded defensively) is skipped.
func addedLines(diff string) []addedLine {
	var out []addedLine
	lines := strings.Split(diff, "\n")
	file := ""

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeaderPattern.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			oldCount := 1
			if m[1] != "" {
				oldCount, _ = strconv.Atoi(m[1])
			}
			newStart, _ := strconv.Atoi(m[2])
			newCount := 1
			if m[3] != "" {
				newCount, _ = strconv.Atoi(m[3])
			}
			nextLine := newStart
			// Consume exactly oldCount+newCount following lines as this
			// hunk's body, by position, regardless of what they look like.
			bodyLen := oldCount + newCount
			for n := 0; n < bodyLen && i+1 < len(lines); n++ {
				i++
				body := lines[i]
				if strings.HasPrefix(body, "+") {
					if file != "" && file != "/dev/null" {
						out = append(out, addedLine{file: file, line: nextLine, text: body[1:]})
					}
					nextLine++
				}
				// "-" body lines are removed (old-side only); they never
				// occupy a new-side line number, so nextLine does not
				// advance for them.
			}
		}
	}
	return out
}

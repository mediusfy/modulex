package patchapply

import "regexp"

// secretPatterns is a best-effort, pattern-based set of regexes for
// secret-shaped strings. This list intentionally mirrors
// provenance.secretPatterns (see provenance/provenance.go) and
// contract.secretPatterns (see contract/secrets.go) for consistency across
// this repository's ADR-0032 family, but is declared independently here
// rather than shared: provenance and contract are separately versioned
// leaf packages, and neither exports these regexes, so copying a few lines
// locally is simpler and safer than adding an import-and-reach-into-
// unexported-state dependency just to avoid the duplication. Per this
// ticket's scope, provenance/provenance.go and contract/*.go are not
// modified.
//
// This is a safety net, not a guarantee: it catches common, recognizable
// secret shapes (cloud credential env-var assignments, PEM key blocks,
// well-known token prefixes, generic key/value assignments, and
// JWT-shaped strings), but it cannot catch every secret format, and it can
// both miss secrets (false negatives) and flag non-secrets (false
// positives). See docs/planning/agent-safety-policy.md's "Secrets and
// credentials" section and ADR-0032's requirement to "redact command
// output before it enters provenance artifacts" — this package applies
// that same requirement to its own diagnostic output (see previewContent
// in patchapply.go).
var secretPatterns = []*regexp.Regexp{
	// AWS secret-key-shaped env var assignments, e.g.
	// AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/...
	regexp.MustCompile(`(?i)\bAWS_SECRET[A-Z_]*\s*[:=]\s*['"]?[A-Za-z0-9/+=]{8,}['"]?`),
	// PEM private key blocks.
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	// GitHub token prefixes.
	regexp.MustCompile(`\b(ghp_|gho_|ghu_|ghs_|ghr_|github_pat_)[A-Za-z0-9_]{20,}\b`),
	// Generic key/token/password/secret assignments with a non-trivial
	// value (at least 6 non-whitespace/quote characters). Deliberately not
	// anchored to a word boundary on the left so it also catches compound
	// identifiers such as "api_key=" or "db_password:".
	regexp.MustCompile(`(?i)(key|token|password|secret)\s*[:=]\s*['"]?[^\s'",]{6,}['"]?`),
	// JWT-shaped strings: three dot-separated base64url segments, the
	// first starting with the common "eyJ" header prefix.
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
}

// redactionMarker replaces any matched secret-shaped value.
const redactionMarker = "[REDACTED]"

// maxPreviewBytes bounds how much of a file's content is ever echoed back
// in an error message or Journal-related diagnostic, after redaction.
const maxPreviewBytes = 200

// redact replaces every secret-shaped match in s with redactionMarker.
func redact(s string) string {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, redactionMarker)
		}
	}
	return s
}

// previewContent returns a short, human-readable preview of content, safe
// to include in an error message or journal-summary string: content is
// converted to a string, scanned and redacted for secret-shaped values,
// and ONLY THEN truncated to maxPreviewBytes.
//
// Redaction must happen before truncation, not after: truncating first
// could cut a matchable secret shape in half (e.g. a GitHub token whose
// required-length suffix falls past the truncation point), producing a
// remnant that no longer matches any secretPatterns entry and so would
// slip through unredacted. Running redaction over the complete, untruncated
// content first closes that gap — every full-length secret-shaped match
// is found and replaced before any content is discarded for length.
//
// This is best-effort, matching secretPatterns' own limits: it can miss
// secret shapes it does not recognize.
func previewContent(content []byte) string {
	if content == nil {
		return "<absent>"
	}
	if len(content) == 0 {
		return "<empty>"
	}
	s := redact(string(content))
	if len(s) > maxPreviewBytes {
		s = s[:maxPreviewBytes] + "...(truncated)"
	}
	return s
}

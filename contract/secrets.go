package contract

import (
	"fmt"
	"regexp"
)

// secretPattern pairs a regexp for one recognizable secret shape with a
// short, human-readable label used in Validate's error messages (e.g.
// "GitHub token").
type secretPattern struct {
	label string
	re    *regexp.Regexp
}

// secretPatterns is a best-effort, pattern-based set of regexes for
// secret-shaped strings. This list intentionally mirrors
// provenance.secretPatterns (see provenance/provenance.go) for consistency
// between the two schemas, but is declared independently here rather than
// shared: provenance and contract are separately versioned leaf packages,
// and a regexp list a few lines long is not worth an import or a shared
// internal package just to avoid the duplication.
//
// This is a safety net, not a guarantee: it catches common, recognizable
// secret shapes (cloud credential env-var assignments, PEM key blocks,
// well-known token prefixes, generic key/value assignments, and
// JWT-shaped strings), but it cannot catch every secret format, and it can
// both miss secrets (false negatives) and flag non-secrets (false
// positives). The only real prevention is not putting secrets into a
// checked-in modulex.agent.yaml in the first place — see
// docs/planning/agent-safety-policy.md and ADR-0032's requirement to
// "never expose secret values in prompts, reports, or persisted
// artifacts."
var secretPatterns = []secretPattern{
	{
		label: "AWS secret key",
		re:    regexp.MustCompile(`(?i)\bAWS_SECRET[A-Z_]*\s*[:=]\s*['"]?[A-Za-z0-9/+=]{8,}['"]?`),
	},
	{
		label: "PEM private key",
		re:    regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	},
	{
		label: "GitHub token",
		re:    regexp.MustCompile(`\b(ghp_|gho_|ghu_|ghs_|ghr_|github_pat_)[A-Za-z0-9_]{20,}\b`),
	},
	{
		// Generic key/token/password/secret assignments with a
		// non-trivial value (at least 6 non-whitespace/quote
		// characters). Deliberately not anchored to a word boundary on
		// the left so it also catches compound identifiers such as
		// "api_key=" or "db_password:".
		label: "generic key/token/password/secret assignment",
		re:    regexp.MustCompile(`(?i)(key|token|password|secret)\s*[:=]\s*['"]?[^\s'",]{6,}['"]?`),
	},
	{
		// Three dot-separated base64url segments, the first starting
		// with the common "eyJ" header prefix.
		label: "JWT-shaped value",
		re:    regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	},
}

// secretLabel reports whether s matches any known secret-shaped pattern,
// returning the matching pattern's label if so.
func secretLabel(s string) (string, bool) {
	for _, p := range secretPatterns {
		if p.re.MatchString(s) {
			return p.label, true
		}
	}
	return "", false
}

// fieldValue pairs a dotted/indexed field path (for error messages, e.g.
// "projects[1].description") with the string value found there.
type fieldValue struct {
	path  string
	value string
}

// fieldsForSecretScan enumerates every free-text string field in c, paired
// with a field path identifying where it came from, for validateSecrets to
// scan. Empty values are omitted since they can never match a secret
// pattern.
func (c *Contract) fieldsForSecretScan() []fieldValue {
	var fields []fieldValue
	add := func(path, value string) {
		if value != "" {
			fields = append(fields, fieldValue{path: path, value: value})
		}
	}

	for i, p := range c.Projects {
		add(fmt.Sprintf("projects[%d].name", i), p.Name)
		add(fmt.Sprintf("projects[%d].path", i), p.Path)
		add(fmt.Sprintf("projects[%d].module_path", i), p.ModulePath)
		add(fmt.Sprintf("projects[%d].description", i), p.Description)
		for j, root := range p.CompositionRoots {
			add(fmt.Sprintf("projects[%d].composition_roots[%d]", i, j), root)
		}
	}

	add("instructions.rule", c.Instructions.Rule)
	for i, f := range c.Instructions.Files {
		add(fmt.Sprintf("instructions.files[%d].path", i), f.Path)
		add(fmt.Sprintf("instructions.files[%d].notes", i), f.Notes)
	}

	for i, b := range c.Boundaries {
		add(fmt.Sprintf("boundaries[%d].name", i), b.Name)
		add(fmt.Sprintf("boundaries[%d].description", i), b.Description)
		add(fmt.Sprintf("boundaries[%d].rule", i), b.Rule)
		for j, path := range b.Paths {
			add(fmt.Sprintf("boundaries[%d].paths[%d]", i, j), path)
		}
	}

	for i, cmd := range c.Commands {
		add(fmt.Sprintf("commands[%d].name", i), cmd.Name)
		add(fmt.Sprintf("commands[%d].command", i), cmd.Command)
		add(fmt.Sprintf("commands[%d].reason", i), cmd.Reason)
	}

	for i, chk := range c.Verification.Focused {
		add(fmt.Sprintf("verification.focused[%d].name", i), chk.Name)
		add(fmt.Sprintf("verification.focused[%d].command", i), chk.Command)
		add(fmt.Sprintf("verification.focused[%d].reason", i), chk.Reason)
		add(fmt.Sprintf("verification.focused[%d].required_tool", i), chk.RequiredTool)
	}
	for i, chk := range c.Verification.Full {
		add(fmt.Sprintf("verification.full[%d].name", i), chk.Name)
		add(fmt.Sprintf("verification.full[%d].command", i), chk.Command)
		add(fmt.Sprintf("verification.full[%d].reason", i), chk.Reason)
		add(fmt.Sprintf("verification.full[%d].required_tool", i), chk.RequiredTool)
	}

	for i, p := range c.ProtectedPaths {
		add(fmt.Sprintf("protected_paths[%d]", i), p)
	}
	for i, p := range c.GeneratedPaths {
		add(fmt.Sprintf("generated_paths[%d]", i), p)
	}
	for i, t := range c.RequiredTools {
		add(fmt.Sprintf("required_tools[%d]", i), t)
	}
	for i, cred := range c.RequiredCredentials {
		add(fmt.Sprintf("required_credentials[%d]", i), cred)
	}
	for i, s := range c.OptionalServices {
		add(fmt.Sprintf("optional_services[%d].name", i), s.Name)
		add(fmt.Sprintf("optional_services[%d].description", i), s.Description)
	}

	add("handoff_format", c.HandoffFormat)

	return fields
}

// validateSecrets scans every free-text field in c (see
// fieldsForSecretScan) and returns one error per field that looks like it
// contains a live secret value, naming the field path and the matched
// pattern's label.
func (c *Contract) validateSecrets() []error {
	var errs []error
	for _, f := range c.fieldsForSecretScan() {
		if label, ok := secretLabel(f.value); ok {
			errs = append(errs, fmt.Errorf("%s: contains a value that looks like a secret (%s)", f.path, label))
		}
	}
	return errs
}

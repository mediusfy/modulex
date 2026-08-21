package discovery

import (
	"regexp"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// ClassificationRule maps a command pattern to a provenance.CommandClass
// and a human-readable reason. Pattern is matched against the trimmed
// command string (e.g. "make release VERSION=v0.2.0", "git push origin
// main"); Reason should be specific enough that a human reviewing an agent
// transcript understands why the command was classified the way it was
// without re-deriving it themselves.
type ClassificationRule struct {
	Pattern *regexp.Regexp
	Class   provenance.CommandClass
	Reason  string
}

// ClassificationRules is the ordered, first-match-wins rule table
// ClassifyCommand consults, built from
// docs/planning/agent-safety-policy.md's command-classification table for
// ADR-0032 P0 (Jira MOD-64). It reuses provenance.CommandClass rather than
// a parallel enum, per that package's whole reason for existing as a
// separate, composable leaf package.
//
// Rules are grouped by command family and ordered from narrower/more
// consequential to broader/safer within each family; add new rules where
// they logically belong rather than only appending, so an unusual future
// pattern overlap stays easy to spot by reading the table top to bottom.
var ClassificationRules = []ClassificationRule{
	// --- git: safe / read-only ---
	{
		Pattern: regexp.MustCompile(`^git (status|diff|log)\b`),
		Class:   provenance.ClassSafe,
		Reason:  "read-only git inspection command; always allowed per agent-safety-policy.md",
	},

	// --- git: destructive / history-rewriting ---
	{
		Pattern: regexp.MustCompile(`^git (reset --hard|clean -f|branch -D)\b`),
		Class:   provenance.ClassDestructive,
		Reason:  "discards uncommitted work or deletes a branch irreversibly; requires explicit human approval before running per agent-safety-policy.md",
	},

	// --- git: external mutation, approval-required ---
	{
		Pattern: regexp.MustCompile(`^git (push|tag)\b`),
		Class:   provenance.ClassApprovalRequired,
		Reason:  "pushes to a remote or creates a tag; external mutation requires explicit, current-session human approval per agent-safety-policy.md",
	},

	// --- git: local mutating ---
	{
		Pattern: regexp.MustCompile(`^git (add|commit)\b`),
		Class:   provenance.ClassMutating,
		Reason:  "writes to the local git index/history on the current branch; allowed on a feature branch, never directly on main",
	},

	// --- go: safe / read-only ---
	{
		// The optional `-C <dir>` accepts the same restricted character set
		// verify's isPathSafeForCommand allows in a path it interpolates
		// into a command, so a directory argument can't smuggle in further
		// shell syntax or flags past this classification.
		Pattern: regexp.MustCompile(`^go (-C [A-Za-z0-9_./-]+ )?(build|vet|test)\b`),
		Class:   provenance.ClassSafe,
		Reason:  "compiles or statically checks code with no side effects; always allowed per agent-safety-policy.md",
	},

	// --- go: networked ---
	{
		Pattern: regexp.MustCompile(`^go mod download\b`),
		Class:   provenance.ClassNetworked,
		Reason:  "fetches module content from the configured module proxy",
	},

	// --- make targets: local mutating ---
	{
		Pattern: regexp.MustCompile(`^make fmt\b`),
		Class:   provenance.ClassMutating,
		Reason:  "rewrites source files in place (gofmt -s -w .)",
	},

	// --- make targets: networked ---
	{
		Pattern: regexp.MustCompile(`^make deps\b`),
		Class:   provenance.ClassNetworked,
		Reason:  "downloads Go module dependencies (go mod download)",
	},
	{
		Pattern: regexp.MustCompile(`^make vuln\b`),
		Class:   provenance.ClassNetworked,
		Reason:  "fetches the govulncheck vulnerability database over the network",
	},

	// --- make targets: approval-required ---
	{
		Pattern: regexp.MustCompile(`^make release\b`),
		Class:   provenance.ClassApprovalRequired,
		Reason:  "tags and pushes a release (git tag + git push origin); tagging/publishing a release always requires explicit human approval per agent-safety-policy.md, regardless of any standing autonomy instruction",
	},
	// make publish-godev is genuinely both ClassNetworked (it curls
	// proxy.golang.org to trigger indexing) and externally visible: asking
	// the public module proxy/pkg.go.dev to (re-)index a version is itself
	// a publishing action with an outward effect, not a private read.
	//
	// Tie-breaking rule: when a command is both ClassNetworked and would
	// trigger an externally-visible or approval-gated action,
	// ClassApprovalRequired wins. Reason: collapsing an ambiguous command
	// to the weaker "networked" classification would let it run
	// unattended on the theory that mere network I/O is always fine, which
	// defeats the purpose of having an approval-required class at all — an
	// approval gate must never be satisfiable just because the command
	// also happens to match a broader, cheaper class. Any future rule that
	// is genuinely both networked and approval-required should follow this
	// same precedence.
	{
		Pattern: regexp.MustCompile(`^make publish-godev\b`),
		Class:   provenance.ClassApprovalRequired,
		Reason:  "networked AND externally visible (asks proxy.golang.org/pkg.go.dev to index a release); approval_required takes precedence over networked per this rule table's documented tie-break",
	},

	// --- make targets: safe (read-only builds/tests/verification) ---
	{
		Pattern: regexp.MustCompile(`^make (build|test|test-arch|lint|help|check-[a-z-]+)\b`),
		Class:   provenance.ClassSafe,
		Reason:  "runs a read-only build, test, or verification target (wraps go build/test/vet, golangci-lint run, or a read-only check script) with no network or mutating side effects",
	},
}

// ClassifyCommand classifies cmd (a full command line, e.g. "make release
// VERSION=v0.2.0" or "git push origin main") by matching it against
// ClassificationRules in order and returning the first match's class and
// reason.
//
// Fail-safe default: if no rule matches, ClassifyCommand returns
// provenance.ClassApprovalRequired rather than defaulting to safe. An
// unrecognized command must never silently pass through as though it had
// been vetted; per agent-safety-policy.md, "when in doubt about whether an
// action is safe, an agent must treat it as requiring approval rather than
// assuming permission."
func ClassifyCommand(cmd string) (provenance.CommandClass, string) {
	trimmed := strings.TrimSpace(cmd)
	for _, rule := range ClassificationRules {
		if rule.Pattern.MatchString(trimmed) {
			return rule.Class, rule.Reason
		}
	}
	return provenance.ClassApprovalRequired, "no classification rule matched this command; fail-safe default treats any unrecognized command as approval-required rather than assuming it is safe"
}

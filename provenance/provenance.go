// Package provenance defines a versioned, JSON-marshalable schema for
// recording what an AI coding agent did to a repository and why, per
// ADR-0032 ("Agent-First Development Experience"), P1: "Add provenance and
// handoff JSON".
//
// This package is a standalone data schema. It intentionally does not import
// github.com/mediusfy/modulex or any other package in this repository: a
// future `modulex agent handoff` CLI (out of scope for this package), a CI
// step, or any other tool can depend on provenance alone to produce or
// consume an Envelope, without pulling in the runtime library.
//
// # Building a handoff artifact
//
//	env := provenance.Envelope{
//	    SchemaVersion: provenance.SchemaVersion,
//	    Repository: provenance.RepoState{
//	        Path:   "/repo",
//	        Branch: "MOD-66-provenance-handoff-json",
//	        Commit: "abc1234",
//	        Dirty:  false,
//	    },
//	    Agent: provenance.AgentInfo{
//	        Name: "claude",
//	        Tool: "claude-code",
//	    },
//	    CreatedAt: time.Now().UTC(),
//	}
//	env.Redact()
//	if err := env.Validate(); err != nil {
//	    return err
//	}
//	b, err := json.Marshal(env)
//
// See docs/planning/provenance-handoff-schema.md for the full guide,
// including the redaction and validation guarantees and their limits.
package provenance

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the current version of the Envelope schema, using a
// plain semver string (not a Go module path or API version) because the
// schema is consumed as data, not as a Go API: it may be read by non-Go
// tooling (a future CLI, CI steps, MCP clients) that has no notion of Go
// module compatibility rules. Bump the minor version for backward-compatible
// additions (new optional fields) and the major version for breaking changes
// (renamed/removed/retyped fields), and document the change in
// CHANGELOG.md whenever Envelope's shape changes.
const SchemaVersion = "1.0.0"

// redactionMarker replaces any matched secret-shaped value.
const redactionMarker = "[REDACTED]"

// Status is the outcome of a command or verification step. Modeling this as
// an explicit enum rather than a bool is required so that "did not run" and
// "ran and passed" are never confused: a missing tool, an unmet approval
// gate, and an intentional skip are all distinct from both success and
// failure.
type Status string

const (
	// StatusPass means the step ran and succeeded.
	StatusPass Status = "pass"
	// StatusFail means the step ran and failed.
	StatusFail Status = "fail"
	// StatusSkipped means the step was intentionally not run (e.g. out of
	// scope for this change). Requires a non-empty Reason.
	StatusSkipped Status = "skipped"
	// StatusUnavailable means the step could not be run (e.g. a required
	// tool or service was missing). Requires a non-empty Reason.
	StatusUnavailable Status = "unavailable"
	// StatusApprovalRequired means the step is gated behind human approval
	// and has not (yet) been approved.
	StatusApprovalRequired Status = "approval_required"
)

// CommandClass classifies a command by the kind of impact it can have, per
// ADR-0032's requirement to "classify commands by filesystem, network,
// destructive, and approval impact."
type CommandClass string

const (
	// ClassSafe commands are read-only and side-effect-free.
	ClassSafe CommandClass = "safe"
	// ClassMutating commands write to the local filesystem/repository.
	ClassMutating CommandClass = "mutating"
	// ClassNetworked commands perform network I/O.
	ClassNetworked CommandClass = "networked"
	// ClassDestructive commands can delete or irreversibly overwrite data.
	ClassDestructive CommandClass = "destructive"
	// ClassApprovalRequired commands require explicit human approval before
	// running (e.g. push, release, infrastructure changes).
	ClassApprovalRequired CommandClass = "approval_required"
)

// ChangeType describes how a file was affected.
type ChangeType string

const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// VerificationCategory groups a VerificationResult by the kind of check it
// represents, per ADR-0032's "focused and full verification results" and
// "boundary, compatibility, security, and secret-scan results." Modeling
// these as a Category field on one VerificationResult type (rather than
// separate top-level slices per category) keeps the schema flat and lets new
// categories be added without a breaking schema change.
type VerificationCategory string

const (
	VerificationFocused       VerificationCategory = "focused"
	VerificationFull          VerificationCategory = "full"
	VerificationBoundary      VerificationCategory = "boundary"
	VerificationCompatibility VerificationCategory = "compatibility"
	VerificationSecurity      VerificationCategory = "security"
	VerificationSecretScan    VerificationCategory = "secret_scan"
	VerificationChangelog     VerificationCategory = "changelog"
)

// RepoState records the repository path, branch, commit, and dirty-worktree
// state the envelope was produced from.
type RepoState struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit"`
	// Dirty is true if the worktree had uncommitted changes (staged or
	// unstaged) at the time this envelope was produced.
	Dirty bool `json:"dirty"`
}

// AgentInfo records which agent, tool, and CLI produced the envelope.
type AgentInfo struct {
	// Name identifies the agent (e.g. "claude", "codex", "kimi").
	Name string `json:"name"`
	// Version is the agent/model version, if known.
	Version string `json:"version,omitempty"`
	// Tool identifies the host tool or SDK (e.g. "claude-code").
	Tool string `json:"tool,omitempty"`
	// ToolVersion is the host tool's version, if known.
	ToolVersion string `json:"tool_version,omitempty"`
	// CLIVersion is the version of the modulex agent CLI (or equivalent)
	// that generated this envelope, if any.
	CLIVersion string `json:"cli_version,omitempty"`
}

// FileChange records one changed file and, when available, a content hash
// of the resulting artifact (e.g. "sha256:<hex>") so a reviewer or CI system
// can verify the handoff matches the actual diff.
type FileChange struct {
	Path string `json:"path"`
	// OldPath is set for ChangeRenamed to record the file's previous path.
	OldPath string     `json:"old_path,omitempty"`
	Type    ChangeType `json:"type"`
	// Hash is a content hash of the file after the change, in
	// "<algorithm>:<hex>" form (e.g. "sha256:abc123..."). Empty if not
	// computed (e.g. for a deleted file).
	Hash string `json:"hash,omitempty"`
}

// CommandResult records one command the agent ran: its classification,
// outcome, exit code, duration, and any environment requirements. Output is
// free text and MUST be passed through Envelope.Redact (or independently
// scrubbed) before persisting or transmitting the envelope; see the package
// doc and docs/planning/provenance-handoff-schema.md.
type CommandResult struct {
	// Name is the command or Make target invoked (e.g. "make test").
	Name           string       `json:"name"`
	Args           []string     `json:"args,omitempty"`
	Classification CommandClass `json:"classification"`
	Status         Status       `json:"status"`
	// ExitCode is the process exit code. It is meaningless (and should be
	// ignored by consumers) when Status is StatusSkipped,
	// StatusUnavailable, or StatusApprovalRequired, so it is a pointer:
	// nil means "no exit code", distinct from an explicit 0 (success).
	ExitCode *int `json:"exit_code,omitempty"`
	// Duration is wall-clock time the command took to run, encoded as a
	// nanosecond count (time.Duration's default JSON encoding). Zero for
	// commands that never ran.
	Duration time.Duration `json:"duration_ns,omitempty"`
	// EnvironmentNeeds lists tools, services, or credentials the command
	// required (e.g. "docker", "GITHUB_TOKEN"), without ever including
	// their values.
	EnvironmentNeeds []string `json:"environment_needs,omitempty"`
	// Output is a free-text capture of command output. Redact before
	// persisting; see Envelope.Redact.
	Output string `json:"output,omitempty"`
	// Reason explains a StatusSkipped/StatusUnavailable/
	// StatusApprovalRequired status. Required (non-empty) in those cases;
	// see Envelope.Validate.
	Reason string `json:"reason,omitempty"`
}

// VerificationResult records the outcome of one verification step (a
// focused check, a full repository gate, a boundary/compatibility/security/
// secret-scan pass, etc.), distinguished by Category.
type VerificationResult struct {
	Name     string               `json:"name"`
	Category VerificationCategory `json:"category"`
	Status   Status               `json:"status"`
	Duration time.Duration        `json:"duration_ns,omitempty"`
	// Message is free-text detail (e.g. a failure summary). Redact before
	// persisting; see Envelope.Redact.
	Message string `json:"message,omitempty"`
	// Reason explains a StatusSkipped/StatusUnavailable/
	// StatusApprovalRequired status. Required (non-empty) in those cases;
	// see Envelope.Validate.
	Reason string `json:"reason,omitempty"`
}

// Approval records a human approval granted for an elevated action (push,
// release, deletion, infrastructure change, etc.), per ADR-0032's
// human-approval boundary.
type Approval struct {
	// Action names what was approved (e.g. "push", "release").
	Action     string    `json:"action"`
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
	// Notes is free text. Redact before persisting; see Envelope.Redact.
	Notes string `json:"notes,omitempty"`
}

// RollbackStatus records whether the change can be rolled back and whether
// a rollback has been applied.
type RollbackStatus struct {
	// Available is true if a rollback path (e.g. a git revert, a retained
	// patch) exists for this change.
	Available bool `json:"available"`
	// Applied is true if a rollback has actually been performed.
	Applied bool `json:"applied"`
	// Method describes the rollback mechanism (e.g. "git revert <sha>").
	Method string `json:"method,omitempty"`
	// Notes is free text. Redact before persisting; see Envelope.Redact.
	Notes string `json:"notes,omitempty"`
}

// Envelope is the top-level, versioned provenance/handoff record for one
// unit of agent work. It is designed to be marshaled to JSON and attached to
// a PR description, a CI artifact, an MCP response, or an audit log.
//
// json.Marshal(envelope) produces a deterministic field order because every
// field is either a scalar or an ordered slice — there are no map types in
// this schema, so there is nothing that needs sorting before marshaling
// (contrast with the core module's Manager.Diagnostics/ModuleContract, which
// do sort map-derived slices for the same determinism goal; this package
// does not import or reference that code).
type Envelope struct {
	SchemaVersion string    `json:"schema_version"`
	Repository    RepoState `json:"repository"`
	Agent         AgentInfo `json:"agent"`
	// Changes lists every file the agent touched, in the order they were
	// changed.
	Changes []FileChange `json:"changes,omitempty"`
	// Commands lists every command the agent ran, in execution order.
	Commands []CommandResult `json:"commands,omitempty"`
	// Verification lists every verification step (focused, full, boundary,
	// compatibility, security, secret-scan, changelog, ...), in the order
	// they were run.
	Verification []VerificationResult `json:"verification,omitempty"`
	// Approvals lists human approvals granted for elevated actions, if any.
	Approvals []Approval `json:"approvals,omitempty"`
	// Rollback describes rollback availability/status for this change. Nil
	// means rollback was not assessed (distinct from an assessed-but-
	// unavailable RollbackStatus{Available: false}).
	Rollback *RollbackStatus `json:"rollback,omitempty"`
	// CreatedAt is when this envelope was produced.
	CreatedAt time.Time `json:"created_at"`
}

// secretPatterns is a best-effort, pattern-based set of regexes for
// secret-shaped strings. This is a safety net, not a guarantee: it catches
// common, recognizable secret shapes (cloud credential env-var names, PEM
// key blocks, well-known token prefixes, generic key/value assignments, and
// JWT-shaped strings), but it cannot catch every secret format, and it can
// both miss secrets (false negatives) and flag non-secrets (false
// positives). The only real prevention is not putting secrets into these
// fields in the first place — see docs/planning/agent-safety-policy.md,
// the human-facing policy this schema operationalizes, and ADR-0032's
// requirement to "redact command output before it enters provenance
// artifacts."
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

// containsSecret reports whether s matches any known secret-shaped pattern.
func containsSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// redactString replaces every secret-shaped match in s with redactionMarker,
// reporting whether any replacement was made.
func redactString(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	changed := false
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			changed = true
			s = re.ReplaceAllString(s, redactionMarker)
		}
	}
	return s, changed
}

// RedactSecrets replaces every secret-shaped match in s with the redaction
// marker ("[REDACTED]"), reporting whether any replacement was made. It is
// the exported form of the same best-effort, pattern-based detection Redact
// uses internally (see secretPatterns' doc comment for what it does and does
// not catch), exposed so other packages needing secret-shaped-value
// detection (e.g. review's diff secret scan, Jira MOD-65) reuse this one
// pattern set instead of maintaining a second, divergent copy.
func RedactSecrets(s string) (string, bool) {
	return redactString(s)
}

// Redact scrubs secret-shaped values from every free-text field in the
// envelope (command args/output/reason, verification message/reason,
// approval notes, rollback notes), replacing matches in place with
// redactionMarker ("[REDACTED]").
//
// This is a best-effort pattern-based safety net (see secretPatterns' doc
// comment), not a guarantee that no secret can leak through. Callers should
// still avoid putting secrets into these fields in the first place, per
// docs/planning/agent-safety-policy.md.
func (e *Envelope) Redact() {
	for i := range e.Commands {
		e.Commands[i].Output, _ = redactString(e.Commands[i].Output)
		e.Commands[i].Reason, _ = redactString(e.Commands[i].Reason)
		for j, arg := range e.Commands[i].Args {
			e.Commands[i].Args[j], _ = redactString(arg)
		}
	}
	for i := range e.Verification {
		e.Verification[i].Message, _ = redactString(e.Verification[i].Message)
		e.Verification[i].Reason, _ = redactString(e.Verification[i].Reason)
	}
	for i := range e.Approvals {
		e.Approvals[i].Notes, _ = redactString(e.Approvals[i].Notes)
	}
	if e.Rollback != nil {
		e.Rollback.Notes, _ = redactString(e.Rollback.Notes)
	}
}

// Validate checks that the envelope is structurally well-formed and free of
// unredacted secret-shaped values, returning a single error (via
// errors.Join) naming every problem found, or nil if the envelope is valid.
//
// Structural checks:
//   - SchemaVersion, Repository.Path, Repository.Commit, and CreatedAt are
//     required (non-empty/non-zero).
//   - Every CommandResult and VerificationResult with Status ==
//     StatusSkipped or StatusUnavailable must carry a non-empty Reason.
//
// Secret checks: every free-text field (command args/output/reason,
// verification message/reason, approval notes, rollback notes) is scanned
// with the same best-effort detection Redact uses. A caller should call
// Redact before Validate; Validate exists as a backstop so an envelope with
// a live secret in it cannot be marshaled/persisted without an explicit
// error, even if Redact was skipped. See Redact's doc comment for the
// limits of this detection.
func (e *Envelope) Validate() error {
	var errs []error

	if e.SchemaVersion == "" {
		errs = append(errs, errors.New("schema_version is required"))
	}
	if e.Repository.Path == "" {
		errs = append(errs, errors.New("repository.path is required"))
	}
	if e.Repository.Commit == "" {
		errs = append(errs, errors.New("repository.commit is required"))
	}
	if e.CreatedAt.IsZero() {
		errs = append(errs, errors.New("created_at is required"))
	}

	for i, c := range e.Commands {
		errs = append(errs, validateCommand(i, c)...)
	}
	for i, v := range e.Verification {
		errs = append(errs, validateVerification(i, v)...)
	}
	for i, a := range e.Approvals {
		if containsSecret(a.Notes) {
			errs = append(errs, fmt.Errorf("approvals[%d] (%s): notes contains an unredacted secret-shaped value", i, a.Action))
		}
	}
	if e.Rollback != nil && containsSecret(e.Rollback.Notes) {
		errs = append(errs, errors.New("rollback: notes contains an unredacted secret-shaped value"))
	}

	return errors.Join(errs...)
}

// requiresReason reports whether status requires a non-empty Reason field.
func requiresReason(status Status) bool {
	return status == StatusSkipped || status == StatusUnavailable
}

func validateCommand(i int, c CommandResult) []error {
	var errs []error
	if requiresReason(c.Status) && strings.TrimSpace(c.Reason) == "" {
		errs = append(errs, fmt.Errorf("commands[%d] (%s): reason is required when status is %q", i, c.Name, c.Status))
	}
	if containsSecret(c.Output) {
		errs = append(errs, fmt.Errorf("commands[%d] (%s): output contains an unredacted secret-shaped value", i, c.Name))
	}
	if containsSecret(c.Reason) {
		errs = append(errs, fmt.Errorf("commands[%d] (%s): reason contains an unredacted secret-shaped value", i, c.Name))
	}
	for _, arg := range c.Args {
		if containsSecret(arg) {
			errs = append(errs, fmt.Errorf("commands[%d] (%s): args contains an unredacted secret-shaped value", i, c.Name))
			break
		}
	}
	return errs
}

func validateVerification(i int, v VerificationResult) []error {
	var errs []error
	if requiresReason(v.Status) && strings.TrimSpace(v.Reason) == "" {
		errs = append(errs, fmt.Errorf("verification[%d] (%s): reason is required when status is %q", i, v.Name, v.Status))
	}
	if containsSecret(v.Message) {
		errs = append(errs, fmt.Errorf("verification[%d] (%s): message contains an unredacted secret-shaped value", i, v.Name))
	}
	if containsSecret(v.Reason) {
		errs = append(errs, fmt.Errorf("verification[%d] (%s): reason contains an unredacted secret-shaped value", i, v.Name))
	}
	return errs
}

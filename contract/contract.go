// Package contract defines a versioned, YAML-marshalable schema for a
// repository's agent contract (`modulex.agent.yaml`), per ADR-0032
// ("Agent-First Development Experience"), P0: "Define and validate the
// Modulex agent repository contract" (Jira MOD-62). The ADR's "Canonical
// repository contract" section describes what such a contract should
// declare:
//
//   - projects, Go modules, composition roots, and relevant source paths;
//   - applicable instruction files and precedence rules;
//   - lifecycle and module boundaries;
//   - safe, mutating, networked, destructive, and approval-required
//     commands;
//   - focused and repository-wide verification commands;
//   - generated and protected paths;
//   - required tools and optional services;
//   - secret and credential requirements without storing secret values;
//   - expected artifacts, reports, and handoff format.
//
// This package is a standalone leaf package, like provenance, discovery, and
// verify: it does not import the core modulex package, discovery, or
// verify. It depends on provenance only for provenance.CommandClass, so
// command classification stays consistent with the provenance/handoff
// schema and discovery's command classifier rather than inventing a
// parallel enum.
//
// # A declaration, not a derivation
//
// Contract is a human/agent-authored declaration of what should be true
// about a repository, checked in as `modulex.agent.yaml`. This is
// deliberately different from discovery.Discover, which derives similar
// facts (modules, composition roots, instruction files) by scanning the
// filesystem at runtime. Nothing in this package reads the filesystem or
// depends on discovery; a Contract is only ever built by unmarshaling YAML
// or constructing one in code, and validated as data.
//
// # Command strings are data, not sanitized shell input
//
// CommandDecl.Command and CheckDecl.Command store shell command lines as
// plain strings (e.g. "make test"). This package never executes them. A
// consumer that does execute a declared command (a future `modulex agent`
// CLI, or any other tool) must apply the same allow-list discipline
// verify.isPathSafeForCommand uses before interpolating any untrusted value
// (such as a changed-file path) into one of these strings and passing it to
// a shell — this package makes no guarantee that a Command value is safe to
// shell-interpolate with untrusted data appended to it.
//
// # Validation guarantees and their limits
//
// Validate checks required fields, rejects any CommandDecl whose Class is
// not one of provenance's five known CommandClass values, and scans every
// free-text field for secret-shaped values using a pattern list that
// mirrors provenance's (see secrets.go). Unlike provenance.Envelope, this
// package never redacts: a contract is a file a human reads and edits, so a
// value that looks like a live credential fails validation outright rather
// than being silently rewritten to "[REDACTED]". See secrets.go's doc
// comment for why this is a best-effort safety net, not a guarantee.
//
// See docs/planning/agent-repository-contract-guide.md for the full guide,
// including the schema versioning policy and a worked example.
package contract

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mediusfy/modulex/provenance"
)

// SchemaVersion is the current version of the Contract schema, using a
// plain semver string (not a Go module path or API version) for the same
// reason provenance.SchemaVersion does: the schema is consumed as data,
// potentially by non-Go tooling (a future CLI, editor integrations, CI
// steps) with no notion of Go module compatibility rules. Bump the minor
// version for backward-compatible additions (new optional fields), the
// major version for breaking changes (renamed, removed, or retyped
// fields), and document every schema change in CHANGELOG.md.
const SchemaVersion = "1.0.0"

// Project describes one project this contract covers: a Go module (or a
// logical grouping of one), its location, and the composition roots
// (runnable entry points) that wire it together, per ADR-0032's "projects,
// Go modules, composition roots, and relevant source paths."
type Project struct {
	// Name is a short, stable identifier for this project (e.g. "modulex").
	Name string `yaml:"name"`
	// Path is the project's root directory relative to the repository
	// root ("." for the repository root itself).
	Path string `yaml:"path"`
	// ModulePath is the Go module path declared in this project's go.mod
	// (e.g. "github.com/mediusfy/modulex"), if it is a Go module.
	ModulePath string `yaml:"module_path,omitempty"`
	// Description is a short, human-readable summary of this project's
	// purpose.
	Description string `yaml:"description,omitempty"`
	// CompositionRoots lists directories (relative to the repository
	// root) that wire this project's pieces together into a runnable
	// program (e.g. "examples/bootstrap").
	CompositionRoots []string `yaml:"composition_roots,omitempty"`
}

// InstructionFile describes one agent-instruction file (e.g. AGENTS.md)
// and its precedence relative to any other declared instruction file.
type InstructionFile struct {
	// Path is the instruction file's location relative to the repository
	// root (e.g. "AGENTS.md").
	Path string `yaml:"path"`
	// Priority orders this file relative to other declared instruction
	// files: lower values take precedence. Ties should be resolved by the
	// Rule field below.
	Priority int `yaml:"priority"`
	// Notes is a short, human-readable description of what this file
	// covers or why it exists.
	Notes string `yaml:"notes,omitempty"`
}

// InstructionPrecedence declares which instruction files apply to this
// repository and how conflicts between them should be resolved, per
// ADR-0032's "applicable instruction files and precedence rules."
type InstructionPrecedence struct {
	// Files lists every applicable instruction file, in no particular
	// slice order — Priority (and, for ties, Rule) determines precedence.
	Files []InstructionFile `yaml:"files,omitempty"`
	// Rule is a short, human-readable statement of the precedence rule
	// (e.g. which document wins when two disagree).
	Rule string `yaml:"rule,omitempty"`
}

// Boundary describes one lifecycle or module boundary this repository
// enforces (e.g. "the core package must not import an adapter package"),
// per ADR-0032's "lifecycle and module boundaries."
type Boundary struct {
	// Name is a short, stable identifier for this boundary.
	Name string `yaml:"name"`
	// Description explains what the boundary protects and why.
	Description string `yaml:"description,omitempty"`
	// Paths lists the repository paths this boundary applies to.
	Paths []string `yaml:"paths,omitempty"`
	// Rule is a short, human-readable statement of how the boundary is
	// enforced (e.g. a script or CI check name).
	Rule string `yaml:"rule,omitempty"`
}

// CommandDecl declares one command this repository's agents may run,
// classified by impact, per ADR-0032's "safe, mutating, networked,
// destructive, and approval-required commands." Class reuses
// provenance.CommandClass rather than a parallel enum, so command
// classification stays consistent with the provenance/handoff schema and
// discovery.ClassifyCommand's rule table.
//
// Command is stored as data. See the package doc comment's "Command
// strings are data, not sanitized shell input" section before any consumer
// executes it or interpolates untrusted input into it.
type CommandDecl struct {
	// Name is a short, stable identifier for this command (e.g.
	// "go-test").
	Name string `yaml:"name"`
	// Command is the shell command line a consumer would run (e.g.
	// "go test ./...").
	Command string `yaml:"command"`
	// Class classifies this command's impact. Validate rejects any value
	// that is not one of provenance.ClassSafe, ClassMutating,
	// ClassNetworked, ClassDestructive, or ClassApprovalRequired.
	Class provenance.CommandClass `yaml:"class"`
	// Reason explains why this command was classified the way it was.
	Reason string `yaml:"reason,omitempty"`
}

// CheckDecl declares one verification check, shaped so it could plausibly
// be converted to/from verify.CheckSpec by a future integration (this
// package does not import verify; see the package doc comment). Category
// (verify.CheckSpec's provenance.VerificationFocused/VerificationFull
// distinction) is implied by which of VerificationDecl's two fields a
// CheckDecl appears in, rather than repeated on CheckDecl itself.
//
// Command is stored as data; see CommandDecl's doc comment for the same
// caveat.
type CheckDecl struct {
	// Name is a short, stable identifier for this check (e.g. "lint").
	Name string `yaml:"name"`
	// Command is the shell command line that performs this check.
	Command string `yaml:"command"`
	// Reason explains why this check was selected or is required.
	Reason string `yaml:"reason,omitempty"`
	// RequiredTool names a binary this check depends on (e.g.
	// "golangci-lint"), or "" if it has no such dependency.
	RequiredTool string `yaml:"required_tool,omitempty"`
	// Networked marks a check that performs network I/O.
	Networked bool `yaml:"networked,omitempty"`
}

// VerificationDecl declares this repository's verification commands, split
// into focused (recommended for a specific change) and full (always
// required before push or release), per ADR-0032's "focused and
// repository-wide verification commands." This mirrors verify.Plan's
// FocusedChecks/FullGates split: full is never a function of what changed,
// and nothing in this schema lets a consumer treat focused as a substitute
// for full.
type VerificationDecl struct {
	// Focused lists checks recommended for a specific, scoped change.
	Focused []CheckDecl `yaml:"focused,omitempty"`
	// Full lists checks that are always required before push or release,
	// regardless of what changed.
	Full []CheckDecl `yaml:"full,omitempty"`
}

// OptionalService describes an external or optional service this
// repository's agents may use when available (e.g. a static-analysis
// dashboard, a vulnerability database), per ADR-0032's "required tools and
// optional services." Unlike RequiredTools, an OptionalService's absence
// should never block ordinary development; consumers should report it as
// unavailable rather than failing.
type OptionalService struct {
	// Name is a short, stable identifier for this service (e.g.
	// "SonarCloud").
	Name string `yaml:"name"`
	// Description explains what the service provides and when it's used.
	Description string `yaml:"description,omitempty"`
}

// Contract is the top-level, versioned repository agent contract
// (`modulex.agent.yaml`). See the package doc comment for what it is and
// is not, and docs/planning/agent-repository-contract-guide.md for the
// full guide.
type Contract struct {
	// SchemaVersion identifies the version of this schema the document
	// was written against (see the package-level SchemaVersion constant).
	SchemaVersion string `yaml:"schema_version"`
	// Projects lists every project this contract covers. A contract
	// describing zero projects is rejected by Validate as meaningless.
	Projects []Project `yaml:"projects"`
	// Instructions declares applicable agent-instruction files and their
	// precedence.
	Instructions InstructionPrecedence `yaml:"instructions,omitempty"`
	// Boundaries lists lifecycle/module boundaries this repository
	// enforces.
	Boundaries []Boundary `yaml:"boundaries,omitempty"`
	// Commands lists commands this repository's agents may run,
	// classified by impact.
	Commands []CommandDecl `yaml:"commands,omitempty"`
	// Verification declares focused and full verification commands.
	Verification VerificationDecl `yaml:"verification,omitempty"`
	// ProtectedPaths lists paths agents must not modify without explicit
	// human approval, per ADR-0032's "generated and protected paths" and
	// docs/planning/agent-safety-policy.md's protected-paths list.
	ProtectedPaths []string `yaml:"protected_paths,omitempty"`
	// GeneratedPaths lists paths that are machine-generated and should
	// not be hand-edited (e.g. coverage output).
	GeneratedPaths []string `yaml:"generated_paths,omitempty"`
	// RequiredTools lists binaries this repository's declared commands
	// and checks depend on (e.g. "go", "git", "golangci-lint").
	RequiredTools []string `yaml:"required_tools,omitempty"`
	// OptionalServices lists external or optional services agents may
	// use when available.
	OptionalServices []OptionalService `yaml:"optional_services,omitempty"`
	// RequiredCredentials lists the *names* of secrets/credentials this
	// repository's workflows need (e.g. "GITHUB_TOKEN") — never their
	// values, per ADR-0032's "secret and credential requirements without
	// storing secret values." Validate additionally scans every string
	// field in this schema (including this one) for secret-shaped values
	// and fails if any are found; see secrets.go.
	RequiredCredentials []string `yaml:"required_credentials,omitempty"`
	// HandoffFormat names the schema/format an agent's handoff artifact
	// should conform to (e.g. "provenance.Envelope v1.0.0") — a reference
	// to that format by name, not a duplicate of its schema here.
	HandoffFormat string `yaml:"handoff_format,omitempty"`
}

// validCommandClasses is the set of provenance.CommandClass values
// Validate accepts for a CommandDecl.Class. Built from provenance's
// exported constants rather than redeclared literals, so a future addition
// to provenance's enum does not silently make this set stale.
var validCommandClasses = map[provenance.CommandClass]bool{
	provenance.ClassSafe:             true,
	provenance.ClassMutating:         true,
	provenance.ClassNetworked:        true,
	provenance.ClassDestructive:      true,
	provenance.ClassApprovalRequired: true,
}

// validCommandClassNames is a stable, human-readable list of the accepted
// CommandClass values, for use in Validate's error messages.
const validCommandClassNames = "safe, mutating, networked, destructive, approval_required"

// Validate checks that c is structurally well-formed and free of
// secret-shaped values, returning a single error (via errors.Join) naming
// every problem found, each specific enough to act on, or nil if c is
// valid.
//
// Structural checks:
//   - SchemaVersion is required (non-empty).
//   - At least one Project is required; a contract describing zero
//     projects is meaningless.
//   - Every Project must have a non-empty Name and Path.
//   - Every CommandDecl.Class must be one of provenance's five known
//     CommandClass values. Since CommandClass is a plain string type,
//     yaml.Unmarshal happily accepts any string into it without error —
//     this check is what actually rejects an unknown class like "yolo".
//
// Secret checks: every free-text field in the schema (project names,
// paths, and descriptions; instruction file paths and notes; boundary
// descriptions and rules; command names, commands, and reasons; check
// names, commands, reasons, and required tools; protected/generated
// paths; required tools; optional service names and descriptions;
// required-credential names; the handoff format) is scanned for
// secret-shaped values using the same best-effort pattern list
// provenance.Envelope's redaction uses (see secrets.go). Unlike
// provenance, Validate here is the only line of defense — this package has
// no Redact — so a contract containing what looks like a live credential
// fails validation outright rather than being silently rewritten. See
// secrets.go's doc comment for the limits of this detection.
func (c *Contract) Validate() error {
	var errs []error

	if strings.TrimSpace(c.SchemaVersion) == "" {
		errs = append(errs, errors.New("schema_version is required"))
	}

	if len(c.Projects) == 0 {
		errs = append(errs, errors.New("projects: at least one project is required"))
	}
	for i, p := range c.Projects {
		if strings.TrimSpace(p.Name) == "" {
			errs = append(errs, fmt.Errorf("projects[%d]: name is required", i))
		}
		if strings.TrimSpace(p.Path) == "" {
			errs = append(errs, fmt.Errorf("projects[%d] (%q): path is required", i, p.Name))
		}
	}

	for i, cmd := range c.Commands {
		if !validCommandClasses[cmd.Class] {
			errs = append(errs, fmt.Errorf(
				"commands[%d] (%q): unknown command class %q; must be one of: %s",
				i, cmd.Name, cmd.Class, validCommandClassNames,
			))
		}
	}

	errs = append(errs, c.validateSecrets()...)

	return errors.Join(errs...)
}

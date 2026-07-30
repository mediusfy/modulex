// Package agentdocs renders a contract.Contract into provider-specific
// agent instruction documents, per ADR-0032 ("Agent-First Development
// Experience", docs/adr/adr-0032-agent-first-development-experience.md),
// P1: "Generate portable agent instruction files and repository
// templates" (Jira MOD-67). The ADR's "Portability" section is the source
// of truth for what this package produces:
//
//	The repository contract is authoritative. Thin adapters may generate:
//
//	  - AGENTS.md for OpenAI/Codex and generic repository-aware agents;
//	  - CLAUDE.md or Claude-specific hooks when available;
//	  - Kimi project guidance and optional CodeGraph hook setup;
//	  - MCP discovery metadata and read-only tool descriptions.
//
//	No provider-specific hook or global configuration may be required for
//	the baseline workflow. Agents without hooks must still be able to use
//	the CLI and contract directly.
//
// This package is the "thin adapter": it turns one contract.Contract into
// consistent, provider-specific guidance so that AGENTS.md, CLAUDE.md, Kimi
// guidance, and Codex guidance never drift from the contract or from each
// other on the facts that matter (commands, verification, protected
// paths, credentials, handoff format) — only the framing text differs.
//
// # Standalone leaf package
//
// agentdocs is a standalone leaf package, like provenance, discovery,
// verify, and contract: it does not import the core modulex package. It
// depends only on contract (for the Contract schema) and the standard
// library. It does not import discovery or verify — the command matrix
// and verification guidance this package renders are derived entirely
// from contract.Contract.Commands and contract.Contract.Verification,
// which already carry everything needed (name, command, class, reason);
// nothing here needs discovery.ToolStatus or verify.CheckSpec.
//
// # Does not touch the filesystem
//
// Generate and Drift are pure functions: string (or contract.Contract) in,
// string (or bool, error) out. Neither reads nor writes a file. A caller
// (a future `modulex agent generate` CLI, out of scope for this ticket) is
// responsible for reading modulex.agent.yaml, unmarshaling it into a
// contract.Contract, and writing Generate's output to AGENTS.md, CLAUDE.md,
// or wherever the target's convention places it. This mirrors contract's
// own "never reads the filesystem beyond what a caller hands it"
// discipline.
//
// # One Generate function, not four
//
// Generate(c, target) takes a Target parameter rather than exposing four
// separate GenerateAGENTS/GenerateCLAUDE/GenerateKimi/GenerateCodex
// functions. The four outputs share the same section structure (command
// matrix, verification, safety policy, handoff) built from the same
// contract.Contract fields — only the title and introductory framing
// differ per target (see targetFraming). A single function with a Target
// switch keeps that shared structure in one place; four separate functions
// would either duplicate the section-building code four times or still
// funnel through a shared unexported helper, at which point the exported
// four-function surface would just be a thinner wrapper around what
// Generate already is. An unknown Target is a caller bug (a typo, a
// forgotten case after adding a fifth target), so Generate returns an
// error rather than panicking or silently emitting an empty/default
// document.
//
// # Hybrid text/template + string-building, not one or the other
//
// The four targets' documents share one overall skeleton (generated-file
// header, title, intro, source note, then a fixed sequence of sections,
// then a footer) but differ in the title/intro text. That skeleton is a
// text/template (docTemplateText) so the four targets' shape stays
// visibly identical and easy to diff — the same reason contract.RenderText
// uses plain string-building for one shape, but here there genuinely are
// four shapes to keep in sync.
//
// Each *section* (command matrix, verification tables, safety policy,
// projects, boundaries, handoff), however, is built with a small
// strings.Builder helper (renderCommandsSection, renderSafetySection,
// ...), not template range/if actions. A markdown table cell needs literal
// backtick characters for inline code; a text/template *source file* is
// conventionally written as a Go raw string literal, which cannot itself
// contain a literal backtick (it would terminate the raw string). Rather
// than thread a {{.BT}} backtick-escape variable through every table row
// (workable, but noisy and easy to get wrong across four call sites), each
// section is rendered to a plain Go string once — where backticks are just
// ordinary characters in an interpreted string literal — and handed to the
// skeleton template as an already-formatted, opaque field
// (renderData.CommandsSection, etc.). template.Execute does not
// escape template data (that is html/template's job, not text/template's),
// so inserting a pre-rendered markdown block via {{.CommandsSection}}
// reproduces it byte-for-byte. This also means the section-building code
// is written once and shared by all four targets, rather than repeated
// per target.
//
// # All four targets render as Markdown
//
// AGENTS.md and CLAUDE.md are unambiguously Markdown by filename
// convention. For TargetKimi and TargetCodex, this package also emits
// Markdown: both are prose guidance documents (Kimi Code CLI project
// guidance; OpenAI Codex / generic repository-aware agent guidance) meant
// to be read by an agent or a human, not machine-parsed configuration, so
// Markdown with an HTML-comment header ("<!-- GENERATED FROM ... -->",
// valid and inert in Markdown) is the natural format for all four rather
// than inventing a second comment syntax for no benefit. If a future
// target needs a genuinely different file format (e.g. a TOML hook
// config), that target's section of the generated-comment/footer helpers
// is exactly where format-specific comment syntax would be added.
//
// # Deterministic regeneration
//
// Generate(c, target) must produce byte-identical output across repeated
// calls with the same input. contract.Contract's slice fields (Commands,
// Verification.Focused/Full, ProtectedPaths, GeneratedPaths,
// RequiredTools, OptionalServices, RequiredCredentials, Projects,
// Boundaries) carry no ordering guarantee coming out of YAML — map/slice
// order from yaml.Unmarshal reflects file order, which a human editing
// modulex.agent.yaml could reorder without changing meaning. Every such
// slice is copied and sorted (by Class-then-Name for commands, by Name for
// checks/services/projects/boundaries, alphabetically for plain string
// lists) before rendering, mirroring the sorted-slice determinism
// discipline provenance and discovery already use. Sorting a *copy*, not
// c's slices in place, matters here specifically because Generate takes
// contract.Contract by value: a Go value copy of a struct still shares the
// backing array of any slice field, so sorting in place would silently
// mutate the caller's Contract out from under it.
//
// # Drift detection
//
// Drift(c, target, existingContent) reports whether existingContent (e.g.
// read by the caller from a checked-in AGENTS.md) differs from what
// Generate(c, target) would currently produce — "the checked-in file is
// stale relative to modulex.agent.yaml." Drift never reads a file itself;
// it only calls Generate and compares strings, so a future CI step or CLI
// command can wire in the actual file I/O.
//
// See docs/planning/agent-instruction-generation-guide.md for the full
// guide, including a worked example and how the four targets differ.
package agentdocs

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/mediusfy/modulex/contract"
)

// Target names one provider-specific rendering of a contract.Contract.
type Target string

const (
	// TargetAGENTS renders AGENTS.md-flavored guidance: baseline
	// instructions for OpenAI/Codex and any other generic,
	// repository-aware coding agent, per ADR-0032's portability section.
	TargetAGENTS Target = "AGENTS.md"
	// TargetCLAUDE renders CLAUDE.md-flavored guidance: Claude-specific
	// framing for Claude Code sessions.
	TargetCLAUDE Target = "CLAUDE.md"
	// TargetKimi renders Kimi Code CLI project guidance.
	TargetKimi Target = "kimi"
	// TargetCodex renders guidance for the OpenAI Codex CLI/cloud agent
	// and other generic repository-aware agents that read a dedicated
	// instructions document distinct from AGENTS.md.
	TargetCodex Target = "codex"
)

// sourceFile is the canonical name of the file a Contract is unmarshaled
// from; Generate does not read this file itself (see the package doc
// comment) but names it in the generated-file header so a reader knows
// where to make changes instead of hand-editing the generated output.
const sourceFile = "modulex.agent.yaml"

// safetyPolicyPath, contractGuidePath, and generationGuidePath are
// referenced by name in generated output; they are plain strings, not
// values read from disk, matching this package's no-filesystem-access
// discipline.
const (
	safetyPolicyPath    = "docs/planning/agent-safety-policy.md"
	contractGuidePath   = "docs/planning/agent-repository-contract-guide.md"
	generationGuidePath = "docs/planning/agent-instruction-generation-guide.md"
)

// targetFraming holds the title and introductory paragraph that
// distinguish one target's rendering from another; every other section is
// shared, built from the same contract.Contract fields regardless of
// target.
type targetFraming struct {
	title string
	intro string
}

// framingFor returns target's title/intro framing, or an error if target
// is not one of the four known constants.
func framingFor(target Target) (targetFraming, error) {
	switch target {
	case TargetAGENTS:
		return targetFraming{
			title: "AGENTS.md — Repository Agent Instructions",
			intro: "This file is baseline guidance for OpenAI/Codex and any other " +
				"generic, repository-aware coding agent operating in this " +
				"repository, per ADR-0032's portability guidance. It is read " +
				"directly from the repository root; no provider-specific hook or " +
				"global configuration is required to use it.",
		}, nil
	case TargetCLAUDE:
		return targetFraming{
			title: "CLAUDE.md — Claude Code Instructions",
			intro: "This file is Claude-specific guidance for Claude Code sessions " +
				"in this repository. It covers the same contract as AGENTS.md; " +
				"where a Claude-specific hook or setting is available it may " +
				"supplement this file, but nothing below requires one to be " +
				"present.",
		}, nil
	case TargetKimi:
		return targetFraming{
			title: "Kimi Code CLI — Project Guidance",
			intro: "This file is project guidance for the Kimi Code CLI. If Kimi " +
				"hook support is configured for this repository (for example, a " +
				"project-specific hook registered under " +
				"`~/.kimi-code/config.toml`), it may supplement this file; the " +
				"content below is authoritative on its own and does not require " +
				"that hook configuration to be present.",
		}, nil
	case TargetCodex:
		return targetFraming{
			title: "Codex / Repository-Aware Agent Instructions",
			intro: "This file is guidance for the OpenAI Codex CLI/cloud agent and " +
				"other generic repository-aware agents that read a dedicated " +
				"instructions document. Its content mirrors AGENTS.md by design " +
				"(per ADR-0032, both audiences share one baseline); it is " +
				"generated as a separate document so a Codex-specific " +
				"integration can be pointed at it without depending on " +
				"AGENTS.md's exact filename.",
		}, nil
	default:
		return targetFraming{}, fmt.Errorf("agentdocs: unknown target %q (must be one of %q, %q, %q, %q)",
			target, TargetAGENTS, TargetCLAUDE, TargetKimi, TargetCodex)
	}
}

// renderData holds the fully-rendered, opaque markdown sections the
// skeleton template assembles. Every field is pre-formatted by a section
// helper (see the package doc comment's "Hybrid text/template +
// string-building" section) so the template itself never needs to handle
// a literal backtick.
type renderData struct {
	GeneratedComment    string
	Title               string
	Intro               string
	SourceNote          string
	ProjectsSection     string
	BoundariesSection   string
	CommandsSection     string
	VerificationSection string
	SafetySection       string
	HandoffSection      string
	FooterComment       string
}

// docTemplateText is the shared skeleton for all four targets. It
// contains no literal backticks, so it can be a plain Go raw string
// literal; every field it references is already fully rendered (see
// renderData).
const docTemplateText = `{{.GeneratedComment}}

# {{.Title}}

{{.Intro}}

{{.SourceNote}}
{{if .ProjectsSection}}
{{.ProjectsSection}}
{{end}}{{if .BoundariesSection}}
{{.BoundariesSection}}
{{end}}
{{.CommandsSection}}

{{.VerificationSection}}

{{.SafetySection}}
{{if .HandoffSection}}
{{.HandoffSection}}
{{end}}
{{.FooterComment}}
`

// docTemplate is parsed once at package init from the constant template
// text above; template.Must panics only if docTemplateText itself is
// malformed, which is a compile-time-discoverable programmer error, not a
// runtime condition callers of Generate can trigger.
var docTemplate = template.Must(template.New("agentdoc").Parse(docTemplateText))

// Generate renders c as target's provider-specific agent instruction
// document. It returns an error if target is not one of the package's
// four Target constants. Generate never reads or writes a file (see the
// package doc comment) and produces byte-identical output for the same
// (c, target) pair on every call.
func Generate(c contract.Contract, target Target) (string, error) {
	framing, err := framingFor(target)
	if err != nil {
		return "", err
	}

	data := buildRenderData(c, framing)

	var buf bytes.Buffer
	if err := docTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("agentdocs: render %s: %w", target, err)
	}
	return buf.String(), nil
}

// Drift reports whether existingContent differs from what
// Generate(c, target) would currently produce — i.e. whether a checked-in
// file (e.g. AGENTS.md) is stale relative to the contract. Drift does not
// read existingContent from disk itself; the caller is responsible for
// that, matching this package's no-filesystem-access discipline.
func Drift(c contract.Contract, target Target, existingContent string) (bool, error) {
	current, err := Generate(c, target)
	if err != nil {
		return false, err
	}
	return current != existingContent, nil
}

// buildRenderData assembles renderData from c and framing, sorting a copy
// of every contract slice field first (see the package doc comment's
// "Deterministic regeneration" section for why a copy, not an in-place
// sort).
func buildRenderData(c contract.Contract, framing targetFraming) renderData {
	return renderData{
		GeneratedComment:  renderGeneratedComment(c.SchemaVersion),
		Title:             framing.title,
		Intro:             framing.intro,
		SourceNote:        renderSourceNote(c.SchemaVersion),
		ProjectsSection:   renderProjectsSection(sortedProjects(c.Projects)),
		BoundariesSection: renderBoundariesSection(sortedBoundaries(c.Boundaries)),
		CommandsSection:   renderCommandsSection(sortedCommands(c.Commands)),
		VerificationSection: renderVerificationSection(
			sortedChecks(c.Verification.Focused),
			sortedChecks(c.Verification.Full),
		),
		SafetySection: renderSafetySection(
			sortedStrings(c.ProtectedPaths),
			sortedStrings(c.RequiredCredentials),
		),
		HandoffSection: renderHandoffSection(c.HandoffFormat),
		FooterComment:  renderFooterComment(c.SchemaVersion),
	}
}

// -- section renderers --------------------------------------------------

func renderGeneratedComment(schemaVersion string) string {
	return fmt.Sprintf(
		"<!-- GENERATED FROM %s (schema v%s) — DO NOT EDIT BY HAND. "+
			"Regenerate via agentdocs.Generate; see %s. -->",
		sourceFile, schemaVersion, generationGuidePath,
	)
}

func renderFooterComment(schemaVersion string) string {
	return fmt.Sprintf(
		"<!-- End of generated content. Source: %s (schema v%s). "+
			"Regenerate via agentdocs.Generate rather than editing by hand. -->",
		sourceFile, schemaVersion,
	)
}

func renderSourceNote(schemaVersion string) string {
	return fmt.Sprintf(
		"Generated from `%s` (schema v%s). See `%s` for the contract's full "+
			"schema and `%s` for how this file is generated and how to detect "+
			"drift between it and the checked-in contract. Do not hand-edit "+
			"below this line — regenerate instead.",
		sourceFile, schemaVersion, contractGuidePath, generationGuidePath,
	)
}

func renderProjectsSection(projects []contract.Project) string {
	if len(projects) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Projects\n\n")
	for _, p := range projects {
		fmt.Fprintf(&b, "- **%s** (`%s`)", p.Name, p.Path)
		if p.ModulePath != "" {
			fmt.Fprintf(&b, " — module `%s`", p.ModulePath)
		}
		b.WriteString("\n")
		if p.Description != "" {
			fmt.Fprintf(&b, "  %s\n", p.Description)
		}
		for _, root := range p.CompositionRoots {
			fmt.Fprintf(&b, "  - composition root: `%s`\n", root)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderBoundariesSection(boundaries []contract.Boundary) string {
	if len(boundaries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Boundaries\n\n")
	for _, bound := range boundaries {
		fmt.Fprintf(&b, "- **%s**", bound.Name)
		if bound.Rule != "" {
			fmt.Fprintf(&b, " — %s", bound.Rule)
		}
		b.WriteString("\n")
		if bound.Description != "" {
			fmt.Fprintf(&b, "  %s\n", bound.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderCommandsSection(commands []contract.CommandDecl) string {
	var b strings.Builder
	b.WriteString("## Command matrix\n\n")
	if len(commands) == 0 {
		b.WriteString("No commands declared in the contract.")
		return b.String()
	}
	b.WriteString("| Class | Name | Command | Reason |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s |\n",
			cmd.Class, cmd.Name, cmd.Command, emptyDash(cmd.Reason))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderVerificationSection(focused, full []contract.CheckDecl) string {
	var b strings.Builder
	b.WriteString("## Verification\n\n")
	b.WriteString("Focused checks are recommended for a specific, scoped change. " +
		"Full gates are always required before push or release, regardless of " +
		"what changed — focused checks are never a substitute for full " +
		"gates.\n\n")
	b.WriteString("### Focused checks\n\n")
	writeCheckTable(&b, focused)
	b.WriteString("\n### Full gates (always required before push or release)\n\n")
	writeCheckTable(&b, full)
	return strings.TrimRight(b.String(), "\n")
}

func writeCheckTable(b *strings.Builder, checks []contract.CheckDecl) {
	if len(checks) == 0 {
		b.WriteString("None declared.\n")
		return
	}
	b.WriteString("| Name | Command | Reason | Required tool |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, chk := range checks {
		fmt.Fprintf(b, "| %s | `%s` | %s | %s |\n",
			chk.Name, chk.Command, emptyDash(chk.Reason), emptyDash(chk.RequiredTool))
	}
}

// renderSafetySection renders a standalone summary of
// docs/planning/agent-safety-policy.md's approval boundary, plus the
// contract's protected paths and required-credential names. The approval
// boundary bullet list below is a static statement, not derived from the
// contract — contract.Contract has no approval-boundary field of its own
// (see the ticket's explicit note that this is intentional) — so it is
// hand-written here to mirror agent-safety-policy.md's "Human approval
// boundary" section, and must be kept in sync with that document by
// whoever edits either one.
func renderSafetySection(protectedPaths, requiredCredentials []string) string {
	var b strings.Builder
	b.WriteString("## Safety policy\n\n")
	b.WriteString("The default posture in this repository is repository-local and " +
		"read-only: agents may read any file and run non-mutating commands " +
		"freely. The following always require explicit, current-session human " +
		"approval — a general standing instruction to \"work autonomously\" " +
		"does not itself cover these unless it names them specifically:\n\n")
	b.WriteString("- Pushing to a shared branch or the default branch.\n")
	b.WriteString("- Opening, merging, or closing a pull request.\n")
	b.WriteString("- Tagging or publishing a release.\n")
	b.WriteString("- Deleting a branch, file, or remote resource not created by " +
		"the agent in the current session.\n")
	b.WriteString("- Any infrastructure or CI/CD configuration change.\n")
	b.WriteString("- Any external mutation (issue tracker, chat, or third-party " +
		"service state).\n")
	b.WriteString("- Force-pushing, history rewriting, or skipping commit " +
		"hooks/CI gates.\n\n")
	fmt.Fprintf(&b, "See `%s` for the full policy; this section summarizes it and "+
		"does not replace it. Where the two disagree, `%s` wins.\n",
		safetyPolicyPath, safetyPolicyPath)

	if len(protectedPaths) > 0 {
		b.WriteString("\n### Protected paths (do not modify without explicit human approval)\n\n")
		for _, p := range protectedPaths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
	}

	if len(requiredCredentials) > 0 {
		b.WriteString("\n### Required credentials (names only — never values)\n\n")
		for _, c := range requiredCredentials {
			fmt.Fprintf(&b, "- `%s`\n", c)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func renderHandoffSection(handoffFormat string) string {
	if handoffFormat == "" {
		return ""
	}
	return fmt.Sprintf(
		"## Handoff\n\nBefore reporting work as complete, run the full "+
			"verification gates above and produce a handoff artifact "+
			"conforming to: `%s`. Report skipped or unavailable checks "+
			"explicitly — never report a check as passing when it was "+
			"skipped or could not run.",
		handoffFormat,
	)
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// -- deterministic ordering ----------------------------------------------
//
// Every helper below copies its input before sorting, rather than sorting
// in place, so buildRenderData never mutates the contract.Contract a
// caller passed to Generate by value (see the package doc comment's
// "Deterministic regeneration" section for why a value copy still shares
// slice backing arrays).

func sortedProjects(in []contract.Project) []contract.Project {
	out := append([]contract.Project(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedBoundaries(in []contract.Boundary) []contract.Boundary {
	out := append([]contract.Boundary(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedCommands(in []contract.CommandDecl) []contract.CommandDecl {
	out := append([]contract.CommandDecl(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func sortedChecks(in []contract.CheckDecl) []contract.CheckDecl {
	out := append([]contract.CheckDecl(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

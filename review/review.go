// Package review implements "agent diff review": checking a changeset for
// boundary violations, secret-shaped values, API compatibility breaks, and
// missing changelog obligations, per ADR-0032 ("Agent-First Development
// Experience"), P1: "Add diff review for boundaries, secrets, API
// compatibility, and changelog obligations" (Jira MOD-65), step 6 of the
// ADR's "Standard agent workflow":
//
//	`modulex agent review` checks the diff for unexpected files, secrets,
//	boundary violations, API compatibility changes, generated-file drift,
//	and missing changelog or documentation updates.
//
// review is a standalone leaf package (github.com/mediusfy/modulex/review),
// like verify, discovery, and provenance: it does not import the core
// modulex package. It depends on verify for CheckSpec/Run (reusing the same
// tool-availability/network gating and "sh -c" execution rather than
// duplicating it) and on provenance for the VerificationResult/
// VerificationCategory types, so this package's output is exactly what a
// future `modulex agent handoff` would consume, with no translation layer.
//
// # Why this package exists separately from verify
//
// verify.FullGates already runs check-consumer-boundary, check-module-
// boundary, check-api-compat, and check-changelog — but every one of those
// entries carries Category provenance.VerificationFull, because verify's
// job is "what must pass before push or release," not "what does this
// specific diff need reviewed." provenance.VerificationCategory separately
// defines VerificationBoundary, VerificationCompatibility, and
// VerificationChangelog (alongside VerificationSecurity and
// VerificationSecretScan) precisely for this package's use: Checks below
// re-declares the same four make targets with the category that actually
// describes what each one reviews, so a caller building a diff-review
// report (or a provenance.Envelope) can group and label results correctly
// without review and verify producing conflicting categories for the same
// underlying command.
//
// # Boundary and compatibility checks are not diff-scoped
//
// check-consumer-boundary and check-module-boundary inspect the working
// tree as it stands, not a baseRef..headRef diff — this matches how they
// already run in CI (unconditionally, every push/PR) and in verify.FullGates.
// check-api-compat compares the working tree against the latest git tag, not
// against baseRef. Only the secret scan (ScanSecrets, secrets.go) is
// genuinely diff-scoped, because scanning the entire repository for
// secret-shaped strings on every review would flag pre-existing content
// unrelated to this change and make the check impossible to act on.
package review

import (
	"context"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/verify"
)

// Checks is the fixed list of diff-review checks executed by Review via
// verify.Run — every one of this repository's existing boundary,
// compatibility, and changelog gates, labeled with the provenance.
// VerificationCategory that describes what it reviews (see the package doc
// comment's "Why this package exists separately from verify"). The secret
// scan is not included here: it is not a shell command verify.Run can
// execute, since it needs baseRef/headRef to compute a diff; see ScanSecrets.
//
// Exported so a caller can iterate the canonical list without hardcoding it
// themselves, mirroring verify.FullGates. Treat it as read-only.
var Checks = []verify.CheckSpec{
	{
		Name:         "check-consumer-boundary",
		Command:      "make check-consumer-boundary",
		Category:     provenance.VerificationBoundary,
		Reason:       "verifies a consumer importing only the core package does not compile in an integration adapter as a build dependency",
		RequiredTool: "go",
	},
	{
		Name:         "check-module-boundary",
		Command:      "make check-module-boundary",
		Category:     provenance.VerificationBoundary,
		Reason:       "runs the modboundary analyzer against examples/deployment to enforce feature-module boundaries",
		RequiredTool: "go",
	},
	{
		Name:         "check-api-compat",
		Command:      "make check-api-compat",
		Category:     provenance.VerificationCompatibility,
		Reason:       "reports public API changes since the latest git tag",
		RequiredTool: "go",
	},
	{
		Name:         "check-changelog",
		Command:      "make check-changelog",
		Category:     provenance.VerificationChangelog,
		Reason:       "verifies CHANGELOG.md is updated when required (working tree vs origin/main)",
		RequiredTool: "git",
	},
}

// Review runs the full diff-review check set — boundary, API compatibility,
// and changelog checks (Checks, via verify.Run) plus a secret scan over
// baseRef..headRef (ScanSecrets) — and returns one provenance.
// VerificationResult per check, in Checks order followed by the secret scan
// result.
//
// tools should be discovery.Discover's Tools field (or an equivalent slice);
// it gates each CheckSpec.RequiredTool exactly as verify.Run documents.
// allowNetwork is passed through to verify.Run for forward compatibility
// with a future networked check; none of Checks is currently Networked, so
// it has no effect today.
//
// The result is []provenance.VerificationResult, the same type verify.Run
// produces, so verify.RenderText(results) renders it as a human-readable,
// per-category summary without this package needing its own renderer.
func Review(ctx context.Context, baseRef, headRef string, tools []discovery.ToolStatus, allowNetwork bool) []provenance.VerificationResult {
	results := verify.Run(ctx, Checks, tools, allowNetwork)
	return append(results, ScanSecrets(ctx, baseRef, headRef))
}

// Package review implements "agent diff review": checking a changeset for
// boundary violations, secret-shaped values, API compatibility breaks,
// protected-path edits, and missing changelog obligations, per ADR-0032
// (Jira MOD-65). It depends on verify for CheckSpec/Run and on provenance
// for VerificationResult/VerificationCategory, so its output is exactly
// what `modulex agent handoff` consumes.
//
// Checks re-declares verify.FullGates' boundary/compatibility/changelog
// targets under review-specific categories (VerificationBoundary etc.
// instead of VerificationFull), since verify's job is "what must pass
// before push" and this package's is "what this diff needs reviewed."
// Those checks inspect the working tree, not the diff; only the secret
// scan and CheckProtectedPaths are genuinely diff-scoped.
package review

import (
	"context"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/verify"
)

// Checks is the fixed list of diff-review checks executed by Review via
// verify.Run. The secret scan and protected-paths check aren't included
// here since they need baseRef/headRef, not a shell command; see
// ScanSecrets and CheckProtectedPaths. Exported so a caller can iterate the
// canonical list, mirroring verify.FullGates. Treat it as read-only.
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

// Review runs Checks (via verify.Run), then ScanSecrets and
// CheckProtectedPaths over baseRef..headRef, returning one
// provenance.VerificationResult per check in that order.
//
// dir is the repository root every check runs against (empty means the
// calling process's own cwd, unchanged from before dir existed as a
// parameter). tools gates RequiredTool as verify.Run documents.
// allowNetwork is forwarded to verify.Run. protectedPaths is
// contract.Contract.ProtectedPaths, if the caller has one; nil is normal,
// not an error.
func Review(ctx context.Context, dir, baseRef, headRef string, tools []discovery.ToolStatus, allowNetwork bool, protectedPaths []string) []provenance.VerificationResult {
	checks := make([]verify.CheckSpec, len(Checks))
	for i, c := range Checks {
		c.Dir = dir
		checks[i] = c
	}

	results := verify.Run(ctx, checks, tools, allowNetwork)
	results = append(results, ScanSecrets(ctx, dir, baseRef, headRef))
	return append(results, CheckProtectedPaths(ctx, dir, baseRef, headRef, protectedPaths))
}

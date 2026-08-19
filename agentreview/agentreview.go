// Package agentreview is the shared review-and-handoff orchestration behind
// both the `modulex agent` CLI (tools/agentcli) and the read-only MCP server
// (tools/mcpserver). Per ADR-0032 and ADR-0035 there is one source of
// repository logic: the CLI's review/handoff subcommands and the MCP server's
// review_diff/create_handoff tools both route through this package, so their
// output is identical by construction rather than by two parallel
// implementations kept in sync by hand (Jira MOD-76).
//
// It is deliberately dependency-light — discovery, review, and provenance
// only, plus the standard library. Contract loading (and therefore the YAML
// dependency) stays in each adapter, which discovers the repository and
// resolves protected paths itself and passes them in; this package never
// parses a contract, so it adds nothing to the root module's dependency set.
package agentreview

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
)

// Review runs the repository's review checks (boundary, compatibility,
// changelog, secret scan, and protected-paths) over the baseRef...headRef diff
// using repo's already-discovered Root and Tools, returning one
// provenance.VerificationResult per check in a stable order.
//
// It is pure orchestration over review.Review: the caller discovers repo and
// resolves protectedPaths (from a contract, if any) beforehand, so this
// package stays free of any contract/YAML dependency. It never mutates the
// repository.
func Review(ctx context.Context, repo discovery.Repository, baseRef, headRef string, allowNetwork bool, protectedPaths []string) []provenance.VerificationResult {
	return review.Review(ctx, repo.Root, baseRef, headRef, repo.Tools, allowNetwork, protectedPaths)
}

// Envelope assembles a redacted, validated provenance.Envelope describing
// repo's state (path, branch, commit, dirty), the calling agent (agentName and
// tool, e.g. "modulex-cli" or "mcp"), and the supplied verification results.
// It resolves the commit and branch via an argv-based `git rev-parse` (never a
// shell, so repo.Root cannot inject shell syntax). On a detached HEAD — the
// normal state for a CI checkout (e.g. refs/pull/N/merge) — Branch is left
// empty and omitted from the JSON rather than recorded as the literal,
// meaningless "HEAD" that `git rev-parse --abbrev-ref HEAD` reports there;
// Branch is an optional Envelope field, Commit carries the identity.
//
// The returned Envelope has already been Redacted and passed Validate; a
// Validate failure (e.g. repo is not a git repository, so Commit is empty) is
// returned as an error rather than a partial envelope, with all Validate
// findings flattened onto one "; "-separated line so CLI stderr and MCP error
// payloads stay single-line.
//
// verification is supplied by the caller: the CLI's handoff passes the results
// of a Review call it just ran, while the MCP server's create_handoff passes
// results gathered across earlier tool calls. Either way the envelope is built
// identically here.
func Envelope(ctx context.Context, repo discovery.Repository, agentName, tool string, verification []provenance.VerificationResult) (provenance.Envelope, error) {
	commit, err := gitRevParse(ctx, repo.Root, "HEAD")
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("resolving commit: %w", err)
	}
	branch, err := gitRevParse(ctx, repo.Root, "--abbrev-ref", "HEAD")
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("resolving branch: %w", err)
	}
	if branch == "HEAD" { // detached HEAD: omit rather than record the literal
		branch = ""
	}

	env := provenance.Envelope{
		SchemaVersion: provenance.SchemaVersion,
		Repository: provenance.RepoState{
			Path:   repo.Root,
			Branch: branch,
			Commit: commit,
			Dirty:  repo.Dirty,
		},
		Agent: provenance.AgentInfo{
			Name: agentName,
			Tool: tool,
		},
		Verification: verification,
		CreatedAt:    time.Now().UTC(),
	}
	env.Redact()
	if err := env.Validate(); err != nil {
		return provenance.Envelope{}, fmt.Errorf("assembled handoff failed validation: %s",
			strings.Join(flattenErrors(err), "; "))
	}
	return env, nil
}

// flattenErrors flattens an error tree built by errors.Join (as
// provenance.Envelope.Validate returns) into one string per leaf error, so
// Envelope's validation failure reads as a single "; "-joined line instead of
// errors.Join's multi-line message. Falling back to a single-element slice
// for any other error shape keeps this safe on an arbitrary error.
func flattenErrors(err error) []string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs := joined.Unwrap()
		out := make([]string, len(errs))
		for i, e := range errs {
			out[i] = e.Error()
		}
		return out
	}
	return []string{err.Error()}
}

// gitRevParse runs `git -C root rev-parse args...` via an argv slice (never a
// shell, so root/args cannot inject shell syntax regardless of content) and
// returns the trimmed output, honoring ctx for cancellation.
func gitRevParse(ctx context.Context, root string, args ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, gitPath, append([]string{"-C", root, "rev-parse"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

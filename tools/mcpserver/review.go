package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/agentreview"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
)

// ReviewDiffIn is review_diff's input.
type ReviewDiffIn struct {
	// Root is used to detect available tools and as the working directory
	// every review command runs in. Defaults to "." if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root: used both to detect available tools and as the working directory review commands run in; defaults to \".\" if empty"`
	// BaseRef and HeadRef are git refs review.Review diffs between (git's
	// "A...B" triple-dot form — everything reachable from HeadRef but not
	// from BaseRef's merge-base).
	BaseRef string `json:"base_ref" jsonschema:"git ref to diff from, e.g. \"origin/main\""`
	HeadRef string `json:"head_ref" jsonschema:"git ref to diff to, e.g. \"HEAD\""`
	// AllowNetwork gates any Networked check in review.Checks.
	AllowNetwork bool `json:"allow_network,omitempty" jsonschema:"if false (default), a Networked check is reported as skipped rather than run"`
}

// ReviewDiffOut is review_diff's output.
type ReviewDiffOut struct {
	Results []provenance.VerificationResult `json:"results"`
}

// reviewDiff discovers root's repository and runs the shared
// agentreview.Review over it. A bad baseRef/headRef surfaces as
// StatusUnavailable inside Results, not a handler error (the underlying git
// calls use argv, never "sh -c"); this only errors if discovery.Discover
// fails, or the contract file at baseRef is unparseable.
//
// protectedPaths comes from review.LoadProtectedPathsAtRef(root, baseRef) —
// baseRef, not root's checked-out head, matching the CLI's
// loadProtectedPaths and the hosted engine's Modulex.Review: root may be the
// side under review (e.g. a feature branch an agent is iterating on), so a
// change that both violates a protected path and edits or deletes
// modulex.agent.yaml in the same commit must not be able to make this check
// pass by rewriting the policy that judges it. An absent contract at baseRef
// means none enforced, matching CheckProtectedPaths' "absence is not
// failure." A present-but-unparseable contract is a real error, not "no
// protected paths" — failing open there would silently disable the one
// check meant to catch unauthorized edits, and the CLI's `modulex agent
// review` fails closed on the same input (identical by construction).
// Malformed glob entries are CheckProtectedPaths' own concern: it fails
// naming them rather than silently skipping them, so they need no special
// handling here.
func reviewDiff(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) (ReviewDiffOut, error) {
	resolvedRoot := resolveRoot(root)
	repo, err := discovery.Discover(resolvedRoot)
	if err != nil {
		return ReviewDiffOut{}, fmt.Errorf("mcpserver: discover %q: %w", resolvedRoot, err)
	}

	protectedPaths, err := review.LoadProtectedPathsAtRef(ctx, resolvedRoot, baseRef)
	if err != nil {
		if errors.Is(err, review.ErrContractUnparseable) {
			return ReviewDiffOut{}, fmt.Errorf("mcpserver: %w", err)
		}
		// Any other failure (typically baseRef does not resolve) must not
		// be a handler error, matching this function's "a bad
		// baseRef/headRef surfaces as StatusUnavailable inside Results"
		// contract: CheckProtectedPaths below performs the same baseRef
		// resolution internally (via ChangedFiles) and will independently
		// report StatusUnavailable for it.
		protectedPaths = nil
	}

	results := agentreview.Review(ctx, repo, baseRef, headRef, allowNetwork, protectedPaths)
	return ReviewDiffOut{Results: results}, nil
}

func reviewDiffHandler(ctx context.Context, _ *mcp.CallToolRequest, in ReviewDiffIn) (*mcp.CallToolResult, ReviewDiffOut, error) {
	out, err := reviewDiff(ctx, in.Root, in.BaseRef, in.HeadRef, in.AllowNetwork)
	return nil, out, err
}

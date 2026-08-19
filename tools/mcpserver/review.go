package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/agentreview"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
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
// calls use argv, never "sh -c"); this only errors if discovery.Discover or
// readContract itself fails, or the contract file is unparseable.
//
// protectedPaths comes from readContract(root): an absent contract means none
// enforced, matching CheckProtectedPaths' "absence is not failure." A
// present-but-unparseable contract is a real error, not "no protected paths"
// — failing open there would silently disable the one check meant to catch
// unauthorized edits, and the CLI's `modulex agent review` fails closed on
// the same input (MOD-76: identical by construction). A contract that parses
// but fails Validate for some unrelated reason still has its ProtectedPaths
// enforced — invalid isn't the same as absent. Malformed glob entries are
// CheckProtectedPaths' own concern: it fails naming them rather than
// silently skipping them, so they need no special handling here.
func reviewDiff(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) (ReviewDiffOut, error) {
	resolvedRoot := resolveRoot(root)
	repo, err := discovery.Discover(resolvedRoot)
	if err != nil {
		return ReviewDiffOut{}, fmt.Errorf("mcpserver: discover %q: %w", resolvedRoot, err)
	}

	contractOut, err := readContract(root)
	if err != nil {
		return ReviewDiffOut{}, err
	}
	if contractOut.Present && contractOut.Contract == nil {
		return ReviewDiffOut{}, fmt.Errorf("mcpserver: %s is present but unparseable: %s",
			contractFileName, strings.Join(contractOut.ValidationErrors, "; "))
	}
	var protectedPaths []string
	if contractOut.Contract != nil {
		protectedPaths = contractOut.Contract.ProtectedPaths
	}

	results := agentreview.Review(ctx, repo, baseRef, headRef, allowNetwork, protectedPaths)
	return ReviewDiffOut{Results: results}, nil
}

func reviewDiffHandler(ctx context.Context, _ *mcp.CallToolRequest, in ReviewDiffIn) (*mcp.CallToolResult, ReviewDiffOut, error) {
	out, err := reviewDiff(ctx, in.Root, in.BaseRef, in.HeadRef, in.AllowNetwork)
	return nil, out, err
}

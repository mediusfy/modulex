package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
)

// ReviewDiffIn is review_diff's input.
type ReviewDiffIn struct {
	// Root is used to detect available tools and as the working directory
	// review.Review's git invocations run in. Defaults to "." if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root; defaults to \".\" if empty"`
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

// reviewDiff resolves root's available tools and runs review.Review. Unlike
// run_verification, this call is safe from shell injection regardless of
// baseRef/headRef content: review.Review's git diff invocation uses an argv
// slice (exec.CommandContext), never "sh -c" — a bad ref simply makes the
// underlying git command fail, surfaced as a provenance.StatusUnavailable
// result inside Results, not a handler error. This handler therefore only
// returns an error when resolveTools itself fails (an invalid root).
func reviewDiff(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) (ReviewDiffOut, error) {
	tools, err := resolveTools(root)
	if err != nil {
		return ReviewDiffOut{}, err
	}
	return ReviewDiffOut{Results: review.Review(ctx, baseRef, headRef, tools, allowNetwork)}, nil
}

func reviewDiffHandler(ctx context.Context, _ *mcp.CallToolRequest, in ReviewDiffIn) (*mcp.CallToolResult, ReviewDiffOut, error) {
	out, err := reviewDiff(ctx, in.Root, in.BaseRef, in.HeadRef, in.AllowNetwork)
	return nil, out, err
}

package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
)

// ReviewDiffIn is review_diff's input.
type ReviewDiffIn struct {
	// Root is used to detect which tools (go, git, golangci-lint, ...) are
	// available on PATH (gating each check's RequiredTool) and as the
	// working directory every review command — including the secret scan's
	// git diff invocation — actually runs in. Defaults to "." if empty.
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

// reviewDiff resolves root's available tools and runs review.Review with
// root as the working directory for every check and the secret scan's git
// diff (review.Review's dir parameter — see its doc comment). Unlike
// run_verification, this call is safe from shell injection regardless of
// baseRef/headRef content: review.Review's git diff invocation uses an argv
// slice (exec.CommandContext), never "sh -c" — a bad ref simply makes the
// underlying git command fail, surfaced as a provenance.StatusUnavailable
// result inside Results, not a handler error. This handler therefore only
// returns an error when resolveTools or readContract itself fails (an
// invalid root, or an I/O error reading modulex.agent.yaml).
//
// protectedPaths comes from readContract(root), which distinguishes three
// normal (non-error) outcomes from a real Go error — see readContract's own
// doc comment for the full tri-state. reviewDiff only cares about one
// further distinction on top of that: Contract nil (no file present, or the
// file failed to parse as YAML) means no protected paths are enforced for
// this call, matching CheckProtectedPaths' "absence is not failure"
// behavior. Contract non-nil — including when it failed
// contract.Contract.Validate for some unrelated reason (e.g. a bad boundary
// rule) — still has its ProtectedPaths read and enforced; a semantically
// invalid contract is not the same as "no contract," and read_contract
// already reports that invalidity tri-state separately for a caller who
// wants to act on it. A genuine readContract error (bad root, or an I/O
// failure other than the file simply not existing) is propagated as a real
// handler error rather than silently treated as "no protected paths" —
// unlike a missing or unparsable contract, an error reading an existing
// contract file gives no basis for concluding protected paths don't apply,
// so failing open here would silently disable the one check whose entire
// purpose is catching unauthorized edits.
func reviewDiff(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) (ReviewDiffOut, error) {
	tools, err := resolveTools(root)
	if err != nil {
		return ReviewDiffOut{}, err
	}
	resolvedRoot := resolveRoot(root)

	contractOut, err := readContract(root)
	if err != nil {
		return ReviewDiffOut{}, err
	}
	var protectedPaths []string
	if contractOut.Contract != nil {
		protectedPaths = contractOut.Contract.ProtectedPaths
	}

	return ReviewDiffOut{Results: review.Review(ctx, resolvedRoot, baseRef, headRef, tools, allowNetwork, protectedPaths)}, nil
}

func reviewDiffHandler(ctx context.Context, _ *mcp.CallToolRequest, in ReviewDiffIn) (*mcp.CallToolResult, ReviewDiffOut, error) {
	out, err := reviewDiff(ctx, in.Root, in.BaseRef, in.HeadRef, in.AllowNetwork)
	return nil, out, err
}

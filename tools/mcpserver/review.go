package mcpserver

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// reviewDiff resolves root's tools and runs review.Review with root as the
// working directory. A bad baseRef/headRef surfaces as StatusUnavailable
// inside Results, not a handler error (review.Review's git calls use argv,
// never "sh -c"); this only errors if resolveTools or readContract itself
// fails.
//
// protectedPaths comes from readContract(root): Contract nil (missing or
// unparsable file) means none enforced, matching CheckProtectedPaths'
// "absence is not failure." Contract non-nil is enforced even if it failed
// Validate for an unrelated reason — invalid isn't the same as absent. A
// genuine readContract error is propagated rather than treated as "no
// protected paths," since failing open there would silently disable the
// one check meant to catch unauthorized edits.
//
// One entry in ProtectedPaths itself can be a malformed glob (an unmatched
// "["), which is exactly what contract.Contract.Validate's own
// protected_paths check exists to catch — but a contract that fails
// Validate for an unrelated reason still has its ProtectedPaths read and
// enforced above, so Validate alone does not guarantee this call catches
// the typo. CheckProtectedPaths silently treats a bad pattern as "never
// matches" as a defense-in-depth fallback, not a primary detection
// mechanism, so reviewDiff re-validates each pattern itself, drops the
// malformed ones before enforcement (valid patterns still protect what
// they can), and prepends a StatusFail result naming them.
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

	var results []provenance.VerificationResult
	if badGlob := invalidProtectedPathGlobResult(protectedPaths); badGlob != nil {
		results = append(results, *badGlob)
		protectedPaths = validProtectedPathGlobs(protectedPaths)
	}
	results = append(results, review.Review(ctx, resolvedRoot, baseRef, headRef, tools, allowNetwork, protectedPaths)...)

	return ReviewDiffOut{Results: results}, nil
}

// invalidProtectedPathGlobResult reports a StatusFail VerificationResult
// naming every entry in protectedPaths that fails to compile as a
// path.Match glob, or nil if all entries are valid. Mirrors
// contract.Contract.Validate's own protected_paths glob check so a typo
// caught there is also flagged here, where it would otherwise silently
// stop being enforced (see CheckProtectedPaths' path.ErrBadPattern
// fallback).
func invalidProtectedPathGlobResult(protectedPaths []string) *provenance.VerificationResult {
	var bad []string
	for _, p := range protectedPaths {
		if _, err := path.Match(p, ""); err != nil {
			bad = append(bad, fmt.Sprintf("%q: %v", p, err))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return &provenance.VerificationResult{
		Name:     "check-protected-paths-contract",
		Category: provenance.VerificationProtectedPaths,
		Status:   provenance.StatusFail,
		Message: fmt.Sprintf("modulex.agent.yaml has %d invalid protected_paths glob pattern(s), excluded from enforcement below:\n%s",
			len(bad), strings.Join(bad, "\n")),
	}
}

// validProtectedPathGlobs returns protectedPaths with every entry that
// fails to compile as a path.Match glob removed.
func validProtectedPathGlobs(protectedPaths []string) []string {
	valid := make([]string, 0, len(protectedPaths))
	for _, p := range protectedPaths {
		if _, err := path.Match(p, ""); err == nil {
			valid = append(valid, p)
		}
	}
	return valid
}

func reviewDiffHandler(ctx context.Context, _ *mcp.CallToolRequest, in ReviewDiffIn) (*mcp.CallToolResult, ReviewDiffOut, error) {
	out, err := reviewDiff(ctx, in.Root, in.BaseRef, in.HeadRef, in.AllowNetwork)
	return nil, out, err
}

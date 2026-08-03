package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/verify"
)

// RecommendVerificationIn is recommend_verification's input.
type RecommendVerificationIn struct {
	// ChangedFiles are paths (relative to the repository root) that
	// changed, e.g. from `git diff --name-only`.
	ChangedFiles []string `json:"changed_files,omitempty" jsonschema:"paths (relative to the repository root) that changed, e.g. from git diff --name-only"`
}

// RecommendVerificationOut is recommend_verification's output.
type RecommendVerificationOut struct {
	Plan verify.Plan `json:"plan"`
}

// recommendVerification wraps verify.PlanFor. Pure path-shape mapping, no
// I/O — this call can never fail.
func recommendVerification(changedFiles []string) RecommendVerificationOut {
	return RecommendVerificationOut{Plan: verify.PlanFor(changedFiles)}
}

func recommendVerificationHandler(_ context.Context, _ *mcp.CallToolRequest, in RecommendVerificationIn) (*mcp.CallToolResult, RecommendVerificationOut, error) {
	return nil, recommendVerification(in.ChangedFiles), nil
}

// CheckSpecIn mirrors verify.CheckSpec field-for-field, as the typed input
// shape for run_verification's Checks. See verify.CheckSpec's doc comment
// for what each field means; toCheckSpec converts one CheckSpecIn to the
// verify.CheckSpec verify.Run actually consumes.
type CheckSpecIn struct {
	Name         string                          `json:"name" jsonschema:"short identifier, e.g. \"lint\""`
	Command      string                          `json:"command" jsonschema:"shell command line, executed verbatim via sh -c"`
	Category     provenance.VerificationCategory `json:"category" jsonschema:"one of: focused, full, boundary, compatibility, security, secret_scan, changelog"`
	Reason       string                          `json:"reason,omitempty"`
	RequiredTool string                          `json:"required_tool,omitempty" jsonschema:"a discovery tool name (e.g. \"go\", \"golangci-lint\") this check requires; if absent from the repository's detected tools, the check is reported as unavailable and never run"`
	Networked    bool                            `json:"networked,omitempty" jsonschema:"if true, this check performs network I/O and is skipped unless allow_network is true"`
}

func toCheckSpec(in CheckSpecIn) verify.CheckSpec {
	return verify.CheckSpec{
		Name:         in.Name,
		Command:      in.Command,
		Category:     in.Category,
		Reason:       in.Reason,
		RequiredTool: in.RequiredTool,
		Networked:    in.Networked,
	}
}

// RunVerificationIn is run_verification's input.
type RunVerificationIn struct {
	// Root is used only to detect which tools (go, git, golangci-lint,
	// ...) are available on PATH, gating each Checks[i].RequiredTool.
	// Defaults to "." if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root used to detect available tools; defaults to \".\" if empty"`
	// Checks are the checks to run — typically reused verbatim from a
	// prior recommend_verification or review_diff result's checks, or from
	// this repository's own documented full gate list, rather than
	// authored ad hoc. See run_verification's tool description
	// (mcpserver.go) for why.
	Checks []CheckSpecIn `json:"checks" jsonschema:"checks to run, typically reused verbatim from recommend_verification's plan or this repository's documented full gate list"`
	// AllowNetwork gates any check with Networked set. If false (the
	// default), such a check is reported as skipped rather than run.
	AllowNetwork bool `json:"allow_network,omitempty" jsonschema:"if false (default), a Networked check is reported as skipped rather than run"`
}

// RunVerificationOut is run_verification's output.
type RunVerificationOut struct {
	Results []provenance.VerificationResult `json:"results"`
}

// runVerification resolves root's available tools, classifies each check's
// Command (see "Command classification gate" below), and runs whichever
// ones clear that gate via verify.Run — see that function's doc comment for
// the tool-availability/network-capability gating and "sh -c" execution it
// performs. Exactly one provenance.VerificationResult is produced per input
// CheckSpecIn, in the same order, whether a check ran, was gated, or was
// unavailable/skipped by verify.Run itself — mirroring verify.Run's own
// "never fewer than input" guarantee.
//
// # Checks[i].Command is executed verbatim, but not unconditionally — trust
// boundary and command classification gate
//
// verify.Run executes Command via "sh -c" with no sanitization of its own;
// this is not new here, verify.CheckSpec has always worked this way for its
// existing callers (verify.FullGates, review.Checks). Unlike verify.PlanFor,
// which only ever builds a Command from this repository's own trusted rule
// table, run_verification lets an MCP caller supply Command directly — so
// before handing any check to verify.Run, runVerification classifies its
// Command with discovery.ClassifyCommand (the same fail-safe classifier
// ADR-0032's command-classification model already defines). A Command that
// classifies as provenance.ClassDestructive or provenance.ClassApprovalRequired
// is never executed: it is reported as provenance.StatusApprovalRequired
// instead, with ClassifyCommand's own Reason explaining why. This is not a
// new approval/auth mechanism (no grant, no token, nothing stateful) — it
// only refuses to run what the repository's existing, already-built
// classifier already flags as unsafe to run unattended, closing the gap a
// caller-supplied Command would otherwise leave in this package's "nothing
// here can mutate the target repository" guarantee (see mcpserver.go's
// package doc). A Command classifying as safe/mutating/networked still runs
// verbatim, exactly as documented: this tool remains intended for Command
// values that originated from this repository's own
// recommend_verification/review_diff output or its documented gate list,
// not arbitrary caller-authored shell — the classifier is defense in depth,
// not a substitute for that expectation. See
// docs/planning/agent-mcp-server-guide.md's safety section.
func runVerification(ctx context.Context, root string, checksIn []CheckSpecIn, allowNetwork bool) (RunVerificationOut, error) {
	tools, err := resolveTools(root)
	if err != nil {
		return RunVerificationOut{}, err
	}

	results := make([]provenance.VerificationResult, len(checksIn))
	var runnable []verify.CheckSpec
	var runnableIdx []int
	for i, c := range checksIn {
		class, reason := discovery.ClassifyCommand(c.Command)
		if class == provenance.ClassDestructive || class == provenance.ClassApprovalRequired {
			results[i] = provenance.VerificationResult{
				Name:     c.Name,
				Category: c.Category,
				Status:   provenance.StatusApprovalRequired,
				Reason:   reason,
			}
			continue
		}
		runnable = append(runnable, toCheckSpec(c))
		runnableIdx = append(runnableIdx, i)
	}

	for i, r := range verify.Run(ctx, runnable, tools, allowNetwork) {
		results[runnableIdx[i]] = r
	}

	return RunVerificationOut{Results: results}, nil
}

func runVerificationHandler(ctx context.Context, _ *mcp.CallToolRequest, in RunVerificationIn) (*mcp.CallToolResult, RunVerificationOut, error) {
	out, err := runVerification(ctx, in.Root, in.Checks, in.AllowNetwork)
	return nil, out, err
}

package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/approval"
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

func toCheckSpec(in CheckSpecIn, dir string) verify.CheckSpec {
	return verify.CheckSpec{
		Name:         in.Name,
		Command:      in.Command,
		Category:     in.Category,
		Reason:       in.Reason,
		RequiredTool: in.RequiredTool,
		Networked:    in.Networked,
		Dir:          dir,
	}
}

// RunVerificationIn is run_verification's input.
type RunVerificationIn struct {
	// Root is used to detect which tools (go, git, golangci-lint, ...) are
	// available on PATH (gating each Checks[i].RequiredTool) and as the
	// working directory every runnable check's Command executes in (via
	// verify.CheckSpec.Dir). Defaults to "." if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root: used both to detect available tools and as the working directory checks run in; defaults to \".\" if empty"`
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
	// ApprovalStatus, keyed by check Name, is set only for a check whose
	// Results entry is StatusApprovalRequired: it reports whether an
	// approval grant already exists for that check's Scope, read from
	// root's approval.FileStore (approval.DefaultStorePath) via the
	// non-consuming Broker.DryRunCheck. This tool still never runs a
	// blocked check; it only reports whether a human has already approved
	// it elsewhere — see `modulex agent approve` (tools/agentcli) for how
	// a grant gets created.
	ApprovalStatus map[string]provenance.Status `json:"approval_status,omitempty"`
}

// runVerification resolves root's available tools, classifies each check's
// Command via discovery.ClassifyCommand, and runs whichever ones clear
// that gate via verify.Run (each CheckSpec.Dir set to root, so Command
// actually executes there). A Command classified Mutating, Destructive, or
// ApprovalRequired is never executed — reported StatusApprovalRequired
// instead, with ClassifyCommand's Reason. Mutating is blocked alongside
// the other two because this package's whole premise is that nothing here
// can mutate the target repository, and a Mutating command (e.g. `git
// commit`, `make fmt`) is, definitionally, unsafe for that. Only Safe and
// Networked commands run, the latter still gated by allowNetwork.
//
// For every blocked check, ApprovalStatus[name] additionally reports
// whether an approval already exists for it, via a fresh
// approval.FileStore.Load() from root's approvals file
// (approval.DefaultStorePath) and a non-consuming Broker.DryRunCheck —
// loaded fresh on every call, not cached, so a grant created by a separate
// `modulex agent approve` process after this server started is still
// seen. A Load failure (e.g. a corrupt approvals file) falls back to an
// empty Broker rather than failing the whole call — approval status is an
// enrichment, not a hard dependency of this tool's core result. This does
// not add an execution path; see mcpserver.go's package doc for why a
// non-consuming dry-run check does not compromise this package's
// read-only guarantee.
//
// This tool remains intended for Command values that originated from this
// repository's own recommend_verification/review_diff output or its
// documented gate list, not arbitrary caller-authored shell — the
// classifier is defense in depth, not a substitute for that expectation.
func runVerification(ctx context.Context, root string, checksIn []CheckSpecIn, allowNetwork bool) (RunVerificationOut, error) {
	tools, err := resolveTools(root)
	if err != nil {
		return RunVerificationOut{}, err
	}
	resolvedRoot := resolveRoot(root)

	broker, err := approval.NewFileStore(approval.DefaultStorePath(resolvedRoot)).Load()
	if err != nil {
		broker = approval.NewBroker()
	}

	results := make([]provenance.VerificationResult, len(checksIn))
	var approvalStatus map[string]provenance.Status
	var runnable []verify.CheckSpec
	var runnableIdx []int
	for i, c := range checksIn {
		class, reason := discovery.ClassifyCommand(c.Command)
		if class == provenance.ClassMutating || class == provenance.ClassDestructive || class == provenance.ClassApprovalRequired {
			results[i] = provenance.VerificationResult{
				Name:     c.Name,
				Category: c.Category,
				Status:   provenance.StatusApprovalRequired,
				Reason:   reason,
			}
			if approvalStatus == nil {
				approvalStatus = make(map[string]provenance.Status, len(checksIn))
			}
			approvalStatus[c.Name] = broker.DryRunCheck(approval.Scope{Action: c.Name})
			continue
		}
		runnable = append(runnable, toCheckSpec(c, resolvedRoot))
		runnableIdx = append(runnableIdx, i)
	}

	for i, r := range verify.Run(ctx, runnable, tools, allowNetwork) {
		results[runnableIdx[i]] = r
	}

	return RunVerificationOut{Results: results, ApprovalStatus: approvalStatus}, nil
}

func runVerificationHandler(ctx context.Context, _ *mcp.CallToolRequest, in RunVerificationIn) (*mcp.CallToolResult, RunVerificationOut, error) {
	out, err := runVerification(ctx, in.Root, in.Checks, in.AllowNetwork)
	return nil, out, err
}

package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/agentreview"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

// CreateHandoffIn is create_handoff's input.
type CreateHandoffIn struct {
	// Root is the repository root the handoff describes. Defaults to "."
	// if empty.
	Root string `json:"root,omitempty" jsonschema:"repository root; defaults to \".\" if empty"`
	// AgentName identifies the calling agent (e.g. "claude"), recorded in
	// the envelope's Agent.Name.
	AgentName string `json:"agent_name" jsonschema:"identifies the calling agent, e.g. \"claude\""`
	// Verification carries results from prior run_verification/review_diff
	// calls earlier in this conversation — create_handoff does not
	// generate any of its own; the server is stateless across calls.
	Verification []provenance.VerificationResult `json:"verification,omitempty" jsonschema:"results from prior run_verification/review_diff calls in this conversation"`
}

// CreateHandoffOut is create_handoff's output.
type CreateHandoffOut struct {
	Envelope provenance.Envelope `json:"envelope"`
}

// buildHandoffEnvelope assembles a provenance.Envelope from root's current
// repository state and the caller-supplied verification results, delegating
// the assembly (git rev-parse for Commit/Branch, Redact, and Validate) to
// agentreview.Envelope — the same code the CLI's `modulex agent handoff`
// calls, so both produce an identical envelope (Jira MOD-76). The agent tool
// is recorded as "mcp". ctx is honored for cancellation by the underlying git
// subprocess calls.
//
// A Validate failure (e.g. root is not a git repository, so Commit is empty,
// or a supplied VerificationResult has StatusSkipped with an empty Reason) is
// returned as a real error: there is no useful degraded output for an invalid
// handoff, matching tools/provenanceci's BuildEnvelope convention of treating
// Validate failure as an error, not a soft field.
func buildHandoffEnvelope(ctx context.Context, root, agentName string, verification []provenance.VerificationResult) (provenance.Envelope, error) {
	resolvedRoot := resolveRoot(root)

	repo, err := discovery.Discover(resolvedRoot)
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: discover %q: %w", resolvedRoot, err)
	}

	env, err := agentreview.Envelope(ctx, repo, agentName, "mcp", verification)
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: %w", err)
	}
	return env, nil
}

func createHandoffHandler(ctx context.Context, _ *mcp.CallToolRequest, in CreateHandoffIn) (*mcp.CallToolResult, CreateHandoffOut, error) {
	env, err := buildHandoffEnvelope(ctx, in.Root, in.AgentName, in.Verification)
	if err != nil {
		return nil, CreateHandoffOut{}, err
	}
	return nil, CreateHandoffOut{Envelope: env}, nil
}

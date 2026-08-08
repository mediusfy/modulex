package mcpserver

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
// repository state (discovery.Discover for Root/Dirty, plus fresh git
// rev-parse calls for Commit/Branch — discovery.Discover does not surface
// those) and the caller-supplied verification results, then Redacts and
// Validates it before returning. ctx is honored for cancellation by the
// git rev-parse subprocess calls (see gitRevParse).
//
// A Validate failure (e.g. root is not a git repository, so Commit is
// empty, or a supplied VerificationResult has StatusSkipped with an empty
// Reason) is returned as a real error: there is no useful degraded output
// for an invalid handoff, matching tools/provenanceci's BuildEnvelope
// convention of treating Validate failure as an error, not a soft field.
func buildHandoffEnvelope(ctx context.Context, root, agentName string, verification []provenance.VerificationResult) (provenance.Envelope, error) {
	resolvedRoot := resolveRoot(root)

	repo, err := discovery.Discover(resolvedRoot)
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: discover %q: %w", resolvedRoot, err)
	}

	commit, err := gitRevParse(ctx, resolvedRoot, "HEAD")
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: resolve commit: %w", err)
	}
	branch, err := gitRevParse(ctx, resolvedRoot, "--abbrev-ref", "HEAD")
	if err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: resolve branch: %w", err)
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
			Tool: "mcp",
		},
		Verification: verification,
		CreatedAt:    time.Now().UTC(),
	}

	env.Redact()
	if err := env.Validate(); err != nil {
		return provenance.Envelope{}, fmt.Errorf("create_handoff: %s", strings.Join(unwrapErrors(err), "; "))
	}
	return env, nil
}

// gitRevParse runs `git -C root rev-parse args...` and returns its trimmed
// output. Uses an argv slice (exec.CommandContext), never a shell, so
// root/args cannot inject shell syntax regardless of content, and honors
// ctx for cancellation — unlike discovery's git-dirty check or
// tools/provenanceci's isDirty helper, which both deliberately swallow a
// git failure to a default value (Dirty being a soft, best-effort signal in
// both), gitRevParse propagates a real error on failure: Commit/Branch are
// required Envelope fields (see buildHandoffEnvelope), so a git failure
// here should surface as a clear "resolve commit"/"resolve branch" error
// rather than silently degrading into an envelope that only fails later, at
// Validate, with a less informative cause.
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

func createHandoffHandler(ctx context.Context, _ *mcp.CallToolRequest, in CreateHandoffIn) (*mcp.CallToolResult, CreateHandoffOut, error) {
	env, err := buildHandoffEnvelope(ctx, in.Root, in.AgentName, in.Verification)
	if err != nil {
		return nil, CreateHandoffOut{}, err
	}
	return nil, CreateHandoffOut{Envelope: env}, nil
}

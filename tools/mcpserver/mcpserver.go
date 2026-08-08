// Package mcpserver implements the read-only half of ADR-0032's
// ("Agent-First Development Experience") "MCP boundary", Jira MOD-68:
//
//	The initial MCP server is read-only and should expose stable operations
//	such as: discover projects; read the repository contract; inspect the
//	module graph; find affected modules; recommend verification; run
//	declared verification; review a diff; create a handoff.
//
//	Write-capable tools are separate, disabled by default, and require an
//	explicit approval token or human confirmation. The MCP server must call
//	the same domain and CLI APIs rather than implementing a second source
//	of repository logic.
//
// Every tool here is a thin adapter over an existing leaf package —
// discovery.Discover, contract.Contract/Validate, verify.PlanFor/Run,
// review.Review, and provenance.Envelope — with no new domain logic. See
// docs/planning/agent-mcp-server-guide.md for the full tool reference and a
// worked example.
//
// mcpserver is a separate, nested Go module (like tools/modboundary,
// tools/scaffold, and tools/provenanceci), so the MCP SDK dependency
// (github.com/modelcontextprotocol/go-sdk) never enters the root module's
// build graph. It depends on the root module the same way
// tools/provenanceci does, via a local replace directive.
//
// # No write-capable tools
//
// Nothing in this package can mutate the target repository, run git commands
// beyond read-only ones (status/diff/rev-parse), grant or consume an
// approval, or apply a patch. A future ticket may add a separate, explicitly
// write-capable tool set; this package intentionally does not.
//
// run_verification does consult an approval.FileStore for a blocked check,
// but only via the non-consuming Broker.DryRunCheck (see approval's guide's
// "Dry runs" section) — reporting whether a grant already exists, never
// granting or consuming one. A grant can only be created by
// `modulex agent approve` (tools/agentcli), a separate CLI process; nothing
// in this package can call Broker.Grant, so this remains read-only in the
// same sense run_verification's own subprocess execution is: reporting on
// state, not changing it.
//
// # "Read-only" does not mean "never runs a subprocess"
//
// run_verification (see verification.go) and review_diff (see review.go)
// both execute this repository's own declared build/test/lint commands —
// exactly what ADR-0032 lists as part of the read-only server's surface
// ("run declared verification"). "Read-only" here means "no tool writes to
// the repository or mutates external state," not "no tool ever spawns a
// process." See run_verification's doc comment in verification.go for the
// specific trust boundary this implies for its Checks[i].Command field.
package mcpserver

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mediusfy/modulex/discovery"
)

// serverName and serverVersion identify this MCP server to a connecting
// client (mcp.Implementation.Name/Version), surfaced in MCP client UIs and
// logs.
const serverName = "modulex"

// NewServer builds the read-only Modulex MCP server, registering all six
// tools (discover_repository, read_contract, recommend_verification,
// run_verification, review_diff, create_handoff). Run it with
// server.Run(ctx, &mcp.StdioTransport{}) (see cmd/mcpserver/main.go), or
// connect it directly for testing (see mcpserver_test.go).
func NewServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "discover_repository",
		Description: "Scan a repository root and report its Go modules, composition roots, instruction files, Make targets, CI workflows, semantic indexes, available tools, and git dirty-worktree state.",
	}, discoverRepositoryHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_contract",
		Description: "Read and validate a repository's modulex.agent.yaml contract, if present. Absence of a contract file is reported as present=false, not an error.",
	}, readContractHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "recommend_verification",
		Description: "Map a list of changed file paths to recommended focused checks, plus this repository's always-required full gate list.",
	}, recommendVerificationHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "run_verification",
		Description: "Run a list of verification checks (e.g. from recommend_verification or review_diff) and report each one's pass/fail/skipped/unavailable/approval-required status. Each check runs with root as its working directory. Each Command is classified via discovery.ClassifyCommand before running; a mutating, destructive, or approval-required command is never executed and is reported approval-required, with approval_status reporting (non-consuming, read from root's approvals file) whether it has already been approved via `modulex agent approve` — see docs/planning/agent-mcp-server-guide.md for the trust boundary this implies.",
	}, runVerificationHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "review_diff",
		Description: "Review the diff between two git refs for boundary violations, secret-shaped values, API compatibility changes, and missing changelog entries.",
	}, reviewDiffHandler)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_handoff",
		Description: "Assemble a provenance handoff envelope from repository state and a list of prior verification results (e.g. from run_verification/review_diff calls earlier in this conversation).",
	}, createHandoffHandler)

	return s
}

// resolveRoot returns root if non-empty, otherwise ".": every tool that
// takes a repository root defaults an empty input to "." explicitly rather
// than leaving it empty (discovery.Discover treats "" as any other
// filepath.Abs input, which resolves to the process's actual working
// directory — an implicit, undocumented default this package avoids by
// making the default explicit here instead).
func resolveRoot(root string) string {
	if root == "" {
		return "."
	}
	return root
}

// resolveTools runs discovery.Discover(root) and returns its Tools field,
// the []discovery.ToolStatus that verify.Run and review.Review use to gate
// a CheckSpec.RequiredTool. Shared by run_verification and review_diff so
// both tools resolve tool availability identically.
func resolveTools(root string) ([]discovery.ToolStatus, error) {
	resolvedRoot := resolveRoot(root)
	repo, err := discovery.Discover(resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: discover %q: %w", resolvedRoot, err)
	}
	return repo.Tools, nil
}

// unwrapErrors flattens an error tree built by errors.Join (as
// contract.Contract.Validate and provenance.Envelope.Validate both return)
// into one string per leaf error, for a caller that wants per-error detail
// rather than one opaque multi-line message. errors.Join's result
// implements Unwrap() []error (Go 1.20+); falling back to a single-element
// slice for any other error shape keeps this safe to call on an arbitrary
// error, not just one from errors.Join. Shared by read_contract and
// create_handoff.
func unwrapErrors(err error) []string {
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

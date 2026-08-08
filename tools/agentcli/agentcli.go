// Package agentcli implements the `modulex agent` CLI's domain logic —
// thin wrappers over the same leaf packages (contract, agentdocs, ...)
// tools/mcpserver already calls, per ADR-0032's "the MCP server must call
// the same domain and CLI APIs rather than implementing a second source of
// repository logic." cmd/modulex is the actual binary; this package holds
// the logic so it stays testable without exec-ing a built binary.
package agentcli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/agentdocs"
	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/contract"
)

// ContractFileName is the well-known repository contract file name, per
// docs/planning/agent-repository-contract-guide.md. Mirrors
// tools/mcpserver's unexported contractFileName constant of the same
// value.
const ContractFileName = "modulex.agent.yaml"

// LoadContract reads and unmarshals <root>/ContractFileName, then validates
// it. Unlike tools/mcpserver's readContract, LoadContract has no
// "not present" tri-state: `modulex agent generate` has nothing useful to
// do without a contract, so a missing or invalid file is a plain error,
// not a normal outcome for this CLI to model in its output.
func LoadContract(root string) (contract.Contract, error) {
	path := filepath.Join(root, ContractFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		return contract.Contract{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var c contract.Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return contract.Contract{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return contract.Contract{}, fmt.Errorf("%s failed validation: %w", path, err)
	}
	return c, nil
}

// Approve grants an approval for action (and, if non-empty, resource),
// attributed to approvedBy, valid for ttl, in root's approval.FileStore
// (approval.DefaultStorePath) — the same store tools/mcpserver's
// run_verification reads via the non-consuming Broker.DryRunCheck. This is
// the only supported way to make a grant visible to a separately-running
// MCP server process: `modulex agent approve` and the MCP server are
// different processes with no shared memory, so the grant only bridges
// between them via this file. See the Agent Approval Broker Guide's
// "Wired for visibility, not yet for granting" section.
//
// The returned Grant carries the raw Token — the caller (runApprove) is
// responsible for treating it as sensitive, per approval's "Token
// sensitivity" section: this function itself never logs or persists it
// unredacted (FileStore.Save only ever writes it to root's own approvals
// file, whose whole purpose is holding exactly this).
func Approve(root, action, resource, approvedBy string, ttl time.Duration) (approval.Grant, error) {
	store := approval.NewFileStore(approval.DefaultStorePath(root))

	b, err := store.Load()
	if err != nil {
		return approval.Grant{}, fmt.Errorf("loading approvals: %w", err)
	}

	grant, err := b.Grant(approval.Scope{Action: action, Resource: resource}, approvedBy, ttl)
	if err != nil {
		return approval.Grant{}, err
	}

	if err := store.Save(b); err != nil {
		return approval.Grant{}, fmt.Errorf("saving approvals: %w", err)
	}
	return grant, nil
}

// outputFile pairs the repository-root file name `modulex agent generate`
// writes with the agentdocs.Target that produces its contract-derived
// content.
type outputFile struct {
	name   string
	target agentdocs.Target
}

// OutputFiles is the fixed list of files `modulex agent generate` writes,
// in a stable order. Exported so a caller (the CLI's -target flag handling,
// or a test) can iterate it rather than hardcoding file names.
var OutputFiles = []outputFile{
	{name: "AGENTS.md", target: agentdocs.TargetAGENTS},
	{name: "CLAUDE.md", target: agentdocs.TargetCLAUDE},
}

// GeneratedFiles renders every entry in OutputFiles from c, returning a map
// of file name to full content: agentdocs.Generate's contract-derived
// output followed by toolingAddendum (see its doc comment for why that
// part is static rather than contract-driven). Deterministic for the same
// c, since agentdocs.Generate is and toolingAddendum is a constant.
func GeneratedFiles(c contract.Contract) (map[string]string, error) {
	out := make(map[string]string, len(OutputFiles))
	for _, f := range OutputFiles {
		body, err := agentdocs.Generate(c, f.target)
		if err != nil {
			return nil, fmt.Errorf("generating %s: %w", f.name, err)
		}
		out[f.name] = body + "\n" + toolingAddendumHeader + "\n\n" + toolingAddendum + "\n"
	}
	return out, nil
}

// WriteGeneratedFiles calls GeneratedFiles(c) and writes each result to
// <root>/<name> with mode 0644, overwriting any existing file. Returns the
// list of file names written, in OutputFiles order.
func WriteGeneratedFiles(root string, c contract.Contract) ([]string, error) {
	files, err := GeneratedFiles(c)
	if err != nil {
		return nil, err
	}

	written := make([]string, 0, len(OutputFiles))
	for _, f := range OutputFiles {
		path := filepath.Join(root, f.name)
		if err := os.WriteFile(path, []byte(files[f.name]), 0o644); err != nil {
			return written, fmt.Errorf("writing %s: %w", path, err)
		}
		written = append(written, f.name)
	}
	return written, nil
}

// toolingAddendum covers repository tooling that has no corresponding
// contract.Contract field — CodeGraph usage and the per-agent hook setup
// documented in AGENTS.md before this package existed (see
// scripts/install-codegraph-hooks.sh, added alongside the contract this
// package now generates from). agentdocs.Generate's output already covers
// everything contract.Contract *does* carry (commands, verification,
// protected paths, credentials, handoff format); this section is appended
// after it rather than folded into agentdocs itself, which stays
// schema-pure per its own package doc comment ("Does not touch the
// filesystem" / no field for "how to configure CodeGraph").
//
// Identical for every agentdocs.Target: CodeGraph and its per-provider hook
// story apply the same way regardless of which file a reader opened it
// from, matching how AGENTS.md's original "Agent-specific hooks" section
// already named Kimi, Claude Code, and Antigravity side by side rather than
// splitting the content by target file.
const toolingAddendumHeader = "<!-- The section below is static, not generated from modulex.agent.yaml — " +
	"it documents tooling with no contract.Contract field (see " +
	"tools/agentcli/agentcli.go's toolingAddendum). Edit it there, not here. -->"

const toolingAddendum = `## CodeGraph

This project uses CodeGraph (` + "`.codegraph/codegraph.db`" + `) as the source of truth for code navigation. Before starting work on this project or beginning a new turn, run:

` + "```bash\ncodegraph sync\n```" + `

When investigating code, prefer querying CodeGraph over raw ` + "`grep`/`find`" + `. Useful queries:

` + "```bash" + `
# Find symbols by name
codegraph query "Manager"

# Find definitions in a file
codegraph query --file modulex.go "StartModules"

# Show index status
codegraph status
` + "```" + `

### Keeping CodeGraph in sync

Git hooks are installed under ` + "`.git/hooks`" + ` to run ` + "`codegraph sync`" + ` automatically on commit, checkout, merge, and rewrite. To install them in a fresh clone:

` + "```bash\n./scripts/install-codegraph-hooks.sh\n```" + `

#### Agent-specific hooks

- **Kimi Code CLI**: hooks are configured in ` + "`~/.kimi-code/config.toml`" + `. The project-specific hook at ` + "`~/.kimi-code/hooks/codegraph-sync.sh`" + ` runs ` + "`codegraph sync`" + ` on ` + "`SessionStart`" + ` and ` + "`UserPromptSubmit`" + ` when the session cwd is this repo.
- **Claude Code**: run ` + "`codegraph install`" + ` and choose global or local installation to enable native Claude Code hook integration.
- **Antigravity / ` + "`agy`" + `**: does not expose a pre-turn hook mechanism. Rely on the git hooks above and this rule.

All agents (Kimi, Claude, Antigravity) must use CodeGraph for locating symbols, call sites, and references.`

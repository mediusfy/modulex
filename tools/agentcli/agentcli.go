// Package agentcli implements the `modulex agent` CLI's domain logic —
// thin wrappers over the same leaf packages (contract, agentdocs, ...)
// tools/mcpserver already calls, per ADR-0032's "the MCP server must call
// the same domain and CLI APIs rather than implementing a second source of
// repository logic." cmd/modulex is the actual binary; this package holds
// the logic so it stays testable without exec-ing a built binary.
package agentcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mediusfy/modulex/agentdocs"
	"github.com/mediusfy/modulex/agentreview"
	"github.com/mediusfy/modulex/approval"
	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/review"
	"github.com/mediusfy/modulex/verify"
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

// discoverRepo is the repository-discovery seam Review and Handoff use to
// resolve the working tree's root and available tools. It is a package var so
// tests can inject a fixture without a real filesystem/PATH scan; production
// always uses discovery.Discover.
var discoverRepo = discovery.Discover

// loadProtectedPaths reads <root>/modulex.agent.yaml and returns its declared
// ProtectedPaths, or nil if the file is absent. Unlike LoadContract it does
// NOT validate the contract: a contract that fails validation for some
// unrelated reason still declares protected paths that must be enforced,
// matching tools/mcpserver's review_diff ("invalid isn't the same as
// absent"). A present-but-unparseable file is a real error.
func loadProtectedPaths(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ContractFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ContractFileName, err)
	}
	var c contract.Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", ContractFileName, err)
	}
	return c.ProtectedPaths, nil
}

// reviewRepo is the single prologue behind Review and Handoff: discover root,
// resolve its protected paths, and run the shared agentreview.Review over the
// baseRef...headRef diff. Keeping it in one place means the two subcommands
// cannot drift apart in how they discover the repo or load the contract —
// the same by-construction guarantee MOD-76 demands between the CLI and MCP
// adapters, applied within the CLI itself.
func reviewRepo(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) (discovery.Repository, []provenance.VerificationResult, error) {
	repo, err := discoverRepo(root)
	if err != nil {
		return discovery.Repository{}, nil, fmt.Errorf("discovering %q: %w", root, err)
	}
	protectedPaths, err := loadProtectedPaths(root)
	if err != nil {
		return discovery.Repository{}, nil, err
	}
	return repo, agentreview.Review(ctx, repo, baseRef, headRef, allowNetwork, protectedPaths), nil
}

// Review runs the repository's review checks (boundary, compatibility,
// changelog, secret scan, and protected-paths) over the baseRef...headRef diff
// and returns one provenance.VerificationResult per check, in a stable order.
// It discovers the repo and resolves protected paths here (the adapter's job),
// then delegates the actual check run to agentreview.Review — the same code
// tools/mcpserver's review_diff calls, so the CLI and MCP results are identical
// by construction (Jira MOD-76). It never mutates the repository.
func Review(ctx context.Context, root, baseRef, headRef string, allowNetwork bool) ([]provenance.VerificationResult, error) {
	_, results, err := reviewRepo(ctx, root, baseRef, headRef, allowNetwork)
	return results, err
}

// Handoff runs the same review as Review and assembles a redacted, validated
// provenance.Envelope describing the change. Envelope assembly is delegated to
// agentreview.Envelope — the same code tools/mcpserver's create_handoff calls
// — recording the agent tool as "modulex-cli". The returned Envelope has
// already been Redacted and passed Validate; a Validate failure (e.g. root is
// not a git repository, so Commit is empty) is returned as an error, not a
// partial envelope.
func Handoff(ctx context.Context, root, agentName, baseRef, headRef string, allowNetwork bool) (provenance.Envelope, error) {
	repo, results, err := reviewRepo(ctx, root, baseRef, headRef, allowNetwork)
	if err != nil {
		return provenance.Envelope{}, err
	}
	return agentreview.Envelope(ctx, repo, agentName, "modulex-cli", results)
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

// CheckGeneratedFiles reports which of OutputFiles at root have drifted from
// what GeneratedFiles(c) would render right now — `modulex agent generate
// -check`'s domain logic. A missing file counts as drifted; any other read
// error (permission denied, the path is a directory) is returned as an
// error rather than misreported as drift, since "regenerate" cannot fix it.
// An empty result means the checked-in files are exactly what generate
// would write, so CI can gate on contract/instructions drift without
// mutating the tree.
func CheckGeneratedFiles(root string, c contract.Contract) ([]string, error) {
	want, err := GeneratedFiles(c)
	if err != nil {
		return nil, err
	}
	var drifted []string
	for _, f := range OutputFiles {
		got, err := os.ReadFile(filepath.Join(root, f.name))
		if errors.Is(err, os.ErrNotExist) {
			drifted = append(drifted, f.name)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.name, err)
		}
		if string(got) != want[f.name] {
			drifted = append(drifted, f.name)
		}
	}
	return drifted, nil
}

// Verify plans and runs verification checks for the baseRef...headRef diff:
// changed files feed verify.PlanFor, whose focused checks run (plus the
// repository's full gates when full is true, deduplicated by name — an
// unmapped path's focused fallback IS the full gate set). Commands are
// classified through discovery.ClassifyCommand with exactly
// tools/mcpserver run_verification's posture: a mutating, destructive, or
// approval-required command is never executed and reports
// StatusApprovalRequired instead; when a grant for it already exists in
// root's approvals file, the Reason says so (this CLI still never consumes
// a grant — execution stays a human decision). Like run_verification, the
// grant probe matches only a grant whose Action equals the check's plan
// name exactly with no Resource (approval.Grant matching is exact on both
// fields), so a resource-scoped grant or one keyed on a contract command
// name is not surfaced by the note. Safe/networked checks run via
// verify.Run with the discovered tool set, networked ones gated by
// allowNetwork.
func Verify(ctx context.Context, root, baseRef, headRef string, allowNetwork, full bool) ([]provenance.VerificationResult, error) {
	repo, err := discoverRepo(root)
	if err != nil {
		return nil, fmt.Errorf("discovering %q: %w", root, err)
	}
	changed, err := review.ChangedFiles(ctx, repo.Root, baseRef, headRef)
	if err != nil {
		return nil, fmt.Errorf("listing changed files %s...%s: %w", baseRef, headRef, err)
	}

	plan := verify.PlanFor(changed)
	specs := plan.FocusedChecks
	if full {
		seen := make(map[string]bool, len(specs))
		for _, s := range specs {
			seen[s.Name] = true
		}
		for _, s := range plan.FullGates {
			if !seen[s.Name] {
				specs = append(specs, s)
			}
		}
	}

	broker, err := approval.NewFileStore(approval.DefaultStorePath(repo.Root)).Load()
	if err != nil {
		// Approval status is an enrichment, not a hard dependency —
		// mirroring run_verification's fallback for a corrupt store.
		broker = approval.NewBroker()
	}

	results := make([]provenance.VerificationResult, len(specs))
	var runnable []verify.CheckSpec
	var runnableIdx []int
	for i, s := range specs {
		class, reason := discovery.ClassifyCommand(s.Command)
		if class == provenance.ClassMutating || class == provenance.ClassDestructive || class == provenance.ClassApprovalRequired {
			r := provenance.VerificationResult{
				Name:     s.Name,
				Category: s.Category,
				Status:   provenance.StatusApprovalRequired,
				Reason:   reason,
			}
			if broker.DryRunCheck(approval.Scope{Action: s.Name}) == provenance.StatusPass {
				r.Reason += "; an approval grant exists for this check"
			}
			results[i] = r
			continue
		}
		if s.Dir == "" {
			s.Dir = repo.Root
		}
		runnable = append(runnable, s)
		runnableIdx = append(runnableIdx, i)
	}
	for i, r := range verify.Run(ctx, runnable, repo.Tools, allowNetwork) {
		results[runnableIdx[i]] = r
	}
	return results, nil
}

// DoctorReport is Doctor's diagnosis of one repository: what discovery
// found, whether the contract is present/parseable/valid, and the headline
// counts an agent (or human) needs to know before working here.
type DoctorReport struct {
	Root      string                 `json:"root"`
	IsGitRepo bool                   `json:"is_git_repo"`
	Dirty     bool                   `json:"dirty"`
	Tools     []discovery.ToolStatus `json:"tools"`
	// ContractPresent is false when root has no modulex.agent.yaml — a
	// normal state, not an error.
	ContractPresent bool `json:"contract_present"`
	// ContractError holds the parse or validation failure for a present
	// contract, empty when absent or fully valid.
	ContractError  string `json:"contract_error,omitempty"`
	Projects       int    `json:"projects"`
	Commands       int    `json:"commands"`
	ProtectedPaths int    `json:"protected_paths"`
}

// Doctor diagnoses root: discovery plus contract state, in one report.
// Unlike LoadContract it never fails on a bad contract — a doctor's whole
// job is reporting what is wrong — so only discovery failure or an I/O
// error other than absence returns a non-nil error.
func Doctor(root string) (DoctorReport, error) {
	repo, err := discoverRepo(root)
	if err != nil {
		return DoctorReport{}, fmt.Errorf("discovering %q: %w", root, err)
	}
	rep := DoctorReport{
		Root:      repo.Root,
		IsGitRepo: repo.IsGitRepo,
		Dirty:     repo.Dirty,
		Tools:     repo.Tools,
	}

	data, err := os.ReadFile(filepath.Join(root, ContractFileName))
	if errors.Is(err, os.ErrNotExist) {
		return rep, nil
	}
	if err != nil {
		return DoctorReport{}, fmt.Errorf("reading %s: %w", ContractFileName, err)
	}
	rep.ContractPresent = true

	var c contract.Contract
	if err := yaml.Unmarshal(data, &c); err != nil {
		rep.ContractError = fmt.Sprintf("parse: %v", err)
		return rep, nil
	}
	rep.Projects = len(c.Projects)
	rep.Commands = len(c.Commands)
	rep.ProtectedPaths = len(c.ProtectedPaths)
	if err := c.Validate(); err != nil {
		rep.ContractError = fmt.Sprintf("validation: %v", err)
	}
	return rep, nil
}

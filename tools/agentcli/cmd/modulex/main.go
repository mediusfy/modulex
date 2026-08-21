// Command modulex is the unified modulex CLI: one shell-invokable front door
// to the repo-workflow tooling that previously shipped as separate binaries.
// The `agent` commands expose the same domain logic tools/mcpserver exposes
// over MCP, for an agent or CI step that doesn't speak MCP, per ADR-0032
// (docs/adr/adr-0032-agent-first-development-experience.md). See
// tools/agentcli (the agentcli package) for the logic each agent subcommand
// wraps, and docs/planning/agent-cli-guide.md for the full guide.
//
// Usage:
//
//	modulex agent generate [-root <path>] [-check]
//	modulex agent approve -action <name> [-resource <name>] -approved-by <name> [-ttl <duration>] [-root <path>]
//	modulex agent review -base <ref> [-head <ref>] [-root <path>] [-allow-network]
//	modulex agent handoff -base <ref> [-head <ref>] [-agent <name>] [-root <path>] [-allow-network]
//	modulex agent verify -base <ref> [-head <ref>] [-root <path>] [-allow-network] [-full]
//	modulex doctor [-root <path>] [-json]
//	modulex new module -name <feature> -out <parent-dir> [-module <import-path>] [-force]
//	modulex check boundary [analyzer flags] [packages...]
//
// new module scaffolds a feature module with the recommended
// domain/ports/service/adapters/module.go layout (scaffold.Generate — the
// same code the standalone tools/scaffold binary wraps). check boundary
// runs the modboundary analyzer (the same one `make check-module-boundary`
// drives). The single-purpose tools/scaffold and tools/modboundary binaries
// remain available; tools/provenanceci deliberately stays standalone — it
// is pure CI plumbing with no interactive audience.
//
// review runs the repository's review checks (boundary, compatibility,
// changelog, secret scan, protected paths) over the base...head diff and
// prints the results as JSON. handoff runs the same review and prints a
// redacted, validated provenance.Envelope — the auditable handoff artifact a
// CI step or editor attaches to a PR. Both are read-only and never mutate the
// repository, and both wrap the same agentcli functions tools/mcpserver's
// review_diff and create_handoff expose over MCP.
//
// Exit codes for review and handoff: 0 when the review ran and no check
// failed, 1 when any check reports StatusFail (the JSON is still written to
// stdout first) or the review could not run at all, 2 for usage errors. A CI
// step shelling out to either can therefore key on the exit code alone.
//
// generate reads <root>/modulex.agent.yaml, renders AGENTS.md and CLAUDE.md
// via agentdocs.Generate plus a static tooling addendum (see agentcli.go),
// and writes both at <root>, overwriting any existing copies.
//
// approve grants an approval for action (and, if given, resource) in
// <root>/.modulex/approvals.json (approval.DefaultStorePath) — the same
// file tools/mcpserver's run_verification reads to report approval_status
// for a blocked check. This is a human-run command, not something an agent
// should invoke on its own behalf: an approval is only meaningful if
// granted outside the agent's own tool-calling loop. It never runs,
// unblocks, or executes anything itself — only the MCP server's existing
// classification gate still decides what's blocked; this only records that
// a human has approved a specific, scoped action.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/tools/agentcli"
	"github.com/mediusfy/modulex/tools/modboundary"
	"github.com/mediusfy/modulex/tools/scaffold"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "agent":
		runAgent(os.Args[2:])
	case "new":
		runNew(os.Args[2:])
	case "check":
		runCheck(os.Args[2:])
	case "doctor":
		runDoctor(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "modulex: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func runAgent(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "generate":
		runGenerate(args[1:])
	case "approve":
		runApprove(args[1:])
	case "review":
		runReview(args[1:])
	case "handoff":
		runHandoff(args[1:])
	case "verify":
		runVerify(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "modulex agent: unknown subcommand %q\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: modulex <command> [flags]")
	fmt.Fprintln(os.Stderr, "\nCommands:")
	fmt.Fprintln(os.Stderr, "  agent generate    render AGENTS.md/CLAUDE.md from modulex.agent.yaml")
	fmt.Fprintln(os.Stderr, "  agent approve     grant an approval a separately-running MCP server can see")
	fmt.Fprintln(os.Stderr, "  agent review      run the review checks over a diff and print the results as JSON")
	fmt.Fprintln(os.Stderr, "  agent handoff     run the review and print a validated provenance handoff envelope as JSON")
	fmt.Fprintln(os.Stderr, "  agent verify      plan and run the verification checks a diff recommends")
	fmt.Fprintln(os.Stderr, "  new module        scaffold a feature module (domain/ports/service/adapters/module.go)")
	fmt.Fprintln(os.Stderr, "  check boundary    run the modboundary analyzer against Go packages")
	fmt.Fprintln(os.Stderr, "  doctor            diagnose a repository: discovery, tools, contract state")
}

// runNew handles `modulex new module`, wrapping scaffold.Generate — the same
// code the standalone tools/scaffold binary wraps, which remains available
// for callers that want the single-purpose tool.
func runNew(args []string) {
	if len(args) < 1 || args[0] != "module" {
		fmt.Fprintln(os.Stderr, "Usage: modulex new module -name <feature> -out <parent-dir> [-module <import-path>] [-force]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("modulex new module", flag.ExitOnError)
	name := fs.String("name", "", "feature name, e.g. \"billing\" (required)")
	out := fs.String("out", "", "parent directory to generate into, e.g. \"examples\" (required)")
	module := fs.String("module", "", "Go import path corresponding to -out; auto-detected from the nearest go.mod if omitted")
	force := fs.Bool("force", false, "overwrite the target directory if it already exists and is non-empty")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(2)
	}
	if *name == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "modulex new module: -name and -out are required")
		fs.PrintDefaults()
		os.Exit(2)
	}

	result, err := scaffold.Generate(scaffold.Config{
		Name:         *name,
		OutDir:       *out,
		ModuleImport: *module,
		Force:        *force,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex new module: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("generated %s:\n", result.TargetDir)
	for _, f := range result.Files {
		fmt.Printf("  %s\n", f)
	}
}

// runCheck handles `modulex check boundary`, driving the modboundary
// analyzer through singlechecker — the same analyzer the standalone
// tools/modboundary binary (and `make check-module-boundary`) runs.
// singlechecker owns flag parsing and process exit, so it is handed a
// rewritten os.Args and never returns.
func runCheck(args []string) {
	if len(args) < 1 || args[0] != "boundary" {
		fmt.Fprintln(os.Stderr, "Usage: modulex check boundary [analyzer flags] [packages...]")
		fmt.Fprintln(os.Stderr, "\nAnalyzer flags include -root, -allow, and -dbschema; run with -help for the full set.")
		os.Exit(2)
	}
	os.Args = append([]string{"modulex check boundary"}, args[1:]...)
	singlechecker.Main(modboundary.Analyzer)
}

func runReview(args []string) {
	fs := flag.NewFlagSet("modulex agent review", flag.ExitOnError)
	root := fs.String("root", ".", "repository root to review")
	base := fs.String("base", "", "required: base git ref to diff from, e.g. origin/main")
	head := fs.String("head", "HEAD", "head git ref to diff to")
	allowNetwork := fs.Bool("allow-network", false, "allow networked checks to run")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *base == "" {
		fmt.Fprintln(os.Stderr, "modulex agent review: -base is required")
		fs.PrintDefaults()
		os.Exit(2)
	}

	results, err := agentcli.Review(context.Background(), *root, *base, *head, *allowNetwork)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent review: %v\n", err)
		os.Exit(1)
	}
	if err := printJSON(results); err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent review: %v\n", err)
		os.Exit(1)
	}
	exitOnFailedChecks("modulex agent review", results)
}

func runHandoff(args []string) {
	fs := flag.NewFlagSet("modulex agent handoff", flag.ExitOnError)
	root := fs.String("root", ".", "repository root to describe")
	agent := fs.String("agent", "modulex", "name of the agent producing this handoff")
	base := fs.String("base", "", "required: base git ref to diff from, e.g. origin/main")
	head := fs.String("head", "HEAD", "head git ref to diff to")
	allowNetwork := fs.Bool("allow-network", false, "allow networked checks to run")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *base == "" {
		fmt.Fprintln(os.Stderr, "modulex agent handoff: -base is required")
		fs.PrintDefaults()
		os.Exit(2)
	}

	env, err := agentcli.Handoff(context.Background(), *root, *agent, *base, *head, *allowNetwork)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent handoff: %v\n", err)
		os.Exit(1)
	}
	if err := printJSON(env); err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent handoff: %v\n", err)
		os.Exit(1)
	}
	exitOnFailedChecks("modulex agent handoff", env.Verification)
}

// exitOnFailedChecks exits 1, naming the failed checks on stderr, if any
// verification result has StatusFail. The JSON has already been written to
// stdout at this point, so a CI step shelling out to review/handoff gets both
// the machine-readable results and a red exit code — without this, a diff
// containing a detected secret or a protected-path edit would exit 0 and the
// calling workflow would go green on a failing review.
func exitOnFailedChecks(prefix string, results []provenance.VerificationResult) {
	var failed []string
	for _, r := range results {
		if r.Status == provenance.StatusFail {
			failed = append(failed, r.Name)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "%s: %d check(s) failed: %s\n", prefix, len(failed), strings.Join(failed, ", "))
		os.Exit(1)
	}
}

// printJSON writes v as indented JSON to stdout, the machine-readable output
// both review and handoff emit for a CI step or editor to consume.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("modulex agent generate", flag.ExitOnError)
	root := fs.String("root", ".", "repository root containing modulex.agent.yaml")
	check := fs.Bool("check", false, "report drift between the checked-in AGENTS.md/CLAUDE.md and what generate would write, without writing; exits 1 on drift")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	c, err := agentcli.LoadContract(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent generate: %v\n", err)
		os.Exit(1)
	}

	if *check {
		drifted, err := agentcli.CheckGeneratedFiles(*root, c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "modulex agent generate: %v\n", err)
			os.Exit(1)
		}
		if len(drifted) > 0 {
			fmt.Fprintf(os.Stderr, "modulex agent generate: %d file(s) drifted from the contract: %s\nrun `modulex agent generate` to regenerate\n", len(drifted), strings.Join(drifted, ", "))
			os.Exit(1)
		}
		fmt.Println("ok: generated files match the contract")
		return
	}

	written, err := agentcli.WriteGeneratedFiles(*root, c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent generate: %v\n", err)
		os.Exit(1)
	}

	for _, name := range written {
		fmt.Println("wrote", name)
	}
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("modulex agent verify", flag.ExitOnError)
	root := fs.String("root", ".", "repository root to verify")
	base := fs.String("base", "", "required: base git ref to diff from, e.g. origin/main")
	head := fs.String("head", "HEAD", "head git ref to diff to")
	allowNetwork := fs.Bool("allow-network", false, "allow networked checks to run")
	full := fs.Bool("full", false, "also run the repository's full gates, not just the focused checks the diff recommends")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *base == "" {
		fmt.Fprintln(os.Stderr, "modulex agent verify: -base is required")
		fs.PrintDefaults()
		os.Exit(2)
	}

	results, err := agentcli.Verify(context.Background(), *root, *base, *head, *allowNetwork, *full)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent verify: %v\n", err)
		os.Exit(1)
	}
	if err := printJSON(results); err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent verify: %v\n", err)
		os.Exit(1)
	}
	exitOnFailedChecks("modulex agent verify", results)
}

func runDoctor(args []string) {
	fs := flag.NewFlagSet("modulex doctor", flag.ExitOnError)
	root := fs.String("root", ".", "repository root to diagnose")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of text")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	rep, err := agentcli.Doctor(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex doctor: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		if err := printJSON(rep); err != nil {
			fmt.Fprintf(os.Stderr, "modulex doctor: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("root:            %s\n", rep.Root)
		fmt.Printf("git repository:  %v (dirty: %v)\n", rep.IsGitRepo, rep.Dirty)
		fmt.Println("tools:")
		for _, t := range rep.Tools {
			mark := "missing"
			if t.Present {
				mark = t.Path
			}
			fmt.Printf("  %-14s %s\n", t.Name, mark)
		}
		switch {
		case !rep.ContractPresent:
			fmt.Println("contract:        absent (no modulex.agent.yaml — normal for repositories without one)")
		case rep.ContractError != "":
			fmt.Printf("contract:        BROKEN — %s\n", rep.ContractError)
		default:
			fmt.Printf("contract:        valid (%d project(s), %d command(s), %d protected path(s))\n",
				rep.Projects, rep.Commands, rep.ProtectedPaths)
		}
	}

	if rep.ContractError != "" {
		os.Exit(1)
	}
}

func runApprove(args []string) {
	fs := flag.NewFlagSet("modulex agent approve", flag.ExitOnError)
	root := fs.String("root", ".", "repository root the approval applies to")
	action := fs.String("action", "", "required: the check name being approved, e.g. a run_verification check's \"name\"")
	resource := fs.String("resource", "", "optional: further scope the approval to a specific resource")
	approvedBy := fs.String("approved-by", "", "required: who is granting this approval, e.g. your name or email")
	ttl := fs.Duration("ttl", 10*time.Minute, "how long the approval remains valid")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	if *action == "" || *approvedBy == "" {
		fmt.Fprintln(os.Stderr, "modulex agent approve: -action and -approved-by are required")
		fs.PrintDefaults()
		os.Exit(2)
	}

	grant, err := agentcli.Approve(*root, *action, *resource, *approvedBy, *ttl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent approve: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("granted:", grant)
	fmt.Println("token (sensitive — do not share or log this):", grant.Token)
}

// Command modulex is the `modulex agent` CLI: a shell-invokable entry point
// to the same domain logic tools/mcpserver exposes over MCP, for an agent
// or CI step that doesn't speak MCP, per ADR-0032
// (docs/adr/adr-0032-agent-first-development-experience.md). See
// tools/agentcli (the agentcli package) for the logic each subcommand
// wraps, and docs/planning/agent-cli-guide.md for the full guide.
//
// Usage:
//
//	modulex agent generate [-root <path>]
//	modulex agent approve -action <name> [-resource <name>] -approved-by <name> [-ttl <duration>] [-root <path>]
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
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mediusfy/modulex/tools/agentcli"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "agent" {
		usage()
		os.Exit(2)
	}
	if len(os.Args) < 3 {
		usage()
		os.Exit(2)
	}

	switch os.Args[2] {
	case "generate":
		runGenerate(os.Args[3:])
	case "approve":
		runApprove(os.Args[3:])
	default:
		fmt.Fprintf(os.Stderr, "modulex agent: unknown subcommand %q\n\n", os.Args[2])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: modulex agent <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "\nSubcommands:")
	fmt.Fprintln(os.Stderr, "  generate    render AGENTS.md/CLAUDE.md from modulex.agent.yaml")
	fmt.Fprintln(os.Stderr, "  approve     grant an approval a separately-running MCP server can see")
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("modulex agent generate", flag.ExitOnError)
	root := fs.String("root", ".", "repository root containing modulex.agent.yaml")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	c, err := agentcli.LoadContract(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "modulex agent generate: %v\n", err)
		os.Exit(1)
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

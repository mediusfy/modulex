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
//
// generate reads <root>/modulex.agent.yaml, renders AGENTS.md and CLAUDE.md
// via agentdocs.Generate plus a static tooling addendum (see agentcli.go),
// and writes both at <root>, overwriting any existing copies.
package main

import (
	"flag"
	"fmt"
	"os"

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

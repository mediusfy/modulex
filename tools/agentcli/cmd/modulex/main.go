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
//	modulex agent review -base <ref> [-head <ref>] [-root <path>] [-allow-network]
//	modulex agent handoff -base <ref> [-head <ref>] [-agent <name>] [-root <path>] [-allow-network]
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

	"github.com/mediusfy/modulex/provenance"
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
	case "review":
		runReview(os.Args[3:])
	case "handoff":
		runHandoff(os.Args[3:])
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
	fmt.Fprintln(os.Stderr, "  review      run the review checks over a diff and print the results as JSON")
	fmt.Fprintln(os.Stderr, "  handoff     run the review and print a validated provenance handoff envelope as JSON")
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

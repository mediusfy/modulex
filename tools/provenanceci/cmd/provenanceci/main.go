// Command provenanceci builds a provenance.Envelope summarizing one CI run
// and writes it as JSON, for a CI workflow to upload as a build artifact.
// See tools/provenanceci (the provenanceci package) and
// docs/planning/agent-provenance-ci-guide.md.
//
// Usage:
//
//	provenanceci -commit <sha> -branch <name> -out provenance.json \
//	    -job build-and-test=success -job lint=success ...
//
// Each -job value is "name=result", where result is one of GitHub Actions'
// documented `needs.<job>.result` values: success, failure, cancelled, or
// skipped. -job is repeatable; at least one is required.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mediusfy/modulex/tools/provenanceci"
)

// jobFlags implements flag.Value to collect repeated -job name=result
// flags into a []provenanceci.JobResult.
type jobFlags []provenanceci.JobResult

func (j *jobFlags) String() string {
	if j == nil {
		return ""
	}
	parts := make([]string, len(*j))
	for i, r := range *j {
		parts[i] = r.Name + "=" + r.Result
	}
	return strings.Join(parts, ",")
}

func (j *jobFlags) Set(value string) error {
	name, result, ok := strings.Cut(value, "=")
	if !ok || name == "" || result == "" {
		return fmt.Errorf("invalid -job value %q, want name=result", value)
	}
	*j = append(*j, provenanceci.JobResult{Name: name, Result: result})
	return nil
}

func main() {
	commit := flag.String("commit", "", "commit SHA this CI run checked out (required)")
	branch := flag.String("branch", "", "branch or ref name this CI run checked out")
	repoPath := flag.String("repo", ".", "repository path recorded in the envelope")
	out := flag.String("out", "provenance.json", "output file path for the envelope JSON")
	var jobs jobFlags
	flag.Var(&jobs, "job", "a CI job's outcome as name=result (e.g. lint=success); repeatable, at least one required")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -commit <sha> -job name=result [-job name=result ...] [-branch <name>] [-repo <path>] [-out <path>]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *commit == "" {
		fmt.Fprintln(os.Stderr, "error: -commit is required")
		flag.Usage()
		os.Exit(2)
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one -job is required")
		flag.Usage()
		os.Exit(2)
	}

	env, err := provenanceci.BuildEnvelope(provenanceci.Config{
		RepoPath: *repoPath,
		Commit:   *commit,
		Branch:   *branch,
		Dirty:    isDirty(*repoPath),
		Jobs:     jobs,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("provenanceci: marshal envelope: %w", err))
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("provenanceci: write %s: %w", *out, err))
		os.Exit(1)
	}

	fmt.Printf("wrote %s (%d job result(s))\n", *out, len(jobs))
}

// isDirty reports whether `git status --porcelain` in repoPath reports any
// uncommitted changes, treating a git error (e.g. repoPath is not a git
// repository) as "not dirty" rather than failing the whole tool over a
// best-effort signal.
func isDirty(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

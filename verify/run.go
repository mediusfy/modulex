package verify

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/mediusfy/modulex/discovery"
	"github.com/mediusfy/modulex/provenance"
)

// maxOutputBytes bounds how much of a check's combined stdout+stderr is
// retained in a provenance.VerificationResult.Message, so a runaway or
// verbose command (e.g. a failing `go test -v ./...`) cannot make Run's
// result set unboundedly large in memory. When output exceeds this bound,
// only the last maxOutputBytes bytes are kept (the tail is usually where the
// actual failure/panic is) prefixed with a truncation marker.
const maxOutputBytes = 4096

// Run executes every check in checks and returns exactly one
// provenance.VerificationResult per input CheckSpec, in the same order —
// this 1:1 correspondence is the core "does not silently treat missing
// tools as success" guarantee from ADR-0032/Jira MOD-63: nothing in checks
// can disappear from the result set, and every result's Status is one of
// the five explicit provenance.Status values, never inferred as success by
// omission.
//
// tools is the discovery.Discover output's Tools field (or an equivalent
// slice), used to gate any CheckSpec with a non-empty RequiredTool: if the
// named tool is not Present, Run reports StatusUnavailable with a Reason
// naming the missing tool and does NOT attempt to run the check's Command
// at all — no process is spawned, so a check requiring a missing tool can
// never accidentally "pass" by running something else on PATH with a
// similar name, and can never hang or error in a way that gets confused
// with an actual failure.
//
// allowNetwork gates any CheckSpec with Networked set: when false, such a
// check is reported as StatusSkipped with an explanatory Reason instead of
// being run. The ticket's sketch signature omitted this parameter ("accept
// an explicit allowNetwork bool parameter or a similar environment-
// capability flag the caller passes in"); it is added here as an explicit
// argument rather than folded into CheckSpec or inferred from the
// environment, since real network-reachability detection is out of scope
// and an explicit caller-supplied flag is what the ticket asked for.
//
// Every other check is actually executed via "sh -c <Command>", with ctx
// honored for cancellation/timeout. Exit code 0 maps to StatusPass; any
// other outcome (nonzero exit, or ctx cancellation) maps to StatusFail.
// Combined stdout+stderr is captured into Message, truncated per
// maxOutputBytes.
func Run(ctx context.Context, checks []CheckSpec, tools []discovery.ToolStatus, allowNetwork bool) []provenance.VerificationResult {
	present := toolPresence(tools)

	results := make([]provenance.VerificationResult, 0, len(checks))
	for _, c := range checks {
		results = append(results, runOne(ctx, c, present, allowNetwork))
	}
	return results
}

// toolPresence indexes tools by name for O(1) lookup, so Run does not scan
// the full tools slice once per check.
func toolPresence(tools []discovery.ToolStatus) map[string]bool {
	present := make(map[string]bool, len(tools))
	for _, t := range tools {
		present[t.Name] = t.Present
	}
	return present
}

// runOne runs (or explicitly declines to run) one check, returning exactly
// one provenance.VerificationResult. See Run's doc comment for the
// tool-availability and network-capability gating rules this enforces.
func runOne(ctx context.Context, c CheckSpec, present map[string]bool, allowNetwork bool) provenance.VerificationResult {
	if c.RequiredTool != "" && !present[c.RequiredTool] {
		return provenance.VerificationResult{
			Name:     c.Name,
			Category: c.Category,
			Status:   provenance.StatusUnavailable,
			Reason:   fmt.Sprintf("required tool %q is not present on PATH", c.RequiredTool),
		}
	}

	if c.Networked && !allowNetwork {
		return provenance.VerificationResult{
			Name:     c.Name,
			Category: c.Category,
			Status:   provenance.StatusSkipped,
			Reason:   "networked check skipped: no network access in this environment",
		}
	}

	start := time.Now()
	output, err := runCommand(ctx, c.Command)
	duration := time.Since(start)

	status := provenance.StatusPass
	if err != nil {
		status = provenance.StatusFail
	}

	return provenance.VerificationResult{
		Name:     c.Name,
		Category: c.Category,
		Status:   status,
		Duration: duration,
		Message:  truncateOutput(output),
	}
}

// runCommand actually executes command via the shell (so Command strings
// like "go test ./httpx/..." or a "make X" target work exactly as a human
// would type them, including any shell features they rely on), honoring ctx
// for cancellation. It returns combined stdout+stderr and the command's
// error (nil on exit code 0).
func runCommand(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// truncateOutput bounds s to maxOutputBytes, keeping the tail (where a
// failure or panic is most often reported) and prefixing a marker when
// truncation occurred.
func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return "...[output truncated]...\n" + s[len(s)-maxOutputBytes:]
}

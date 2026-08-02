// Package provenanceci builds a provenance.Envelope summarizing one CI run:
// repository state plus the pass/fail/skipped outcome of every job in that
// run, mapped from GitHub Actions' `needs.<job>.result` values. It backs
// the provenanceci CLI (cmd/provenanceci), implementing ADR-0032's P2
// delivery item "Publish provenance artifacts from CI" (Jira MOD-72). See
// docs/planning/agent-provenance-ci-guide.md and
// docs/planning/provenance-handoff-schema.md for the Envelope schema this
// produces.
//
// provenanceci is a separate, nested Go module (like tools/modboundary and
// tools/scaffold), so it is not part of the root module's build graph or
// dependency footprint. It depends on the root module for provenance.
// Envelope the same way examples/external-consumer does, via a local
// replace directive.
package provenanceci

import (
	"fmt"
	"sort"
	"time"

	"github.com/mediusfy/modulex/provenance"
)

// JobResult is one CI job's outcome, as reported by GitHub Actions'
// `needs.<job>.result` context: "success", "failure", "cancelled", or
// "skipped" (GitHub's own documented set of job.result values).
type JobResult struct {
	Name   string
	Result string
}

// Config describes the inputs needed to build a CI provenance envelope.
type Config struct {
	// RepoPath, Commit, Branch, and Dirty describe the repository state
	// this CI run checked out. Commit is required — BuildEnvelope returns
	// an error if it is empty (provenance.Envelope.Validate requires
	// Repository.Commit).
	RepoPath string
	Commit   string
	Branch   string
	Dirty    bool
	// Jobs is every CI job's outcome to record as a VerificationResult.
	// At least one entry is expected, though BuildEnvelope does not itself
	// enforce that (an empty Jobs produces an envelope with no
	// Verification entries, which is structurally valid).
	Jobs []JobResult
	// CreatedAt is when this envelope was produced. Defaults to
	// time.Now().UTC() if zero.
	CreatedAt time.Time
}

// unknownJobResultReasonFmt is used when a job's raw Result string does not
// match any of GitHub Actions' documented values, so an unrecognized or
// future value fails safe (StatusFail) rather than silently passing.
const unknownJobResultReasonFmt = "unrecognized GitHub Actions job result %q; treating as failed rather than silently passing"

// BuildEnvelope builds a provenance.Envelope from cfg: one
// provenance.VerificationResult per cfg.Jobs entry (Category
// provenance.VerificationFull, since every job name here corresponds to
// one of this repository's required CI gates), sorted by name for
// deterministic output, then Redacted and Validated before being returned.
//
// A non-nil error means the resulting envelope failed
// provenance.Envelope.Validate (most likely cfg.Commit was empty) — the
// caller should treat this as a real bug in provenanceci or its caller,
// not as "the CI run failed": individual job failures are recorded as
// StatusFail VerificationResults within a still-valid envelope, not
// surfaced as an error return here.
func BuildEnvelope(cfg Config) (provenance.Envelope, error) {
	createdAt := cfg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	env := provenance.Envelope{
		SchemaVersion: provenance.SchemaVersion,
		Repository: provenance.RepoState{
			Path:   cfg.RepoPath,
			Branch: cfg.Branch,
			Commit: cfg.Commit,
			Dirty:  cfg.Dirty,
		},
		Agent: provenance.AgentInfo{
			Name: "ci",
			Tool: "github-actions",
		},
		Verification: verificationResults(cfg.Jobs),
		CreatedAt:    createdAt,
	}

	env.Redact()
	if err := env.Validate(); err != nil {
		return provenance.Envelope{}, fmt.Errorf("provenanceci: built an invalid envelope: %w", err)
	}
	return env, nil
}

// verificationResults maps jobs to one provenance.VerificationResult each,
// sorted by Name so BuildEnvelope's output does not depend on the order
// cfg.Jobs was given in (mirroring verify.dedupeAndSort's determinism
// discipline).
func verificationResults(jobs []JobResult) []provenance.VerificationResult {
	sorted := append([]JobResult(nil), jobs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	results := make([]provenance.VerificationResult, 0, len(sorted))
	for _, j := range sorted {
		results = append(results, jobResultToVerification(j))
	}
	return results
}

// jobResultToVerification maps one GitHub Actions job result string to a
// provenance.VerificationResult:
//
//	"success"   -> StatusPass
//	"failure"   -> StatusFail
//	"cancelled" -> StatusFail, with a Reason noting the cancellation
//	"skipped"   -> StatusSkipped, with a Reason (required by
//	               provenance.Envelope.Validate for StatusSkipped)
//	anything else -> StatusFail, with a Reason naming the unrecognized
//	               value, so an unexpected GitHub Actions result can never
//	               be silently treated as a pass
func jobResultToVerification(j JobResult) provenance.VerificationResult {
	switch j.Result {
	case "success":
		return provenance.VerificationResult{
			Name:     j.Name,
			Category: provenance.VerificationFull,
			Status:   provenance.StatusPass,
		}
	case "failure":
		return provenance.VerificationResult{
			Name:     j.Name,
			Category: provenance.VerificationFull,
			Status:   provenance.StatusFail,
		}
	case "cancelled":
		return provenance.VerificationResult{
			Name:     j.Name,
			Category: provenance.VerificationFull,
			Status:   provenance.StatusFail,
			Reason:   "GitHub Actions job was cancelled before completing",
		}
	case "skipped":
		return provenance.VerificationResult{
			Name:     j.Name,
			Category: provenance.VerificationFull,
			Status:   provenance.StatusSkipped,
			Reason:   `GitHub Actions job was skipped (its "if" condition was not met)`,
		}
	default:
		return provenance.VerificationResult{
			Name:     j.Name,
			Category: provenance.VerificationFull,
			Status:   provenance.StatusFail,
			Reason:   fmt.Sprintf(unknownJobResultReasonFmt, j.Result),
		}
	}
}

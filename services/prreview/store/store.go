// Package store defines the hosted PR-review service's durable state:
// delivery dedup records, per-(installation, repo, PR) job documents with
// the lease that serializes reviews (ADR-0035 "No stale or overlapping
// reviews", Jira MOD-85), and the per-installation usage ledger (MOD-87).
//
// The lease state machine is expressed as pure decision functions
// (AcquireDecision, CompleteDecision) so its concurrency contract is
// table-testable; store implementations only apply the returned mutations
// inside their own transaction primitive. Firestore documents are
// partitioned by installation ID (MOD-86): every method takes the
// installation-scoped key, and no query spans installations.
package store

import (
	"context"
	"fmt"
	"time"
)

// JobKey identifies one PR's job document, always scoped to an installation.
type JobKey struct {
	InstallationID int64
	Owner          string
	Repo           string
	PRNumber       int
}

func (k JobKey) String() string {
	return fmt.Sprintf("%d/%s/%s/%d", k.InstallationID, k.Owner, k.Repo, k.PRNumber)
}

// JobStatus is the lease state of a PR's job document.
type JobStatus string

const (
	StatusIdle    JobStatus = "idle"
	StatusRunning JobStatus = "running"
)

// Job is the per-(installation, repo, PR) document: lease fields for
// serialization, review memory for incremental reviews and single-comment
// updates, and the usage ledger fields.
type Job struct {
	Status      JobStatus
	TargetSHA   string
	LeaseExpiry time.Time

	// Review memory (ADR-0035 "Remembers prior PRs").
	LastReviewedSHA string
	CommentID       int64

	// Usage ledger (MOD-87): reviews completed and model tokens reported
	// by the caller-keyed AI layer.
	ReviewCount int64
	TokensUsed  int64
}

// AcquireAction says what the worker should do after a lease transaction.
type AcquireAction string

const (
	// ActionRun means this worker holds the lease and must review TargetSHA.
	ActionRun AcquireAction = "run"
	// ActionSkip means another worker holds an unexpired lease; this
	// delivery only advanced TargetSHA and the running worker will follow up.
	ActionSkip AcquireAction = "skip"
)

// AcquireDecision is the pure lease-acquisition rule: given the current job
// document, the time, and the delivery's head SHA, it returns the mutated
// job and the action. Rapid pushes to one PR collapse: while a review runs,
// deliveries only advance TargetSHA; an expired lease is taken over so a
// crashed worker's PR retries rather than wedging forever.
func AcquireDecision(job Job, now time.Time, headSHA string, leaseTTL time.Duration) (Job, AcquireAction) {
	job.TargetSHA = headSHA
	if job.Status == StatusRunning && now.Before(job.LeaseExpiry) {
		return job, ActionSkip
	}
	job.Status = StatusRunning
	job.LeaseExpiry = now.Add(leaseTTL)
	return job, ActionRun
}

// CompleteOutcome says what the worker should do after finishing a review.
type CompleteOutcome struct {
	// Post is true when the reviewed SHA is still the target: the comment
	// may be posted. A review of a superseded SHA is never posted.
	Post bool
	// FollowUp is true when TargetSHA advanced during the review: exactly
	// one follow-up task must be enqueued for the new target.
	FollowUp bool
	// FollowUpSHA is the SHA the follow-up must review (when FollowUp).
	FollowUpSHA string
}

// CompleteDecision is the pure completion rule: given the job document and
// the SHA this worker just reviewed, it returns the mutated job and the
// outcome. The lease is released either way; review memory and the ledger
// advance only for a non-superseded review.
func CompleteDecision(job Job, reviewedSHA string, commentID int64, tokensUsed int64) (Job, CompleteOutcome) {
	outcome := CompleteOutcome{}
	if job.TargetSHA == reviewedSHA {
		outcome.Post = true
		job.LastReviewedSHA = reviewedSHA
		if commentID != 0 {
			job.CommentID = commentID
		}
		job.ReviewCount++
		job.TokensUsed += tokensUsed
	} else {
		outcome.FollowUp = true
		outcome.FollowUpSHA = job.TargetSHA
	}
	job.Status = StatusIdle
	job.LeaseExpiry = time.Time{}
	return job, outcome
}

// JobStore persists Job documents with a serializable read-modify-write
// primitive; implementations run mutate inside their transaction.
type JobStore interface {
	// Mutate reads the job (zero value if absent), applies mutate, and
	// writes the result atomically. It returns the mutated job.
	Mutate(ctx context.Context, key JobKey, mutate func(Job) Job) (Job, error)
	// Get reads the job document (zero value if absent).
	Get(ctx context.Context, key JobKey) (Job, error)
}

// DedupStore records GitHub delivery IDs (MOD-83). Records self-expire via
// the store's TTL mechanism; Seen returns true when the ID was already
// recorded, atomically recording it otherwise.
type DedupStore interface {
	Seen(ctx context.Context, deliveryID string, ttl time.Duration) (bool, error)
}

// Usage is one installation's aggregated ledger view (MOD-87).
type Usage struct {
	Reviews int64
	Tokens  int64
}

// Ledger aggregates usage per installation.
type Ledger interface {
	// Add records a completed review's usage for an installation.
	Add(ctx context.Context, installationID int64, reviews, tokens int64) error
	// Total returns the installation's aggregate usage.
	Total(ctx context.Context, installationID int64) (Usage, error)
}

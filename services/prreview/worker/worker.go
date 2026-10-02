// Package worker runs one PR review job end to end (ADR-0035 plan step 6;
// Jira MOD-84 with the MOD-85 lease, MOD-86 tenancy, and MOD-87 metering
// woven through): acquire the per-PR lease, mint an installation-scoped
// token, fetch the checkout, run the engine, add caller-keyed AI
// commentary, post or update the single PR comment, record usage, release
// the lease, and follow up once if the target moved during the review.
//
// Any returned error means the task queue retries the job (durable
// retries); the lease is released best-effort first so the retry is not
// blocked, and an unreleased lease still expires on its TTL.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/services/prreview/ai"
	"github.com/mediusfy/modulex/services/prreview/engine"
	"github.com/mediusfy/modulex/services/prreview/githubauth"
	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/store"
	"github.com/mediusfy/modulex/services/prreview/tenants"
	"github.com/mediusfy/modulex/services/prreview/webhook"
)

// Worker wires the job pipeline. Every dependency is an interface with an
// in-memory implementation, so the whole flow is table-testable.
type Worker struct {
	Jobs       store.JobStore
	Queue      queue.Enqueuer
	Tokens     githubauth.TokenMinter
	Fetcher    engine.Fetcher
	Reviewer   engine.Reviewer
	Commentary ai.Commentary
	Comments   githubauth.Commenter
	// PRs resolves the PR's true head from GitHub after the lease is
	// acquired: Cloud Tasks deliveries are unordered, so a late or
	// retried task must be re-anchored on the authoritative head rather
	// than trusted. Optional (nil skips the correction, e.g. in tests).
	PRs      githubauth.PRReader
	Ledger   store.Ledger
	Tenants  tenants.Resolver
	LeaseTTL time.Duration
	Log      *slog.Logger
	Now      func() time.Time
	// NewOwner mints the lease fencing token; overridable in tests.
	NewOwner func() string
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) newOwner() string {
	if w.NewOwner != nil {
		return w.NewOwner()
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Fallback keeps fencing best-effort rather than failing reviews.
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// Handle processes one review task.
func (w *Worker) Handle(ctx context.Context, req webhook.ReviewRequest) error {
	key := store.JobKey{
		InstallationID: req.InstallationID,
		Owner:          req.Owner,
		Repo:           req.Repo,
		PRNumber:       req.PRNumber,
	}
	owner := w.newOwner()

	var action store.AcquireAction
	job, err := w.Jobs.Mutate(ctx, key, func(j store.Job) store.Job {
		j2, a := store.AcquireDecision(j, w.now(), req.HeadSHA, w.LeaseTTL, owner)
		action = a
		return j2
	})
	if err != nil {
		return fmt.Errorf("acquiring lease for %s: %w", key, err)
	}
	if action == store.ActionSkip {
		w.Log.Info("review already running; target advanced", "job", key.String(), "sha", req.HeadSHA)
		return nil
	}

	reviewedSHA := job.TargetSHA
	if err := w.reviewOnce(ctx, key, req, job, reviewedSHA, owner); err != nil {
		w.releaseLease(ctx, key, owner)
		return err
	}
	return nil
}

func (w *Worker) reviewOnce(ctx context.Context, key store.JobKey, req webhook.ReviewRequest, job store.Job, reviewedSHA, owner string) error {
	token, err := w.Tokens.InstallationToken(ctx, req.InstallationID)
	if err != nil {
		return fmt.Errorf("minting installation token: %w", err)
	}

	// Re-anchor on GitHub's answer for the PR head: the lease target may
	// have been set by a stale, reordered, or retried task. Failure to
	// resolve degrades to the lease target rather than dropping the job.
	if w.PRs != nil {
		if head, err := w.PRs.PRHead(ctx, token, req.Owner, req.Repo, req.PRNumber); err != nil {
			w.Log.Warn("PR head lookup failed; using lease target", "job", key.String(), "error", err)
		} else if head != reviewedSHA {
			w.Log.Info("lease target stale; re-anchored on PR head", "job", key.String(), "lease", reviewedSHA, "head", head)
			reviewedSHA = head
			if _, err := w.Jobs.Mutate(ctx, key, func(j store.Job) store.Job {
				if j.LeaseOwner == owner {
					j.TargetSHA = head
				}
				return j
			}); err != nil {
				return fmt.Errorf("re-anchoring target for %s: %w", key, err)
			}
		}
	}

	// Incremental review (MOD-84): diff from the last-reviewed SHA when
	// there is one, else from the PR base branch.
	diffBase := req.BaseRef
	if job.LastReviewedSHA != "" && job.LastReviewedSHA != reviewedSHA {
		diffBase = job.LastReviewedSHA
	}
	dir, cleanup, err := w.Fetcher.Fetch(ctx, req.CloneURL, token, diffBase, reviewedSHA)
	if err != nil {
		return fmt.Errorf("fetching checkout: %w", err)
	}
	defer cleanup()

	results, err := w.Reviewer.Review(ctx, dir, engine.BaseRefName, "HEAD")
	if err != nil {
		return fmt.Errorf("running engine: %w", err)
	}

	// Caller-keyed commentary. AI failure degrades to engine-only —
	// logged, never fatal, so a tenant's model outage cannot drop reviews.
	var commentary string
	var tokensUsed int64
	cfg, err := w.Tenants.AIConfig(ctx, req.InstallationID)
	if err != nil {
		w.Log.Warn("tenant config unavailable; engine-only review", "job", key.String(), "error", err)
	} else if cfg.Enabled() {
		diff, derr := w.Fetcher.Diff(ctx, dir, engine.BaseRefName, "HEAD")
		if derr != nil {
			w.Log.Warn("diff unavailable for commentary", "job", key.String(), "error", derr)
		}
		commentary, tokensUsed, err = w.Commentary.Comment(ctx, ai.Config{
			Provider: cfg.Provider,
			APIKey:   cfg.APIKey,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
		}, results, diff)
		if err != nil {
			w.Log.Warn("AI commentary failed; engine-only review", "job", key.String(), "provider", cfg.Provider, "error", err)
			commentary = ""
		}
	}

	// Post before completing, but never for a SHA that is no longer the
	// target and never on a lease this worker no longer holds.
	current, err := w.Jobs.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("re-reading job before post: %w", err)
	}
	if current.TargetSHA == reviewedSHA && current.LeaseOwner == owner {
		body := ComposeComment(reviewedSHA, results, commentary)
		if current.CommentID != 0 {
			if err := w.Comments.UpdateComment(ctx, token, req.Owner, req.Repo, current.CommentID, body); err != nil {
				return fmt.Errorf("updating comment: %w", err)
			}
		} else {
			commentID, err := w.Comments.CreateComment(ctx, token, req.Owner, req.Repo, req.PRNumber, body)
			if err != nil {
				return fmt.Errorf("creating comment: %w", err)
			}
			// Persist the ID immediately and unconditionally (even if the
			// target advances before completion): losing it would orphan
			// the comment and break the single-comment invariant.
			if _, err := w.Jobs.Mutate(ctx, key, func(j store.Job) store.Job {
				if j.CommentID == 0 {
					j.CommentID = commentID
				}
				return j
			}); err != nil {
				return fmt.Errorf("persisting comment id for %s: %w", key, err)
			}
		}
	}

	var outcome store.CompleteOutcome
	if _, err := w.Jobs.Mutate(ctx, key, func(j store.Job) store.Job {
		j2, o := store.CompleteDecision(j, reviewedSHA, tokensUsed, owner)
		outcome = o
		return j2
	}); err != nil {
		return fmt.Errorf("completing job %s: %w", key, err)
	}

	if outcome.Post {
		if err := w.Ledger.Add(ctx, req.InstallationID, 1, tokensUsed); err != nil {
			w.Log.Warn("ledger update failed", "job", key.String(), "error", err)
		}
		w.Log.Info("review posted", "job", key.String(), "sha", reviewedSHA, "tokens", tokensUsed)
	}
	if outcome.FollowUp {
		follow := req
		follow.HeadSHA = outcome.FollowUpSHA
		follow.DeliveryID = req.DeliveryID + "+followup"
		if err := w.Queue.Enqueue(ctx, follow); err != nil {
			return fmt.Errorf("enqueueing follow-up for %s: %w", key, err)
		}
		w.Log.Info("target moved during review; follow-up enqueued", "job", key.String(), "sha", outcome.FollowUpSHA)
	}
	return nil
}

// releaseLease is a best-effort idle reset after a failure, so the task
// queue's immediate retry is not blocked until the lease TTL expires.
// Fenced by owner: a stale worker whose lease was taken over must not
// wipe the takeover worker's active lease.
func (w *Worker) releaseLease(ctx context.Context, key store.JobKey, owner string) {
	if _, err := w.Jobs.Mutate(ctx, key, func(j store.Job) store.Job {
		return store.ReleaseDecision(j, owner)
	}); err != nil {
		w.Log.Warn("lease release failed; TTL will expire it", "job", key.String(), "error", err)
	}
}

// ComposeComment renders the single PR comment: the deterministic engine
// results (ground truth), then optional AI commentary, then a marker footer.
func ComposeComment(sha string, results []provenance.VerificationResult, commentary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Modulex PR review — `%s`\n\n", shortSHA(sha))
	b.WriteString("| Check | Category | Status |\n|---|---|---|\n")
	failed := false
	for _, r := range results {
		if r.Status == provenance.StatusFail {
			failed = true
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Name, r.Category, r.Status)
	}
	b.WriteString("\n")
	if failed {
		b.WriteString("**Some checks failed.** Details:\n\n")
		for _, r := range results {
			if r.Status != provenance.StatusFail {
				continue
			}
			detail := r.Message
			if detail == "" {
				detail = r.Reason
			}
			fmt.Fprintf(&b, "<details><summary>%s</summary>\n\n```\n%s\n```\n</details>\n\n",
				escapeUntrusted(r.Name), escapeUntrusted(detail))
		}
	}
	if commentary != "" {
		b.WriteString("### AI commentary\n\n")
		b.WriteString(commentary)
		b.WriteString("\n\n")
	}
	b.WriteString("<sub>modulex hosted PR review (ADR-0035); this comment is updated in place on new pushes.</sub>\n")
	return b.String()
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// escapeUntrusted neutralizes check output before it is embedded in the
// comment's code fence: the output covers the PR author's own code, so a
// crafted failure message could otherwise close the fence and inject
// Markdown under the bot's identity. A zero-width space inside any
// backtick run keeps fences and inline code from terminating early
// without visibly altering the text.
func escapeUntrusted(s string) string {
	return strings.ReplaceAll(s, "`", "`\u200b")
}

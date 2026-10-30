package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// Registers the "pgx" database/sql driver used by NewPostgres/sql.Open.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Postgres implements JobStore, DedupStore, and Ledger on PostgreSQL, for
// deployments that have traded Firestore's scale-to-zero pricing for a
// self-hosted k3s cluster with its own always-on Postgres instance. Apply
// store/migrations/001_init_schema.sql before using it.
//
// Every per-key read-modify-write (Mutate, Seen) takes a transaction-scoped
// Postgres advisory lock (pg_advisory_xact_lock, released automatically at
// commit/rollback) keyed on the record's identity before reading it. This
// matters most for a brand-new key: a row that does not exist yet cannot be
// serialized by a row lock ("SELECT ... FOR UPDATE" locks nothing when
// there is nothing to lock), so without the advisory lock two concurrent
// first-time calls for the same key — e.g. two near-simultaneous webhook
// deliveries for a PR that has never been reviewed before — could both read
// "absent", compute independently, and race on the final upsert. The
// advisory lock closes that window regardless of whether the row already
// exists, matching the atomicity Firestore's per-document RunTransaction
// gives the other implementation.
//
// Dedup TTL (MOD-83): unlike Firestore's native TTL policy, Postgres has no
// built-in expiry, and this implementation deliberately does not run a
// background cleanup job for one — Seen simply never considers a row whose
// expire_at has passed ("AND expire_at > now()" on the same lookup it
// already does), so an unpruned expired row is harmless dead weight rather
// than a correctness problem. Whether and how often to vacuum prreview_dedup
// is an operational choice this package does not make for its caller;
// "fail open on cleanup, never block behavior on it" matches this service's
// existing posture elsewhere (e.g. worker.go's AI-commentary and ledger
// paths degrade rather than block a review).
type Postgres struct {
	DB *sql.DB
	// Now is the clock, overridable in tests. Defaults to time.Now.
	Now func() time.Time
}

// NewPostgres opens a connection pool against dsn using the pgx stdlib
// driver and returns a Postgres store wrapping it. The caller owns the
// returned *sql.DB and must Close it on shutdown.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}
	return &Postgres{DB: db}, nil
}

func (p *Postgres) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// advisoryLockKind namespaces the two keyspaces (job keys, dedup delivery
// IDs) that take an advisory lock, so a job key and a delivery ID that
// happen to hash the same text never contend on the same lock.
type advisoryLockKind int64

const (
	lockKindJob   advisoryLockKind = 1
	lockKindDedup advisoryLockKind = 2
)

// lockKey takes the transaction-scoped advisory lock for kind/id (held
// until tx commits or rolls back) before the caller reads the row it is
// about to read-modify-write. See the Postgres doc comment for why this is
// needed even when the underlying row does not exist yet.
func lockKey(ctx context.Context, tx *sql.Tx, kind advisoryLockKind, id string) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, id, int64(kind)); err != nil {
		return fmt.Errorf("acquiring advisory lock: %w", err)
	}
	return nil
}

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, so getJob can read
// either a plain snapshot (Get) or a lock-held row inside a transaction
// (Mutate) through the same code.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getJob(ctx context.Context, q rowQuerier, key JobKey) (Job, error) {
	var (
		status, targetSHA, leaseOwner, lastReviewedSHA string
		leaseExpiry                                    sql.NullTime
		commentID, reviewCount, tokensUsed             int64
	)
	row := q.QueryRowContext(ctx, `
		SELECT status, target_sha, lease_expiry, lease_owner, last_reviewed_sha, comment_id, review_count, tokens_used
		FROM prreview_jobs
		WHERE installation_id = $1 AND owner = $2 AND repo = $3 AND pr_number = $4`,
		key.InstallationID, key.Owner, key.Repo, key.PRNumber)
	err := row.Scan(&status, &targetSHA, &leaseExpiry, &leaseOwner, &lastReviewedSHA, &commentID, &reviewCount, &tokensUsed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Job{}, nil
	case err != nil:
		return Job{}, err
	}
	job := Job{
		Status: JobStatus(status), TargetSHA: targetSHA, LeaseOwner: leaseOwner,
		LastReviewedSHA: lastReviewedSHA, CommentID: commentID,
		ReviewCount: reviewCount, TokensUsed: tokensUsed,
	}
	if leaseExpiry.Valid {
		// pgx decodes timestamptz into the process's local time.Time
		// Location, not UTC; normalize so a round-tripped Job compares
		// equal (==) to one built from a UTC time.Time, matching the
		// Firestore implementation (whose client already returns UTC).
		job.LeaseExpiry = leaseExpiry.Time.UTC()
	}
	return job, nil
}

func upsertJob(ctx context.Context, tx *sql.Tx, key JobKey, job Job) error {
	var leaseExpiry any
	if !job.LeaseExpiry.IsZero() {
		leaseExpiry = job.LeaseExpiry
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO prreview_jobs
			(installation_id, owner, repo, pr_number, status, target_sha, lease_expiry, lease_owner, last_reviewed_sha, comment_id, review_count, tokens_used)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (installation_id, owner, repo, pr_number) DO UPDATE SET
			status = EXCLUDED.status,
			target_sha = EXCLUDED.target_sha,
			lease_expiry = EXCLUDED.lease_expiry,
			lease_owner = EXCLUDED.lease_owner,
			last_reviewed_sha = EXCLUDED.last_reviewed_sha,
			comment_id = EXCLUDED.comment_id,
			review_count = EXCLUDED.review_count,
			tokens_used = EXCLUDED.tokens_used`,
		key.InstallationID, key.Owner, key.Repo, key.PRNumber,
		string(job.Status), job.TargetSHA, leaseExpiry, job.LeaseOwner, job.LastReviewedSHA,
		job.CommentID, job.ReviewCount, job.TokensUsed)
	return err
}

func (p *Postgres) Mutate(ctx context.Context, key JobKey, mutate func(Job) Job) (Job, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, fmt.Errorf("postgres mutate %s: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockKey(ctx, tx, lockKindJob, key.String()); err != nil {
		return Job{}, fmt.Errorf("postgres mutate %s: %w", key, err)
	}

	job, err := getJob(ctx, tx, key)
	if err != nil {
		return Job{}, fmt.Errorf("postgres mutate %s: %w", key, err)
	}

	out := mutate(job)

	if err := upsertJob(ctx, tx, key, out); err != nil {
		return Job{}, fmt.Errorf("postgres mutate %s: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return Job{}, fmt.Errorf("postgres mutate %s: commit: %w", key, err)
	}
	return out, nil
}

func (p *Postgres) Get(ctx context.Context, key JobKey) (Job, error) {
	job, err := getJob(ctx, p.DB, key)
	if err != nil {
		return Job{}, fmt.Errorf("postgres get %s: %w", key, err)
	}
	return job, nil
}

func (p *Postgres) Seen(ctx context.Context, deliveryID string, ttl time.Duration) (bool, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("postgres dedup %s: %w", deliveryID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockKey(ctx, tx, lockKindDedup, deliveryID); err != nil {
		return false, fmt.Errorf("postgres dedup %s: %w", deliveryID, err)
	}

	now := p.now()
	var seen bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM prreview_dedup WHERE delivery_id = $1 AND expire_at > $2)`,
		deliveryID, now).Scan(&seen); err != nil {
		return false, fmt.Errorf("postgres dedup %s: %w", deliveryID, err)
	}
	if seen {
		return true, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO prreview_dedup (delivery_id, expire_at) VALUES ($1, $2)
		ON CONFLICT (delivery_id) DO UPDATE SET expire_at = EXCLUDED.expire_at`,
		deliveryID, now.Add(ttl)); err != nil {
		return false, fmt.Errorf("postgres dedup %s: %w", deliveryID, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("postgres dedup %s: commit: %w", deliveryID, err)
	}
	return false, nil
}

func (p *Postgres) Forget(ctx context.Context, deliveryID string) error {
	if _, err := p.DB.ExecContext(ctx, `DELETE FROM prreview_dedup WHERE delivery_id = $1`, deliveryID); err != nil {
		return fmt.Errorf("postgres dedup forget %s: %w", deliveryID, err)
	}
	return nil
}

func (p *Postgres) Add(ctx context.Context, installationID int64, reviews, tokens int64) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO prreview_ledger (installation_id, reviews, tokens) VALUES ($1, $2, $3)
		ON CONFLICT (installation_id) DO UPDATE SET
			reviews = prreview_ledger.reviews + EXCLUDED.reviews,
			tokens = prreview_ledger.tokens + EXCLUDED.tokens`,
		installationID, reviews, tokens)
	if err != nil {
		return fmt.Errorf("postgres ledger %d: %w", installationID, err)
	}
	return nil
}

func (p *Postgres) Total(ctx context.Context, installationID int64) (Usage, error) {
	var u Usage
	err := p.DB.QueryRowContext(ctx,
		`SELECT reviews, tokens FROM prreview_ledger WHERE installation_id = $1`, installationID,
	).Scan(&u.Reviews, &u.Tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("postgres ledger %d: %w", installationID, err)
	}
	return u, nil
}

var (
	_ JobStore   = (*Postgres)(nil)
	_ DedupStore = (*Postgres)(nil)
	_ Ledger     = (*Postgres)(nil)
)

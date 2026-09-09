package store

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Firestore implements JobStore, DedupStore, and Ledger on Firestore
// (ADR-0035: "Firestore, not Cloud SQL" — no always-on instance, idle cost
// is storage cents). Documents are partitioned by installation ID
// (MOD-86): job documents live under installations/{id}/repos/{owner__repo}
// /prs/{pr}, ledger aggregates on installations/{id}, and no query ever
// spans installations. Dedup records live in a flat dedup collection whose
// expire_at field carries the native Firestore TTL policy (MOD-83), so
// they self-expire with no cleanup worker; Seen still compares expire_at
// because TTL deletion is eventually-consistent (can lag ~24h).
type Firestore struct {
	Client *firestore.Client
	Now    func() time.Time
}

func (f *Firestore) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// jobDoc is the wire form of Job.
type jobDoc struct {
	Status          string    `firestore:"status"`
	TargetSHA       string    `firestore:"target_sha"`
	LeaseExpiry     time.Time `firestore:"lease_expiry"`
	LastReviewedSHA string    `firestore:"last_reviewed_sha"`
	CommentID       int64     `firestore:"comment_id"`
	ReviewCount     int64     `firestore:"review_count"`
	TokensUsed      int64     `firestore:"tokens_used"`
}

func toDoc(j Job) jobDoc {
	return jobDoc{
		Status: string(j.Status), TargetSHA: j.TargetSHA, LeaseExpiry: j.LeaseExpiry,
		LastReviewedSHA: j.LastReviewedSHA, CommentID: j.CommentID,
		ReviewCount: j.ReviewCount, TokensUsed: j.TokensUsed,
	}
}

func fromDoc(d jobDoc) Job {
	return Job{
		Status: JobStatus(d.Status), TargetSHA: d.TargetSHA, LeaseExpiry: d.LeaseExpiry,
		LastReviewedSHA: d.LastReviewedSHA, CommentID: d.CommentID,
		ReviewCount: d.ReviewCount, TokensUsed: d.TokensUsed,
	}
}

func (f *Firestore) jobRef(key JobKey) *firestore.DocumentRef {
	return f.Client.Collection("installations").
		Doc(fmt.Sprintf("%d", key.InstallationID)).
		Collection("repos").Doc(key.Owner + "__" + key.Repo).
		Collection("prs").Doc(fmt.Sprintf("%d", key.PRNumber))
}

func (f *Firestore) Mutate(ctx context.Context, key JobKey, mutate func(Job) Job) (Job, error) {
	ref := f.jobRef(key)
	var out Job
	err := f.Client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		var job Job
		switch {
		case err == nil:
			var d jobDoc
			if err := snap.DataTo(&d); err != nil {
				return err
			}
			job = fromDoc(d)
		case status.Code(err) == codes.NotFound:
			job = Job{}
		default:
			return err
		}
		out = mutate(job)
		return tx.Set(ref, toDoc(out))
	})
	if err != nil {
		return Job{}, fmt.Errorf("firestore mutate %s: %w", key, err)
	}
	return out, nil
}

func (f *Firestore) Get(ctx context.Context, key JobKey) (Job, error) {
	snap, err := f.jobRef(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Job{}, nil
	}
	if err != nil {
		return Job{}, fmt.Errorf("firestore get %s: %w", key, err)
	}
	var d jobDoc
	if err := snap.DataTo(&d); err != nil {
		return Job{}, err
	}
	return fromDoc(d), nil
}

func (f *Firestore) Seen(ctx context.Context, deliveryID string, ttl time.Duration) (bool, error) {
	ref := f.Client.Collection("dedup").Doc(deliveryID)
	seen := false
	err := f.Client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		now := f.now()
		if err == nil {
			expireAt, _ := snap.DataAt("expire_at")
			if t, ok := expireAt.(time.Time); ok && now.Before(t) {
				seen = true
				return nil
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		return tx.Set(ref, map[string]any{"expire_at": now.Add(ttl)})
	})
	if err != nil {
		return false, fmt.Errorf("firestore dedup %s: %w", deliveryID, err)
	}
	return seen, nil
}

func (f *Firestore) Add(ctx context.Context, installationID int64, reviews, tokens int64) error {
	ref := f.Client.Collection("installations").Doc(fmt.Sprintf("%d", installationID))
	_, err := ref.Set(ctx, map[string]any{
		"reviews": firestore.Increment(reviews),
		"tokens":  firestore.Increment(tokens),
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("firestore ledger %d: %w", installationID, err)
	}
	return nil
}

func (f *Firestore) Total(ctx context.Context, installationID int64) (Usage, error) {
	snap, err := f.Client.Collection("installations").Doc(fmt.Sprintf("%d", installationID)).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("firestore ledger %d: %w", installationID, err)
	}
	var d struct {
		Reviews int64 `firestore:"reviews"`
		Tokens  int64 `firestore:"tokens"`
	}
	if err := snap.DataTo(&d); err != nil {
		return Usage{}, err
	}
	return Usage{Reviews: d.Reviews, Tokens: d.Tokens}, nil
}

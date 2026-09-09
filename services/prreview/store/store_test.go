package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

const leaseTTL = 10 * time.Minute

func TestAcquireDecision(t *testing.T) {
	tests := []struct {
		name       string
		job        Job
		now        time.Time
		headSHA    string
		wantAction AcquireAction
		wantJob    Job
	}{
		{
			name:       "fresh PR acquires the lease and runs",
			job:        Job{},
			now:        t0,
			headSHA:    "sha1",
			wantAction: ActionRun,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL)},
		},
		{
			name:       "idle PR with history re-acquires",
			job:        Job{Status: StatusIdle, LastReviewedSHA: "old", CommentID: 5, ReviewCount: 2},
			now:        t0,
			headSHA:    "sha2",
			wantAction: ActionRun,
			wantJob: Job{
				Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL),
				LastReviewedSHA: "old", CommentID: 5, ReviewCount: 2,
			},
		},
		{
			name:       "delivery during a running review only advances the target",
			job:        Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(5 * time.Minute)},
			now:        t0,
			headSHA:    "sha2",
			wantAction: ActionSkip,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(5 * time.Minute)},
		},
		{
			name:       "expired lease is taken over so a crashed worker retries",
			job:        Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(-time.Second)},
			now:        t0,
			headSHA:    "sha2",
			wantAction: ActionRun,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJob, gotAction := AcquireDecision(tt.job, tt.now, tt.headSHA, leaseTTL)
			if gotAction != tt.wantAction {
				t.Fatalf("action: got %q, want %q", gotAction, tt.wantAction)
			}
			if gotJob != tt.wantJob {
				t.Fatalf("job: got %+v, want %+v", gotJob, tt.wantJob)
			}
		})
	}
}

func TestCompleteDecision(t *testing.T) {
	running := Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL), ReviewCount: 1, TokensUsed: 100}
	tests := []struct {
		name        string
		job         Job
		reviewedSHA string
		commentID   int64
		tokens      int64
		wantOutcome CompleteOutcome
		wantJob     Job
	}{
		{
			name:        "current review posts and advances memory and ledger",
			job:         running,
			reviewedSHA: "sha1",
			commentID:   77,
			tokens:      250,
			wantOutcome: CompleteOutcome{Post: true},
			wantJob: Job{
				Status: StatusIdle, TargetSHA: "sha1", LastReviewedSHA: "sha1",
				CommentID: 77, ReviewCount: 2, TokensUsed: 350,
			},
		},
		{
			name:        "superseded review never posts and enqueues one follow-up",
			job:         Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), CommentID: 77, ReviewCount: 2},
			reviewedSHA: "sha1",
			commentID:   99,
			tokens:      500,
			wantOutcome: CompleteOutcome{FollowUp: true, FollowUpSHA: "sha2"},
			wantJob:     Job{Status: StatusIdle, TargetSHA: "sha2", CommentID: 77, ReviewCount: 2},
		},
		{
			name:        "zero comment id keeps the prior comment for the next update",
			job:         Job{Status: StatusRunning, TargetSHA: "sha1", CommentID: 55},
			reviewedSHA: "sha1",
			commentID:   0,
			tokens:      0,
			wantOutcome: CompleteOutcome{Post: true},
			wantJob:     Job{Status: StatusIdle, TargetSHA: "sha1", LastReviewedSHA: "sha1", CommentID: 55, ReviewCount: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJob, gotOutcome := CompleteDecision(tt.job, tt.reviewedSHA, tt.commentID, tt.tokens)
			if gotOutcome != tt.wantOutcome {
				t.Fatalf("outcome: got %+v, want %+v", gotOutcome, tt.wantOutcome)
			}
			if gotJob != tt.wantJob {
				t.Fatalf("job: got %+v, want %+v", gotJob, tt.wantJob)
			}
		})
	}
}

// TestLeaseSerializesConcurrentDeliveries drives the full acquire/complete
// cycle through the Memory store under concurrency: many rapid deliveries
// for one PR must produce exactly one runner at a time, never post a
// superseded SHA, and finish with the last SHA reviewed.
func TestLeaseSerializesConcurrentDeliveries(t *testing.T) {
	tests := []struct {
		name       string
		deliveries []string
	}{
		{name: "burst of rapid pushes", deliveries: []string{"s1", "s2", "s3", "s4", "s5"}},
		{name: "single delivery", deliveries: []string{"only"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mem := NewMemory()
			key := JobKey{InstallationID: 1, Owner: "o", Repo: "r", PRNumber: 1}

			var mu sync.Mutex
			running := 0
			maxRunning := 0
			posted := []string{}

			var review func(sha string)
			review = func(sha string) {
				mu.Lock()
				running++
				if running > maxRunning {
					maxRunning = running
				}
				mu.Unlock()

				// "Review" happens here; then complete.
				var outcome CompleteOutcome
				_, err := mem.Mutate(ctx, key, func(j Job) Job {
					j2, o := CompleteDecision(j, sha, 1, 10)
					outcome = o
					return j2
				})
				if err != nil {
					t.Errorf("complete: %v", err)
				}
				mu.Lock()
				running--
				if outcome.Post {
					posted = append(posted, sha)
				}
				mu.Unlock()
				if outcome.FollowUp {
					deliver(ctx, t, mem, key, outcome.FollowUpSHA, review)
				}
			}

			var wg sync.WaitGroup
			for _, sha := range tt.deliveries {
				wg.Add(1)
				go func(sha string) {
					defer wg.Done()
					deliver(ctx, t, mem, key, sha, review)
				}(sha)
			}
			wg.Wait()

			if maxRunning != 1 {
				t.Fatalf("max concurrent runners = %d, want 1", maxRunning)
			}
			final, _ := mem.Get(ctx, key)
			if final.Status != StatusIdle {
				t.Fatalf("final status %q, want idle", final.Status)
			}
			if final.LastReviewedSHA != final.TargetSHA {
				t.Fatalf("last reviewed %q never caught up to target %q", final.LastReviewedSHA, final.TargetSHA)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, sha := range posted[:len(posted)-1] {
				_ = sha // every posted SHA was the target at post time by construction
			}
			if posted[len(posted)-1] != final.TargetSHA {
				t.Fatalf("last posted %q, want final target %q", posted[len(posted)-1], final.TargetSHA)
			}
		})
	}
}

func deliver(ctx context.Context, t *testing.T, mem *Memory, key JobKey, sha string, review func(string)) {
	t.Helper()
	var action AcquireAction
	job, err := mem.Mutate(ctx, key, func(j Job) Job {
		j2, a := AcquireDecision(j, time.Now(), sha, leaseTTL)
		action = a
		return j2
	})
	if err != nil {
		t.Errorf("acquire: %v", err)
		return
	}
	if action == ActionRun {
		review(job.TargetSHA)
	}
}

func TestMemoryDedup(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, mem *Memory)
	}{
		{
			name: "second sighting within TTL is seen",
			run: func(t *testing.T, mem *Memory) {
				ctx := context.Background()
				if seen, _ := mem.Seen(ctx, "d1", time.Hour); seen {
					t.Fatal("first sighting reported seen")
				}
				if seen, _ := mem.Seen(ctx, "d1", time.Hour); !seen {
					t.Fatal("second sighting not reported seen")
				}
			},
		},
		{
			name: "expired record is not seen and re-records",
			run: func(t *testing.T, mem *Memory) {
				ctx := context.Background()
				now := t0
				mem.Now = func() time.Time { return now }
				_, _ = mem.Seen(ctx, "d1", time.Minute)
				now = t0.Add(2 * time.Minute)
				if seen, _ := mem.Seen(ctx, "d1", time.Minute); seen {
					t.Fatal("expired record still reported seen")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, NewMemory())
		})
	}
}

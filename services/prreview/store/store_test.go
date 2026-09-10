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
		owner      string
		wantAction AcquireAction
		wantJob    Job
	}{
		{
			name:       "fresh PR acquires the lease and runs",
			job:        Job{},
			now:        t0,
			headSHA:    "sha1",
			owner:      "w1",
			wantAction: ActionRun,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w1"},
		},
		{
			name:       "idle PR with history re-acquires",
			job:        Job{Status: StatusIdle, TargetSHA: "old", LastReviewedSHA: "old", CommentID: 5, ReviewCount: 2},
			now:        t0,
			headSHA:    "sha2",
			owner:      "w2",
			wantAction: ActionRun,
			wantJob: Job{
				Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2",
				LastReviewedSHA: "old", CommentID: 5, ReviewCount: 2,
			},
		},
		{
			name:       "delivery during a running review only advances the target",
			job:        Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(5 * time.Minute), LeaseOwner: "w1"},
			now:        t0,
			headSHA:    "sha2",
			owner:      "w2",
			wantAction: ActionSkip,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(5 * time.Minute), LeaseOwner: "w1"},
		},
		{
			name:       "expired lease is taken over so a crashed worker retries",
			job:        Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(-time.Second), LeaseOwner: "w1"},
			now:        t0,
			headSHA:    "sha2",
			owner:      "w2",
			wantAction: ActionRun,
			wantJob:    Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2"},
		},
		{
			name: "task for an already-reviewed SHA keeps the pending target",
			job: Job{
				Status: StatusIdle, TargetSHA: "sha2",
				LastReviewedSHA: "sha1", CommentID: 7, ReviewCount: 1,
			},
			now:        t0,
			headSHA:    "sha1", // late retry of the reviewed push
			owner:      "w3",
			wantAction: ActionRun,
			wantJob: Job{
				Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w3",
				LastReviewedSHA: "sha1", CommentID: 7, ReviewCount: 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJob, gotAction := AcquireDecision(tt.job, tt.now, tt.headSHA, leaseTTL, tt.owner)
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
	tests := []struct {
		name        string
		job         Job
		reviewedSHA string
		tokens      int64
		owner       string
		wantOutcome CompleteOutcome
		wantJob     Job
	}{
		{
			name:        "current review posts and advances memory and ledger",
			job:         Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w1", CommentID: 77, ReviewCount: 1, TokensUsed: 100},
			reviewedSHA: "sha1",
			tokens:      250,
			owner:       "w1",
			wantOutcome: CompleteOutcome{Post: true},
			wantJob: Job{
				Status: StatusIdle, TargetSHA: "sha1", LastReviewedSHA: "sha1",
				CommentID: 77, ReviewCount: 2, TokensUsed: 350,
			},
		},
		{
			name:        "superseded review never posts and enqueues one follow-up",
			job:         Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w1", CommentID: 77, ReviewCount: 2},
			reviewedSHA: "sha1",
			tokens:      500,
			owner:       "w1",
			wantOutcome: CompleteOutcome{FollowUp: true, FollowUpSHA: "sha2"},
			wantJob:     Job{Status: StatusIdle, TargetSHA: "sha2", CommentID: 77, ReviewCount: 2},
		},
		{
			name:        "stale worker (lease taken over) mutates nothing",
			job:         Job{Status: StatusRunning, TargetSHA: "sha3", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2"},
			reviewedSHA: "sha1",
			tokens:      100,
			owner:       "w1",
			wantOutcome: CompleteOutcome{},
			wantJob:     Job{Status: StatusRunning, TargetSHA: "sha3", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJob, gotOutcome := CompleteDecision(tt.job, tt.reviewedSHA, tt.tokens, tt.owner)
			if gotOutcome != tt.wantOutcome {
				t.Fatalf("outcome: got %+v, want %+v", gotOutcome, tt.wantOutcome)
			}
			if gotJob != tt.wantJob {
				t.Fatalf("job: got %+v, want %+v", gotJob, tt.wantJob)
			}
		})
	}
}

func TestReleaseDecision(t *testing.T) {
	running := Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w1", CommentID: 3}
	tests := []struct {
		name  string
		job   Job
		owner string
		want  Job
	}{
		{
			name:  "holder releases: lease idled, memory kept",
			job:   running,
			owner: "w1",
			want:  Job{Status: StatusIdle, TargetSHA: "sha1", CommentID: 3},
		},
		{
			name:  "stale worker cannot wipe a takeover lease",
			job:   Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2"},
			owner: "w1",
			want:  Job{Status: StatusRunning, TargetSHA: "sha2", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReleaseDecision(tt.job, tt.owner); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
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

			var review func(sha, owner string)
			review = func(sha, owner string) {
				mu.Lock()
				running++
				if running > maxRunning {
					maxRunning = running
				}
				mu.Unlock()

				var outcome CompleteOutcome
				_, err := mem.Mutate(ctx, key, func(j Job) Job {
					j2, o := CompleteDecision(j, sha, 10, owner)
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
			if posted[len(posted)-1] != final.TargetSHA {
				t.Fatalf("last posted %q, want final target %q", posted[len(posted)-1], final.TargetSHA)
			}
		})
	}
}

func deliver(ctx context.Context, t *testing.T, mem *Memory, key JobKey, sha string, review func(sha, owner string)) {
	t.Helper()
	owner := "owner-" + sha
	var action AcquireAction
	job, err := mem.Mutate(ctx, key, func(j Job) Job {
		j2, a := AcquireDecision(j, time.Now(), sha, leaseTTL, owner)
		action = a
		return j2
	})
	if err != nil {
		t.Errorf("acquire: %v", err)
		return
	}
	if action == ActionRun {
		review(job.TargetSHA, owner)
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
		{
			name: "forgotten record is not seen (failed-enqueue redelivery path)",
			run: func(t *testing.T, mem *Memory) {
				ctx := context.Background()
				_, _ = mem.Seen(ctx, "d1", time.Hour)
				if err := mem.Forget(ctx, "d1"); err != nil {
					t.Fatal(err)
				}
				if seen, _ := mem.Seen(ctx, "d1", time.Hour); seen {
					t.Fatal("forgotten record still reported seen")
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

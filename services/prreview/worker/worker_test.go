package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mediusfy/modulex/provenance"
	"github.com/mediusfy/modulex/services/prreview/queue"
	"github.com/mediusfy/modulex/services/prreview/store"
	"github.com/mediusfy/modulex/services/prreview/tenants"
	"github.com/mediusfy/modulex/services/prreview/webhook"
)

type fakeMinter struct{ tokens []int64 }

func (f *fakeMinter) InstallationToken(_ context.Context, id int64) (string, error) {
	f.tokens = append(f.tokens, id)
	return "tok-inst", nil
}

type fakeFetcher struct {
	baseRefs []string
	diff     string
}

func (f *fakeFetcher) Fetch(_ context.Context, _, _, baseRef, _ string) (string, func(), error) {
	f.baseRefs = append(f.baseRefs, baseRef)
	return "/tmp/fake-checkout", func() {}, nil
}

func (f *fakeFetcher) Diff(context.Context, string, string, string) (string, error) {
	return f.diff, nil
}

type fakeReviewer struct {
	results []provenance.VerificationResult
	err     error
	// during simulates work happening while the lease is held (e.g. a new
	// delivery advancing the target).
	during func()
}

func (f *fakeReviewer) Review(context.Context, string, string, string) ([]provenance.VerificationResult, error) {
	if f.during != nil {
		f.during()
	}
	return f.results, f.err
}

type fakeCommentary struct {
	text   string
	tokens int64
	err    error
	calls  int
}

func (f *fakeCommentary) Comment(context.Context, string, string, []provenance.VerificationResult, string) (string, int64, error) {
	f.calls++
	return f.text, f.tokens, f.err
}

type fakeCommenter struct {
	created []string
	updated []int64
	nextID  int64
}

func (f *fakeCommenter) CreateComment(_ context.Context, _, _, _ string, _ int, body string) (int64, error) {
	f.created = append(f.created, body)
	f.nextID++
	return f.nextID + 100, nil
}

func (f *fakeCommenter) UpdateComment(_ context.Context, _, _, _ string, commentID int64, body string) error {
	f.updated = append(f.updated, commentID)
	f.created = append(f.created, body)
	return nil
}

func passResults() []provenance.VerificationResult {
	return []provenance.VerificationResult{
		{Name: "check-secrets", Category: "secret_scan", Status: provenance.StatusPass},
		{Name: "build", Category: "full", Status: provenance.StatusPass},
	}
}

func req() webhook.ReviewRequest {
	return webhook.ReviewRequest{
		InstallationID: 42, Owner: "mediusfy", Repo: "demo", PRNumber: 7,
		HeadSHA: "sha-1", BaseRef: "main",
		CloneURL: "https://github.com/mediusfy/demo.git", DeliveryID: "d1",
	}
}

type fixture struct {
	w        *Worker
	mem      *store.Memory
	q        *queue.Memory
	fetcher  *fakeFetcher
	reviewer *fakeReviewer
	comm     *fakeCommentary
	comments *fakeCommenter
}

func newFixture(aiKey string) *fixture {
	mem := store.NewMemory()
	f := &fixture{
		mem:      mem,
		q:        &queue.Memory{},
		fetcher:  &fakeFetcher{diff: "diff --git a b"},
		reviewer: &fakeReviewer{results: passResults()},
		comm:     &fakeCommentary{text: "looks solid", tokens: 250},
		comments: &fakeCommenter{},
	}
	cfg := tenants.AIConfig{}
	if aiKey != "" {
		cfg = tenants.AIConfig{APIKey: aiKey, Model: "claude-opus-5"}
	}
	f.w = &Worker{
		Jobs: mem, Queue: f.q, Tokens: &fakeMinter{}, Fetcher: f.fetcher,
		Reviewer: f.reviewer, Commentary: f.comm, Comments: f.comments,
		Ledger: mem, Tenants: tenants.Static{42: cfg},
		LeaseTTL: 10 * time.Minute,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

func key() store.JobKey {
	return store.JobKey{InstallationID: 42, Owner: "mediusfy", Repo: "demo", PRNumber: 7}
}

func TestHandle(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(f *fixture)
		request func() webhook.ReviewRequest
		wantErr bool
		verify  func(t *testing.T, f *fixture)
	}{
		{
			name: "fresh PR posts a comment and records usage",
			verify: func(t *testing.T, f *fixture) {
				if len(f.comments.created) != 1 || len(f.comments.updated) != 0 {
					t.Fatalf("created %d updated %d, want 1/0", len(f.comments.created), len(f.comments.updated))
				}
				body := f.comments.created[0]
				for _, want := range []string{"check-secrets", "AI commentary", "looks solid", "sha-1"} {
					if !strings.Contains(body, want) {
						t.Fatalf("comment missing %q:\n%s", want, body)
					}
				}
				job, _ := f.mem.Get(ctx, key())
				if job.Status != store.StatusIdle || job.LastReviewedSHA != "sha-1" || job.CommentID == 0 {
					t.Fatalf("job not completed: %+v", job)
				}
				usage, _ := f.mem.Total(ctx, 42)
				if usage.Reviews != 1 || usage.Tokens != 250 {
					t.Fatalf("usage %+v, want 1 review / 250 tokens", usage)
				}
			},
		},
		{
			name: "second review updates the existing comment",
			prepare: func(f *fixture) {
				_, _ = f.mem.Mutate(ctx, key(), func(j store.Job) store.Job {
					j.LastReviewedSHA = "sha-0"
					j.CommentID = 555
					return j
				})
			},
			verify: func(t *testing.T, f *fixture) {
				if len(f.comments.updated) != 1 || f.comments.updated[0] != 555 {
					t.Fatalf("updated %v, want [555]", f.comments.updated)
				}
				// Incremental: diff base is the last-reviewed SHA, not main.
				if f.fetcher.baseRefs[0] != "sha-0" {
					t.Fatalf("fetch base %q, want sha-0", f.fetcher.baseRefs[0])
				}
			},
		},
		{
			name: "AI failure degrades to an engine-only comment",
			prepare: func(f *fixture) {
				f.comm.err = errors.New("model outage")
			},
			verify: func(t *testing.T, f *fixture) {
				if len(f.comments.created) != 1 {
					t.Fatalf("created %d comments, want 1", len(f.comments.created))
				}
				if strings.Contains(f.comments.created[0], "AI commentary") {
					t.Fatal("comment contains AI section despite failure")
				}
			},
		},
		{
			name: "no AI key means commentary is never called",
			prepare: func(f *fixture) {
				f.w.Tenants = tenants.Static{42: {}}
			},
			verify: func(t *testing.T, f *fixture) {
				if f.comm.calls != 0 {
					t.Fatalf("commentary called %d times, want 0", f.comm.calls)
				}
				if len(f.comments.created) != 1 {
					t.Fatalf("created %d comments, want 1", len(f.comments.created))
				}
			},
		},
		{
			name: "superseded review never posts and enqueues one follow-up",
			prepare: func(f *fixture) {
				f.reviewer.during = func() {
					// A new delivery advances the target while the lease is
					// held: AcquireDecision on a running job.
					_, _ = f.mem.Mutate(ctx, key(), func(j store.Job) store.Job {
						j2, _ := store.AcquireDecision(j, time.Now(), "sha-2", 10*time.Minute)
						return j2
					})
				}
			},
			verify: func(t *testing.T, f *fixture) {
				if len(f.comments.created) != 0 {
					t.Fatalf("posted %d comments for a superseded SHA, want 0", len(f.comments.created))
				}
				tasks := f.q.Tasks()
				if len(tasks) != 1 || tasks[0].HeadSHA != "sha-2" {
					t.Fatalf("follow-ups %+v, want one for sha-2", tasks)
				}
				usage, _ := f.mem.Total(ctx, 42)
				if usage.Reviews != 0 {
					t.Fatalf("superseded review recorded usage: %+v", usage)
				}
			},
		},
		{
			name: "unexpired running lease skips without side effects",
			prepare: func(f *fixture) {
				_, _ = f.mem.Mutate(ctx, key(), func(j store.Job) store.Job {
					j.Status = store.StatusRunning
					j.TargetSHA = "sha-0"
					j.LeaseExpiry = time.Now().Add(5 * time.Minute)
					return j
				})
			},
			verify: func(t *testing.T, f *fixture) {
				if len(f.comments.created) != 0 || len(f.q.Tasks()) != 0 {
					t.Fatal("skip path had side effects")
				}
				job, _ := f.mem.Get(ctx, key())
				if job.TargetSHA != "sha-1" {
					t.Fatalf("target %q, want advanced to sha-1", job.TargetSHA)
				}
			},
		},
		{
			name: "engine failure returns an error and releases the lease",
			prepare: func(f *fixture) {
				f.reviewer.err = errors.New("engine exploded")
			},
			wantErr: true,
			verify: func(t *testing.T, f *fixture) {
				job, _ := f.mem.Get(ctx, key())
				if job.Status != store.StatusIdle {
					t.Fatalf("lease not released after failure: %+v", job)
				}
				if len(f.comments.created) != 0 {
					t.Fatal("comment posted despite engine failure")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture("key-42")
			if tt.prepare != nil {
				tt.prepare(f)
			}
			r := req()
			if tt.request != nil {
				r = tt.request()
			}
			err := f.w.Handle(ctx, r)
			if tt.wantErr != (err != nil) {
				t.Fatalf("Handle error = %v, wantErr %v", err, tt.wantErr)
			}
			tt.verify(t, f)
		})
	}
}

func TestComposeCommentFailedChecks(t *testing.T) {
	tests := []struct {
		name string
		res  []provenance.VerificationResult
		want []string
	}{
		{
			name: "failed checks get a details section",
			res: []provenance.VerificationResult{
				{Name: "check-secrets", Category: "secret_scan", Status: provenance.StatusFail, Message: "token in config"},
			},
			want: []string{"Some checks failed", "<details><summary>check-secrets</summary>", "token in config"},
		},
		{
			name: "all-pass has no failure section",
			res:  passResults(),
			want: []string{"| check-secrets | secret_scan | pass |"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := ComposeComment("abcdef1234567890", tt.res, "")
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("comment missing %q:\n%s", want, body)
				}
			}
			if tt.name == "all-pass has no failure section" && strings.Contains(body, "Some checks failed") {
				t.Fatal("all-pass comment contains failure section")
			}
		})
	}
}

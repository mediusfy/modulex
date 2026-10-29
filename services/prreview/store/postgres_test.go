package store

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// newTestPostgres starts a disposable Postgres container, applies
// migrations/001_init_schema.sql against it, and returns a Postgres store
// wrapping the connection. It skips the test (rather than failing it) when
// no healthy container provider (Docker) is available, so `go test ./...`
// stays runnable without Docker; CI's "make check-nested-modules" job runs
// on GitHub Actions runners, which do have Docker.
func newTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()
	schema, err := os.ReadFile("migrations/001_init_schema.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	schemaFile := filepathJoinTemp(t, schema)

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("prreview_test"),
		tcpostgres.WithUsername("prreview"),
		tcpostgres.WithPassword("prreview"),
		tcpostgres.WithInitScripts(schemaFile),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return &Postgres{DB: db}
}

// filepathJoinTemp copies schema into a temp file so it can be passed to
// tcpostgres.WithInitScripts, which takes host file paths rather than raw
// SQL.
func filepathJoinTemp(t *testing.T, schema []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/001_init_schema.sql"
	if err := os.WriteFile(path, schema, 0o600); err != nil {
		t.Fatalf("write schema temp file: %v", err)
	}
	return path
}

func newJobKey(suffix string) JobKey {
	return JobKey{InstallationID: 1, Owner: "o", Repo: "r-" + suffix, PRNumber: 1}
}

func testPostgresJobStoreGetAbsentIsZeroValue(t *testing.T, p *Postgres) {
	ctx := context.Background()

	got, err := p.Get(ctx, newJobKey("absent"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != (Job{}) {
		t.Fatalf("got %+v, want zero value", got)
	}
}

func testPostgresJobStoreMutatePersistsAcrossGet(t *testing.T, p *Postgres) {
	ctx := context.Background()
	key := newJobKey("persist")

	now := t0
	owner := "w1"
	got, action := AcquireDecision(Job{}, now, "sha1", leaseTTL, owner)
	if action != ActionRun {
		t.Fatalf("action = %v, want ActionRun", action)
	}

	mutated, err := p.Mutate(ctx, key, func(Job) Job { return got })
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if mutated != got {
		t.Fatalf("mutate returned %+v, want %+v", mutated, got)
	}

	fromGet, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fromGet != got {
		t.Fatalf("get returned %+v, want %+v", fromGet, got)
	}
}

func testPostgresJobStoreLeaseExpiryRoundTripsZeroAndNonZero(t *testing.T, p *Postgres) {
	ctx := context.Background()
	key := newJobKey("lease-roundtrip")

	leased := Job{Status: StatusRunning, TargetSHA: "sha1", LeaseExpiry: t0.Add(leaseTTL), LeaseOwner: "w1"}
	if _, err := p.Mutate(ctx, key, func(Job) Job { return leased }); err != nil {
		t.Fatalf("mutate (leased): %v", err)
	}
	got, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.LeaseExpiry.Equal(leased.LeaseExpiry) {
		t.Fatalf("lease expiry = %v, want %v", got.LeaseExpiry, leased.LeaseExpiry)
	}

	released := Job{Status: StatusIdle, TargetSHA: "sha1"}
	if _, err := p.Mutate(ctx, key, func(Job) Job { return released }); err != nil {
		t.Fatalf("mutate (released): %v", err)
	}
	got, err = p.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.LeaseExpiry.IsZero() {
		t.Fatalf("lease expiry = %v, want zero", got.LeaseExpiry)
	}
}

// testPostgresJobStoreMutateSerializesConcurrentIncrements drives many
// concurrent Mutate calls against one brand-new key (no row yet exists) and
// asserts every increment survives. A naive upsert (read, compute, write
// with no lock) would lose increments here because two concurrent calls
// can both read "row absent" before either writes; this exercises exactly
// the race the Postgres doc comment's advisory-lock rationale describes.
func testPostgresJobStoreMutateSerializesConcurrentIncrements(t *testing.T, p *Postgres) {
	ctx := context.Background()
	key := newJobKey("concurrent-increment")

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Mutate(ctx, key, func(j Job) Job {
				j.ReviewCount++
				return j
			}); err != nil {
				t.Errorf("mutate: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ReviewCount != n {
		t.Fatalf("review count = %d, want %d", got.ReviewCount, n)
	}
}

// testPostgresJobStoreLeaseSerializesConcurrentDeliveries mirrors
// TestLeaseSerializesConcurrentDeliveries (store_test.go) against Postgres
// instead of Memory, so the same lease invariants (exactly one runner,
// never post a superseded SHA, last-reviewed catches up to the final
// target) are verified against the real adapter, not just the pure
// decision functions.
func testPostgresJobStoreLeaseSerializesConcurrentDeliveries(t *testing.T, p *Postgres) {
	ctx := context.Background()
	key := newJobKey("lease-cycle")

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
		_, err := p.Mutate(ctx, key, func(j Job) Job {
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
			deliverPostgres(ctx, t, p, key, outcome.FollowUpSHA, review)
		}
	}

	deliveries := []string{"s1", "s2", "s3", "s4", "s5"}
	var wg sync.WaitGroup
	for _, sha := range deliveries {
		wg.Add(1)
		go func(sha string) {
			defer wg.Done()
			deliverPostgres(ctx, t, p, key, sha, review)
		}(sha)
	}
	wg.Wait()

	if maxRunning != 1 {
		t.Fatalf("max concurrent runners = %d, want 1", maxRunning)
	}
	final, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
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
}

func deliverPostgres(ctx context.Context, t *testing.T, p *Postgres, key JobKey, sha string, review func(sha, owner string)) {
	t.Helper()
	owner := "owner-" + sha
	var action AcquireAction
	job, err := p.Mutate(ctx, key, func(j Job) Job {
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

func testPostgresDedupStoreSeen(t *testing.T, p *Postgres) {
	ctx := context.Background()

	tests := []struct {
		name string
		run  func(t *testing.T, p *Postgres)
	}{
		{
			name: "second sighting within TTL is seen",
			run: func(t *testing.T, p *Postgres) {
				seen, err := p.Seen(ctx, "d1", time.Hour)
				if err != nil || seen {
					t.Fatalf("first sighting: seen=%v err=%v, want false/nil", seen, err)
				}
				seen, err = p.Seen(ctx, "d1", time.Hour)
				if err != nil || !seen {
					t.Fatalf("second sighting: seen=%v err=%v, want true/nil", seen, err)
				}
			},
		},
		{
			name: "expired record is not seen and re-records",
			run: func(t *testing.T, p *Postgres) {
				now := t0
				p.Now = func() time.Time { return now }
				if _, err := p.Seen(ctx, "d2", time.Minute); err != nil {
					t.Fatalf("first sighting: %v", err)
				}
				now = t0.Add(2 * time.Minute)
				seen, err := p.Seen(ctx, "d2", time.Minute)
				if err != nil || seen {
					t.Fatalf("after expiry: seen=%v err=%v, want false/nil", seen, err)
				}
			},
		},
		{
			name: "forgotten record is not seen",
			run: func(t *testing.T, p *Postgres) {
				if _, err := p.Seen(ctx, "d3", time.Hour); err != nil {
					t.Fatalf("first sighting: %v", err)
				}
				if err := p.Forget(ctx, "d3"); err != nil {
					t.Fatalf("forget: %v", err)
				}
				seen, err := p.Seen(ctx, "d3", time.Hour)
				if err != nil || seen {
					t.Fatalf("after forget: seen=%v err=%v, want false/nil", seen, err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, p)
		})
	}
}

func testPostgresLedgerAddAccumulatesAndTotalReads(t *testing.T, p *Postgres) {
	ctx := context.Background()

	got, err := p.Total(ctx, 42)
	if err != nil {
		t.Fatalf("total (absent): %v", err)
	}
	if got != (Usage{}) {
		t.Fatalf("total (absent) = %+v, want zero value", got)
	}

	if err := p.Add(ctx, 42, 1, 100); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := p.Add(ctx, 42, 2, 250); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err = p.Total(ctx, 42)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if want := (Usage{Reviews: 3, Tokens: 350}); got != want {
		t.Fatalf("total = %+v, want %+v", got, want)
	}
}

// TestPostgres runs each scenario against a fresh Postgres wrapper over one
// shared container (newTestPostgres starts it once). Scenarios use distinct
// keys, so they do not interfere.
func TestPostgres(t *testing.T) {
	shared := newTestPostgres(t)
	tests := []struct {
		name string
		run  func(t *testing.T, p *Postgres)
	}{
		{"job store GetAbsentIsZeroValue", testPostgresJobStoreGetAbsentIsZeroValue},
		{"job store MutatePersistsAcrossGet", testPostgresJobStoreMutatePersistsAcrossGet},
		{"job store LeaseExpiryRoundTripsZeroAndNonZero", testPostgresJobStoreLeaseExpiryRoundTripsZeroAndNonZero},
		{"job store MutateSerializesConcurrentIncrements", testPostgresJobStoreMutateSerializesConcurrentIncrements},
		{"job store LeaseSerializesConcurrentDeliveries", testPostgresJobStoreLeaseSerializesConcurrentDeliveries},
		{"dedup store Seen", testPostgresDedupStoreSeen},
		{"ledger Add accumulates and Total reads", testPostgresLedgerAddAccumulatesAndTotalReads},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, &Postgres{DB: shared.DB})
		})
	}
}

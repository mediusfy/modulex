package modtest

import (
	"context"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
)

// respectingStart blocks until ctx is cancelled (or a long safety timeout
// elapses), returning promptly once cancellation is observed — a
// well-behaved implementation of an interruptible Start.
func respectingStart(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return nil
	}
}

// ignoringStart sleeps for a fixed duration regardless of ctx, simulating a
// module that ignores cancellation/deadlines — the exact regression
// AssertRespectsCancellation/AssertRespectsDeadline exist to catch. The
// sleep is kept short (well above the grace periods used below, but well
// below any suite-level timeout) so a failing test still completes quickly
// and the background goroutine it leaks terminates on its own.
func ignoringStart(ctx context.Context) error {
	time.Sleep(150 * time.Millisecond)
	return nil
}

func TestAssertRespectsCancellation_WellBehaved(t *testing.T) {
	mod := starterFixture{
		fixtureModule: &fixtureModule{name: "cancel-well-behaved"},
		startFn:       respectingStart,
	}

	ft := runFake(func(t TB) { AssertRespectsCancellation(t, mod, PhaseStart, 100*time.Millisecond) })
	if ft.failed {
		t.Fatalf("AssertRespectsCancellation reported a failure for a module that promptly honors ctx.Done(): %v", ft.logs)
	}
}

// TestAssertRespectsCancellation_DetectsRegression proves
// AssertRespectsCancellation correctly fails against a real module that
// ignores context cancellation, not just against synthetic data.
func TestAssertRespectsCancellation_DetectsRegression(t *testing.T) {
	mod := starterFixture{
		fixtureModule: &fixtureModule{name: "cancel-broken"},
		startFn:       ignoringStart,
	}

	ft := runFake(func(t TB) { AssertRespectsCancellation(t, mod, PhaseStart, 30*time.Millisecond) })
	if !ft.failed {
		t.Fatalf("AssertRespectsCancellation did not detect a module that ignores context cancellation")
	}
}

func TestAssertRespectsDeadline_WellBehaved(t *testing.T) {
	mod := starterFixture{
		fixtureModule: &fixtureModule{name: "deadline-well-behaved"},
		startFn:       respectingStart,
	}

	ft := runFake(func(t TB) { AssertRespectsDeadline(t, mod, PhaseStart, 20*time.Millisecond, 100*time.Millisecond) })
	if ft.failed {
		t.Fatalf("AssertRespectsDeadline reported a failure for a module that promptly honors its context deadline: %v", ft.logs)
	}
}

// TestAssertRespectsDeadline_DetectsRegression proves AssertRespectsDeadline
// correctly fails against a real module that ignores its context deadline.
func TestAssertRespectsDeadline_DetectsRegression(t *testing.T) {
	mod := starterFixture{
		fixtureModule: &fixtureModule{name: "deadline-broken"},
		startFn:       ignoringStart,
	}

	ft := runFake(func(t TB) { AssertRespectsDeadline(t, mod, PhaseStart, 10*time.Millisecond, 20*time.Millisecond) })
	if !ft.failed {
		t.Fatalf("AssertRespectsDeadline did not detect a module that ignores its context deadline")
	}
}

func TestAssertRespectsCancellation_InitPhase(t *testing.T) {
	mod := &fixtureModule{
		name: "cancel-init-well-behaved",
		initFn: func(ctx context.Context, reg modulex.Registry) error {
			return respectingStart(ctx)
		},
	}

	ft := runFake(func(t TB) { AssertRespectsCancellation(t, mod, PhaseInit, 100*time.Millisecond) })
	if ft.failed {
		t.Fatalf("AssertRespectsCancellation reported a failure for an Init that promptly honors ctx.Done(): %v", ft.logs)
	}
}

func TestAssertRespectsCancellation_StopPhase(t *testing.T) {
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "cancel-stop-well-behaved"},
		stopFn:        respectingStart,
	}

	ft := runFake(func(t TB) { AssertRespectsCancellation(t, mod, PhaseStop, 100*time.Millisecond) })
	if ft.failed {
		t.Fatalf("AssertRespectsCancellation reported a failure for a Stop that promptly honors ctx.Done(): %v", ft.logs)
	}
}

func TestAssertRespectsCancellation_RequiresSupportedPhase(t *testing.T) {
	mod := &fixtureModule{name: "no-starter"}

	ft := runFake(func(t TB) { AssertRespectsCancellation(t, mod, PhaseStart, 50*time.Millisecond) })
	if !ft.failed {
		t.Fatalf("AssertRespectsCancellation should fail when asked to test PhaseStart against a module with no Starter")
	}
}

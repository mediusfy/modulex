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

func TestAssertRespectsCancellation(t *testing.T) {
	tests := []struct {
		name     string
		mod      modulex.Module
		phase    Phase
		grace    time.Duration
		wantFail bool
	}{
		{
			name:  "well-behaved Start honors ctx.Done",
			mod:   starterFixture{fixtureModule: &fixtureModule{name: "cancel-well-behaved"}, startFn: respectingStart},
			phase: PhaseStart,
			grace: 100 * time.Millisecond,
		},
		{
			// Proves the helper fails against a real module that ignores
			// context cancellation, not just against synthetic data.
			name:     "Start ignoring cancellation is detected",
			mod:      starterFixture{fixtureModule: &fixtureModule{name: "cancel-broken"}, startFn: ignoringStart},
			phase:    PhaseStart,
			grace:    30 * time.Millisecond,
			wantFail: true,
		},
		{
			name: "well-behaved Init honors ctx.Done",
			mod: &fixtureModule{
				name: "cancel-init-well-behaved",
				initFn: func(ctx context.Context, reg modulex.Registry) error {
					return respectingStart(ctx)
				},
			},
			phase: PhaseInit,
			grace: 100 * time.Millisecond,
		},
		{
			name: "well-behaved Stop honors ctx.Done",
			mod: fullFixture{
				fixtureModule: &fixtureModule{name: "cancel-stop-well-behaved"},
				stopFn:        respectingStart,
			},
			phase: PhaseStop,
			grace: 100 * time.Millisecond,
		},
		{
			name:     "PhaseStart against a module with no Starter fails",
			mod:      &fixtureModule{name: "no-starter"},
			phase:    PhaseStart,
			grace:    50 * time.Millisecond,
			wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := runFake(func(t TB) { AssertRespectsCancellation(t, tt.mod, tt.phase, tt.grace) })
			if ft.failed != tt.wantFail {
				t.Fatalf("AssertRespectsCancellation failed = %v, want %v: %v", ft.failed, tt.wantFail, ft.logs)
			}
		})
	}
}

func TestAssertRespectsDeadline(t *testing.T) {
	tests := []struct {
		name     string
		mod      modulex.Module
		deadline time.Duration
		grace    time.Duration
		wantFail bool
	}{
		{
			name:     "well-behaved Start honors its deadline",
			mod:      starterFixture{fixtureModule: &fixtureModule{name: "deadline-well-behaved"}, startFn: respectingStart},
			deadline: 20 * time.Millisecond,
			grace:    100 * time.Millisecond,
		},
		{
			name:     "Start ignoring its deadline is detected",
			mod:      starterFixture{fixtureModule: &fixtureModule{name: "deadline-broken"}, startFn: ignoringStart},
			deadline: 10 * time.Millisecond,
			grace:    20 * time.Millisecond,
			wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := runFake(func(t TB) { AssertRespectsDeadline(t, tt.mod, PhaseStart, tt.deadline, tt.grace) })
			if ft.failed != tt.wantFail {
				t.Fatalf("AssertRespectsDeadline failed = %v, want %v: %v", ft.failed, tt.wantFail, ft.logs)
			}
		})
	}
}

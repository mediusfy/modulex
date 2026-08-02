package modtest

import (
	"context"
	"time"

	"github.com/mediusfy/modulex"
)

// setupForPhase constructs a fresh *modulex.Manager and, for phase ==
// PhaseStart or PhaseStop, drives whichever earlier lifecycle methods are
// needed to reach the state phase requires (Init before Start, Init and
// Start before Stop), all with context.Background() — those setup calls are
// not what is under test, so they are given an uncancellable context and
// any failure is treated as a harness setup error (t.Fatalf), not a
// cancellation-handling regression in mod.
func setupForPhase(t TB, mod modulex.Module, phase Phase) modulex.Registry {
	t.Helper()

	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	if phase == PhaseInit {
		return mgr
	}

	if err := mod.Init(context.Background(), mgr); err != nil {
		t.Fatalf("modtest: setup Init failed: %v", err)
	}
	if phase == PhaseStart {
		return mgr
	}

	if s, ok := mod.(modulex.Starter); ok {
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("modtest: setup Start failed: %v", err)
		}
	}
	return mgr
}

// requirePhaseSupported fails the test (via t.Fatalf) if mod does not
// implement the optional interface phase requires (modulex.Starter for
// PhaseStart, modulex.Stopper for PhaseStop). It must be called from the
// main test goroutine, before invokePhase runs in its own goroutine below —
// t.Fatalf/t.FailNow is only safe to call from the goroutine running the
// test function, so this check cannot live inside invokePhase itself once
// invokePhase is running concurrently with the test.
func requirePhaseSupported(t TB, mod modulex.Module, phase Phase) {
	t.Helper()
	switch phase {
	case PhaseInit:
		// Init is part of the required modulex.Module interface, not an
		// optional one — every mod already satisfies it, so there is
		// nothing to check.
	case PhaseStart:
		if _, ok := mod.(modulex.Starter); !ok {
			t.Fatalf("modtest: module %q does not implement modulex.Starter; cannot test PhaseStart", mod.Name())
		}
	case PhaseStop:
		if _, ok := mod.(modulex.Stopper); !ok {
			t.Fatalf("modtest: module %q does not implement modulex.Stopper; cannot test PhaseStop", mod.Name())
		}
	default:
		panic("unhandled default case")
	}
}

// invokePhase calls the lifecycle method identified by phase on mod with
// ctx, using reg as the Registry for PhaseInit. Callers must have already
// called requirePhaseSupported for the same mod/phase from the main test
// goroutine; invokePhase itself performs no test-failure calls so that it
// is safe to run from a background goroutine (see AssertRespectsCancellation
// and AssertRespectsDeadline).
func invokePhase(mod modulex.Module, phase Phase, ctx context.Context, reg modulex.Registry) error {
	switch phase {
	case PhaseInit:
		return mod.Init(ctx, reg)
	case PhaseStart:
		return mod.(modulex.Starter).Start(ctx)
	case PhaseStop:
		return mod.(modulex.Stopper).Stop(ctx)
	default:
		return nil
	}
}

// AssertRespectsCancellation asserts that mod's Init, Start, or Stop
// (selected by phase) returns promptly once its context is cancelled
// mid-call.
//
// It drives whatever earlier lifecycle phases are needed (see
// setupForPhase) with an uncancellable context, then invokes the phase
// under test in a separate goroutine with a context that is cancelled
// immediately after the call is made. If the call has not returned within
// grace after cancellation, AssertRespectsCancellation fails the test via
// t.Errorf, reporting a likely cancellation regression.
//
// Note: AssertRespectsCancellation drives mod's lifecycle method directly
// rather than through Manager.InitModules/StartModules/StopModules. This is
// deliberate: those Manager methods only check ctx.Err() BEFORE invoking
// each module in sequence, so calling them with an already-cancelled
// context would abort before ever calling the module under test's method —
// a false pass that would never actually exercise the module's own
// cancellation handling. Calling the method directly is the only way to
// genuinely probe whether a specific module's Init/Start/Stop observes
// ctx.Done() while it is running.
//
// This is fully generic: it requires no cooperation from mod beyond the
// standard modulex.Module/Starter/Stopper interfaces, though the module
// must actually block on something for cancellation to have anything to
// interrupt — a module whose Init/Start/Stop always returns quickly
// regardless of ctx trivially "passes" this assertion without ever being
// meaningfully exercised.
func AssertRespectsCancellation(t TB, mod modulex.Module, phase Phase, grace time.Duration) {
	t.Helper()

	reg := setupForPhase(t, mod, phase)
	requirePhaseSupported(t, mod, phase)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- invokePhase(mod, phase, ctx, reg)
	}()

	cancel()

	select {
	case <-done:
		// Returned in time; the error value (if any) is not itself
		// significant here — respecting cancellation promptly is what this
		// assertion verifies, not any particular return value.
	case <-time.After(grace):
		t.Errorf("modtest: module %q did not return from %s within %s after its context was cancelled; it may be ignoring ctx.Done()", mod.Name(), phase, grace)
	}
}

// AssertRespectsDeadline asserts that mod's Init, Start, or Stop (selected
// by phase) returns promptly once its context's deadline elapses.
//
// It behaves like AssertRespectsCancellation, except the context under test
// is given a fixed deadline (context.WithTimeout(ctx, deadline)) instead of
// being cancelled explicitly, and the grace period is measured from when
// the deadline elapses. See AssertRespectsCancellation's doc comment for why
// this drives mod's lifecycle method directly rather than through the
// Manager, and for the same genericity caveat (the module must actually
// block on something past the deadline for this to be a meaningful check).
func AssertRespectsDeadline(t TB, mod modulex.Module, phase Phase, deadline, grace time.Duration) {
	t.Helper()

	reg := setupForPhase(t, mod, phase)
	requirePhaseSupported(t, mod, phase)

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- invokePhase(mod, phase, ctx, reg)
	}()

	select {
	case <-done:
	case <-time.After(deadline + grace):
		t.Errorf("modtest: module %q did not return from %s within %s of its context deadline (%s) elapsing; it may be ignoring the deadline", mod.Name(), phase, grace, deadline)
	}
}

package modtest

import (
	"context"
	"testing"
)

func TestAssertRollbackOnInitFailure_Passes(t *testing.T) {
	var stopped bool
	mod := stopperFixture{
		fixtureModule: &fixtureModule{name: "rollback-init-target"},
		stopFn:        func(ctx context.Context) error { stopped = true; return nil },
	}

	ft := runFake(func(t TB) { AssertRollbackOnInitFailure(t, mod) })
	if ft.failed {
		t.Fatalf("AssertRollbackOnInitFailure reported a failure for a module whose Stop correctly ran during rollback: %v", ft.logs)
	}
	if !stopped {
		t.Fatalf("test setup error: module's Stop was never actually invoked")
	}
}

func TestAssertRollbackOnStartFailure_Passes(t *testing.T) {
	var stopped bool
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "rollback-start-target"},
		stopFn:        func(ctx context.Context) error { stopped = true; return nil },
	}

	ft := runFake(func(t TB) { AssertRollbackOnStartFailure(t, mod) })
	if ft.failed {
		t.Fatalf("AssertRollbackOnStartFailure reported a failure for a module whose Stop correctly ran during rollback: %v", ft.logs)
	}
	if !stopped {
		t.Fatalf("test setup error: module's Stop was never actually invoked")
	}
}

func TestAssertRollback_NoStopperIsNotAFailure(t *testing.T) {
	mod := &fixtureModule{name: "no-stopper"}

	ft := runFake(func(t TB) { AssertRollbackOnInitFailure(t, mod) })
	if ft.failed {
		t.Fatalf("AssertRollbackOnInitFailure should not fail merely because the module under test does not implement modulex.Stopper: %v", ft.logs)
	}
}

// TestAssertStoppedIfStopper_DetectsMissingStop proves the rollback
// helpers' cleanup check correctly fails when a module implements
// modulex.Stopper but its Stop was never recorded — the actual regression
// AssertRollbackOnInitFailure/AssertRollbackOnStartFailure exist to catch.
// It calls assertStoppedIfStopper directly against a deliberately
// incomplete OrderRecorder rather than trying to force a real Manager bug.
func TestAssertStoppedIfStopper_DetectsMissingStop(t *testing.T) {
	mod := stopperFixture{fixtureModule: &fixtureModule{name: "leaky"}}
	rec := NewOrderRecorder() // Stop deliberately never recorded.

	ft := runFake(func(t TB) { assertStoppedIfStopper(t, mod, rec) })
	if !ft.failed {
		t.Fatalf("assertStoppedIfStopper did not detect that Stop was never recorded for a modulex.Stopper module")
	}
}

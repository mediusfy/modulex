package modtest

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex"
)

func TestOrderRecorderIndex(t *testing.T) {
	rec := NewOrderRecorder()
	rec.record("a", "Init")
	rec.record("b", "Init")
	rec.record("a", "Start")

	if got := rec.Index("a", "Init"); got != 0 {
		t.Errorf("Index(a, Init) = %d, want 0", got)
	}
	if got := rec.Index("b", "Init"); got != 1 {
		t.Errorf("Index(b, Init) = %d, want 1", got)
	}
	if got := rec.Index("a", "Start"); got != 2 {
		t.Errorf("Index(a, Start) = %d, want 2", got)
	}
	if got := rec.Index("b", "Start"); got != -1 {
		t.Errorf("Index(b, Start) = %d, want -1", got)
	}

	events := rec.Events()
	if len(events) != 3 {
		t.Fatalf("Events() returned %d events, want 3", len(events))
	}
	if events[0] != (Event{Module: "a", Phase: "Init"}) {
		t.Errorf("Events()[0] = %+v, want {a Init}", events[0])
	}
}

func TestWrapPreservesOptionalInterfaces(t *testing.T) {
	rec := NewOrderRecorder()

	base := &fixtureModule{name: "base"}
	wrappedBase := Wrap(base, rec)
	if _, ok := wrappedBase.(modulex.Starter); ok {
		t.Errorf("Wrap(base-only module) unexpectedly implements modulex.Starter")
	}
	if _, ok := wrappedBase.(modulex.Stopper); ok {
		t.Errorf("Wrap(base-only module) unexpectedly implements modulex.Stopper")
	}

	starter := starterFixture{fixtureModule: &fixtureModule{name: "starter"}}
	wrappedStarter := Wrap(starter, rec)
	if _, ok := wrappedStarter.(modulex.Starter); !ok {
		t.Errorf("Wrap(starter module) does not implement modulex.Starter")
	}
	if _, ok := wrappedStarter.(modulex.Stopper); ok {
		t.Errorf("Wrap(starter-only module) unexpectedly implements modulex.Stopper")
	}

	stopper := stopperFixture{fixtureModule: &fixtureModule{name: "stopper"}}
	wrappedStopper := Wrap(stopper, rec)
	if _, ok := wrappedStopper.(modulex.Starter); ok {
		t.Errorf("Wrap(stopper-only module) unexpectedly implements modulex.Starter")
	}
	if _, ok := wrappedStopper.(modulex.Stopper); !ok {
		t.Errorf("Wrap(stopper module) does not implement modulex.Stopper")
	}

	full := fullFixture{fixtureModule: &fixtureModule{name: "full"}}
	wrappedFull := Wrap(full, rec)
	if _, ok := wrappedFull.(modulex.Starter); !ok {
		t.Errorf("Wrap(full module) does not implement modulex.Starter")
	}
	if _, ok := wrappedFull.(modulex.Stopper); !ok {
		t.Errorf("Wrap(full module) does not implement modulex.Stopper")
	}
}

func TestWrapRecordsCallsAndDelegates(t *testing.T) {
	rec := NewOrderRecorder()
	var initCalled, startCalled, stopCalled bool

	full := fullFixture{
		fixtureModule: &fixtureModule{
			name: "wired",
			initFn: func(ctx context.Context, reg modulex.Registry) error {
				initCalled = true
				return nil
			},
		},
		startFn: func(ctx context.Context) error { startCalled = true; return nil },
		stopFn:  func(ctx context.Context) error { stopCalled = true; return nil },
	}

	wrapped := Wrap(full, rec)
	if err := wrapped.Init(context.Background(), nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := wrapped.(modulex.Starter).Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := wrapped.(modulex.Stopper).Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !initCalled || !startCalled || !stopCalled {
		t.Fatalf("Wrap did not delegate all calls: init=%v start=%v stop=%v", initCalled, startCalled, stopCalled)
	}

	wantEvents := []Event{
		{Module: "wired", Phase: "Init"},
		{Module: "wired", Phase: "Start"},
		{Module: "wired", Phase: "Stop"},
	}
	events := rec.Events()
	if len(events) != len(wantEvents) {
		t.Fatalf("Events() = %+v, want %+v", events, wantEvents)
	}
	for i, want := range wantEvents {
		if events[i] != want {
			t.Errorf("Events()[%d] = %+v, want %+v", i, events[i], want)
		}
	}
}

func TestBootRunsInitAndStartAndCleansUp(t *testing.T) {
	var started, stopped bool
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "boot-test"},
		startFn:       func(ctx context.Context) error { started = true; return nil },
		stopFn:        func(ctx context.Context) error { stopped = true; return nil },
	}

	t.Run("boot", func(t *testing.T) {
		mgr := Boot(t, mod)
		if mgr.State() != modulex.StateRunning {
			t.Errorf("Boot: manager state = %v, want %v", mgr.State(), modulex.StateRunning)
		}
		if !started {
			t.Errorf("Boot did not call Start")
		}
	})

	if !stopped {
		t.Errorf("Boot's t.Cleanup did not call Stop after the subtest finished")
	}
}

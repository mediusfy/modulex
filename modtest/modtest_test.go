package modtest

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex"
)

func TestOrderRecorderIndex(t *testing.T) {
	tests := []struct {
		name      string
		module    string
		phase     string
		wantIndex int
	}{
		{name: "a Init", module: "a", phase: "Init", wantIndex: 0},
		{name: "b Init", module: "b", phase: "Init", wantIndex: 1},
		{name: "a Start", module: "a", phase: "Start", wantIndex: 2},
		{name: "b Start never recorded", module: "b", phase: "Start", wantIndex: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := NewOrderRecorder()
			rec.record("a", "Init")
			rec.record("b", "Init")
			rec.record("a", "Start")

			if got := rec.Index(tt.module, tt.phase); got != tt.wantIndex {
				t.Errorf("Index(%s, %s) = %d, want %d", tt.module, tt.phase, got, tt.wantIndex)
			}

			events := rec.Events()
			if len(events) != 3 {
				t.Fatalf("Events() returned %d events, want 3", len(events))
			}
			if events[0] != (Event{Module: "a", Phase: "Init"}) {
				t.Errorf("Events()[0] = %+v, want {a Init}", events[0])
			}
		})
	}
}

func TestWrapPreservesOptionalInterfaces(t *testing.T) {
	tests := []struct {
		name        string
		mod         modulex.Module
		wantStarter bool
		wantStopper bool
	}{
		{name: "base-only", mod: &fixtureModule{name: "base"}},
		{name: "starter-only", mod: starterFixture{fixtureModule: &fixtureModule{name: "starter"}}, wantStarter: true},
		{name: "stopper-only", mod: stopperFixture{fixtureModule: &fixtureModule{name: "stopper"}}, wantStopper: true},
		{name: "full", mod: fullFixture{fixtureModule: &fixtureModule{name: "full"}}, wantStarter: true, wantStopper: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := Wrap(tt.mod, NewOrderRecorder())
			if _, ok := wrapped.(modulex.Starter); ok != tt.wantStarter {
				t.Errorf("Wrap(%s module) implements modulex.Starter = %v, want %v", tt.name, ok, tt.wantStarter)
			}
			if _, ok := wrapped.(modulex.Stopper); ok != tt.wantStopper {
				t.Errorf("Wrap(%s module) implements modulex.Stopper = %v, want %v", tt.name, ok, tt.wantStopper)
			}
		})
	}
}

func TestWrapRecordsCallsAndDelegates(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "full module records and delegates Init Start Stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

func TestBootRunsInitAndStartAndCleansUp(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "boot-test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

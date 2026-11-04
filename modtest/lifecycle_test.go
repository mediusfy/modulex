package modtest

import (
	"testing"

	"github.com/mediusfy/modulex"
)

func TestAssertLifecycleOrder(t *testing.T) {
	tests := []struct {
		name string
		mods []modulex.Module
	}{
		{
			name: "well-behaved dependency graph",
			mods: []modulex.Module{
				fullFixture{fixtureModule: &fixtureModule{name: "base"}},
				fullFixture{fixtureModule: &fixtureModule{name: "dependent", deps: []string{"base"}}},
			},
		},
		{
			name: "single module with no Starter/Stopper",
			mods: []modulex.Module{&fixtureModule{name: "solo"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := runFake(func(t TB) { AssertLifecycleOrder(t, tt.mods...) })
			if ft.failed {
				t.Fatalf("AssertLifecycleOrder reported a failure for a well-behaved module set: %v", ft.logs)
			}
		})
	}
}

// TestCheckLifecycleOrder_DetectsViolations proves checkLifecycleOrder
// itself correctly fails when handed a synthetic, deliberately broken event
// sequence. It seeds an OrderRecorder directly (rather than driving a real
// Manager) to simulate lifecycle-ordering and shutdown-ordering regressions
// that a correct Manager should never actually produce, so the detection
// logic is proven independent of relying on an accidental Manager bug.
func TestCheckLifecycleOrder_DetectsViolations(t *testing.T) {
	dep := &fixtureModule{name: "dep"}
	dependent := &fixtureModule{name: "dependent", deps: []string{"dep"}}
	solo := []modulex.Module{&fixtureModule{name: "solo"}}

	tests := []struct {
		name   string
		events []Event
		mods   []modulex.Module
	}{
		{
			name:   "start-before-init",
			events: []Event{{"solo", "Start"}, {"solo", "Init"}},
			mods:   solo,
		},
		{
			name:   "stop-before-start",
			events: []Event{{"solo", "Init"}, {"solo", "Stop"}, {"solo", "Start"}},
			mods:   solo,
		},
		{
			name:   "dependency-initialized-after-dependent",
			events: []Event{{"dependent", "Init"}, {"dep", "Init"}},
			mods:   []modulex.Module{dep, dependent},
		},
		{
			name:   "dependency-stopped-before-dependent",
			events: []Event{{"dep", "Init"}, {"dependent", "Init"}, {"dep", "Stop"}, {"dependent", "Stop"}},
			mods:   []modulex.Module{dep, dependent},
		},
		{
			name: "init-never-recorded",
			mods: solo,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := NewOrderRecorder()
			for _, e := range tt.events {
				rec.record(e.Module, e.Phase)
			}
			ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, tt.mods) })
			if !ft.failed {
				t.Fatalf("checkLifecycleOrder did not detect violation %q", tt.name)
			}
		})
	}
}

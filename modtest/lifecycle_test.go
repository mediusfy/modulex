package modtest

import (
	"testing"

	"github.com/mediusfy/modulex"
)

func TestAssertLifecycleOrder_WellBehaved(t *testing.T) {
	base := fullFixture{fixtureModule: &fixtureModule{name: "base"}}
	dependent := fullFixture{fixtureModule: &fixtureModule{name: "dependent", deps: []string{"base"}}}

	ft := runFake(func(t TB) { AssertLifecycleOrder(t, base, dependent) })
	if ft.failed {
		t.Fatalf("AssertLifecycleOrder reported a failure for a well-behaved dependency graph: %v", ft.logs)
	}
}

func TestAssertLifecycleOrder_SingleModuleNoDeps(t *testing.T) {
	mod := &fixtureModule{name: "solo"}
	ft := runFake(func(t TB) { AssertLifecycleOrder(t, mod) })
	if ft.failed {
		t.Fatalf("AssertLifecycleOrder reported a failure for a single module with no Starter/Stopper: %v", ft.logs)
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

	t.Run("start-before-init", func(t *testing.T) {
		rec := NewOrderRecorder()
		rec.record("solo", "Start")
		rec.record("solo", "Init")
		ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, []modulex.Module{&fixtureModule{name: "solo"}}) })
		if !ft.failed {
			t.Fatalf("checkLifecycleOrder did not detect Start recorded before Init")
		}
	})

	t.Run("stop-before-start", func(t *testing.T) {
		rec := NewOrderRecorder()
		rec.record("solo", "Init")
		rec.record("solo", "Stop")
		rec.record("solo", "Start")
		ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, []modulex.Module{&fixtureModule{name: "solo"}}) })
		if !ft.failed {
			t.Fatalf("checkLifecycleOrder did not detect Stop recorded before Start")
		}
	})

	t.Run("dependency-initialized-after-dependent", func(t *testing.T) {
		rec := NewOrderRecorder()
		rec.record("dependent", "Init")
		rec.record("dep", "Init")
		ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, []modulex.Module{dep, dependent}) })
		if !ft.failed {
			t.Fatalf("checkLifecycleOrder did not detect a dependency initialized after its dependent")
		}
	})

	t.Run("dependency-stopped-before-dependent", func(t *testing.T) {
		rec := NewOrderRecorder()
		rec.record("dep", "Init")
		rec.record("dependent", "Init")
		rec.record("dep", "Stop")
		rec.record("dependent", "Stop")
		ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, []modulex.Module{dep, dependent}) })
		if !ft.failed {
			t.Fatalf("checkLifecycleOrder did not detect a dependency stopped before its dependent (expected reverse teardown order)")
		}
	})

	t.Run("init-never-recorded", func(t *testing.T) {
		rec := NewOrderRecorder()
		ft := runFake(func(t TB) { checkLifecycleOrder(t, rec, []modulex.Module{&fixtureModule{name: "solo"}}) })
		if !ft.failed {
			t.Fatalf("checkLifecycleOrder did not detect a module whose Init was never recorded")
		}
	})
}

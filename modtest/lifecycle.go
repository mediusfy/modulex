package modtest

import (
	"context"

	"github.com/mediusfy/modulex"
)

// AssertLifecycleOrder registers mods (wrapped with an OrderRecorder) on a
// fresh *modulex.Manager, drives InitModules, StartModules, and StopModules
// with context.Background(), and asserts:
//
//   - Every module's Init was recorded.
//   - A module's Start (if it implements modulex.Starter) happened after its
//     own Init.
//   - A module's Stop (if it implements modulex.Stopper) happened after its
//     own Start (or after its own Init, if it does not implement Starter).
//   - For every dependency edge declared via DependsOn, the dependency's
//     Init happened before the dependent's Init (dependency-first startup
//     ordering), and the dependency's Stop happened after the dependent's
//     Stop (reverse, dependent-first, teardown ordering) — whenever both
//     sides recorded that phase.
//
// This is fully generic: it requires no cooperation from mods beyond the
// standard modulex.Module/Starter/Stopper interfaces. It detects both
// startup-ordering regressions (a module starting before its dependencies
// finished initializing) and shutdown-ordering regressions (a dependency
// torn down before the modules that depend on it).
//
// AssertLifecycleOrder fails the test (via t.Errorf, so all violations are
// reported rather than stopping at the first) if InitModules, StartModules,
// or StopModules return an unexpected error, or if any ordering invariant
// above is violated.
func AssertLifecycleOrder(t TB, mods ...modulex.Module) {
	t.Helper()
	if len(mods) == 0 {
		t.Fatalf("modtest: AssertLifecycleOrder requires at least one module")
	}

	rec := NewOrderRecorder()
	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	for _, mod := range mods {
		if err := mgr.RegisterModule(Wrap(mod, rec)); err != nil {
			t.Fatalf("modtest: RegisterModule(%q): %v", mod.Name(), err)
		}
	}

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		t.Fatalf("modtest: InitModules: %v", err)
	}
	if err := mgr.StartModules(ctx); err != nil {
		t.Fatalf("modtest: StartModules: %v", err)
	}
	if err := mgr.StopModules(ctx); err != nil {
		t.Fatalf("modtest: StopModules: %v", err)
	}

	checkLifecycleOrder(t, rec, mods)
}

// checkLifecycleOrder implements the assertions documented on
// AssertLifecycleOrder against an already-populated OrderRecorder. It is
// factored out from AssertLifecycleOrder so modtest's own tests can exercise
// the ordering-violation-detection logic directly against a synthetic
// OrderRecorder, without needing to reproduce an actual Manager bug.
func checkLifecycleOrder(t TB, rec *OrderRecorder, mods []modulex.Module) {
	t.Helper()

	for _, mod := range mods {
		name := mod.Name()
		initIdx := rec.Index(name, PhaseInit.String())
		startIdx := rec.Index(name, PhaseStart.String())
		stopIdx := rec.Index(name, PhaseStop.String())

		if initIdx == -1 {
			t.Errorf("modtest: module %q: Init was never recorded", name)
			continue
		}
		if startIdx != -1 && startIdx < initIdx {
			t.Errorf("modtest: module %q: Start (event #%d) happened before its own Init (event #%d)", name, startIdx, initIdx)
		}
		if stopIdx != -1 {
			switch {
			case startIdx != -1 && stopIdx < startIdx:
				t.Errorf("modtest: module %q: Stop (event #%d) happened before its own Start (event #%d)", name, stopIdx, startIdx)
			case startIdx == -1 && stopIdx < initIdx:
				t.Errorf("modtest: module %q: Stop (event #%d) happened before its own Init (event #%d)", name, stopIdx, initIdx)
			}
		}
	}

	for _, mod := range mods {
		name := mod.Name()
		modInit := rec.Index(name, PhaseInit.String())
		modStop := rec.Index(name, PhaseStop.String())

		for _, dep := range mod.DependsOn() {
			depInit := rec.Index(dep, PhaseInit.String())
			if depInit != -1 && modInit != -1 && depInit > modInit {
				t.Errorf("modtest: dependency %q (Init event #%d) initialized after its dependent %q (Init event #%d); expected dependency-first ordering", dep, depInit, name, modInit)
			}

			depStop := rec.Index(dep, PhaseStop.String())
			if depStop != -1 && modStop != -1 && depStop < modStop {
				t.Errorf("modtest: dependency %q (Stop event #%d) stopped before its dependent %q (Stop event #%d); expected reverse (dependent-first) teardown ordering", dep, depStop, name, modStop)
			}
		}
	}
}

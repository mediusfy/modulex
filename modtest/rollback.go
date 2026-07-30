package modtest

import (
	"context"
	"errors"

	"github.com/mediusfy/modulex"
)

// errInducedFailure is wrapped by the errors NewFailingModule returns by
// default, so AssertRollbackOnInitFailure/AssertRollbackOnStartFailure can
// confirm (via errors.Is) that the failure they observed is the one they
// induced, rather than some unrelated error the module under test happened
// to also raise.
var errInducedFailure = errors.New("modtest: induced failure")

// failingModule is a minimal modulex.Module that deliberately fails Init or
// Start, used to force InitModules/StartModules rollback so
// AssertRollbackOnInitFailure and AssertRollbackOnStartFailure can observe
// whether the module under test's own Stop ran during the resulting
// rollback.
type failingModule struct {
	name  string
	deps  []string
	phase Phase
	err   error
}

// NewFailingModule returns a modulex.Module named name, depending on deps,
// whose Init or Start (selected by phase, which must be PhaseInit or
// PhaseStart) always returns err. The other lifecycle method is a no-op
// returning nil. It is exported so callers can build custom rollback
// scenarios beyond what AssertRollbackOnInitFailure/
// AssertRollbackOnStartFailure construct automatically — for example,
// inducing the failure in a module that depends on more than one module
// under test at once.
func NewFailingModule(name string, deps []string, phase Phase, err error) modulex.Module {
	return &failingModule{name: name, deps: deps, phase: phase, err: err}
}

func (f *failingModule) Name() string        { return f.name }
func (f *failingModule) DependsOn() []string { return f.deps }

func (f *failingModule) Init(ctx context.Context, reg modulex.Registry) error {
	if f.phase == PhaseInit {
		return f.err
	}
	return nil
}

func (f *failingModule) Start(ctx context.Context) error {
	if f.phase == PhaseStart {
		return f.err
	}
	return nil
}

// AssertRollbackOnInitFailure registers modUnderTest alongside a
// harness-provided module that depends on it and always fails Init, drives
// InitModules on a fresh *modulex.Manager, and asserts:
//
//   - InitModules returns an error wrapping the induced failure.
//   - If modUnderTest implements modulex.Stopper, its Stop was invoked
//     during the resulting rollback (Modulex stops successfully initialized
//     modules in reverse order when a later module's Init fails).
//
// If modUnderTest does not implement modulex.Stopper, there is nothing to
// verify for cleanup and AssertRollbackOnInitFailure logs that fact via
// t.Logf rather than failing.
//
// This is fully generic: it requires no cooperation from modUnderTest
// beyond the standard modulex.Module/Stopper interfaces.
func AssertRollbackOnInitFailure(t TB, modUnderTest modulex.Module) {
	t.Helper()

	rec := NewOrderRecorder()
	wrapped := Wrap(modUnderTest, rec)
	failer := NewFailingModule(inducedFailerName(modUnderTest.Name(), "init"), []string{modUnderTest.Name()}, PhaseInit, errInducedFailure)

	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	if err := mgr.RegisterModule(wrapped); err != nil {
		t.Fatalf("modtest: RegisterModule(%q): %v", modUnderTest.Name(), err)
	}
	if err := mgr.RegisterModule(failer); err != nil {
		t.Fatalf("modtest: RegisterModule(%q): %v", failer.Name(), err)
	}

	err = mgr.InitModules(context.Background())
	if err == nil {
		t.Fatalf("modtest: expected InitModules to fail due to an induced failure in a module depending on %q, got nil error", modUnderTest.Name())
	}
	if !errors.Is(err, errInducedFailure) {
		t.Errorf("modtest: InitModules error does not wrap the induced failure: %v", err)
	}

	assertStoppedIfStopper(t, modUnderTest, rec)
}

// AssertRollbackOnStartFailure registers modUnderTest alongside a
// harness-provided module that depends on it and always fails Start, drives
// InitModules (expected to succeed) then StartModules (expected to fail) on
// a fresh *modulex.Manager, and asserts:
//
//   - StartModules returns an error wrapping the induced failure.
//   - If modUnderTest implements modulex.Stopper, its Stop was invoked
//     during the resulting rollback (Modulex stops successfully started
//     modules in reverse order when a later module's Start fails; a module
//     that does not implement modulex.Starter still counts as "successfully
//     started" and is stopped like any other).
//
// If modUnderTest does not implement modulex.Stopper, there is nothing to
// verify for cleanup and AssertRollbackOnStartFailure logs that fact via
// t.Logf rather than failing.
//
// This is fully generic: it requires no cooperation from modUnderTest
// beyond the standard modulex.Module/Starter/Stopper interfaces.
func AssertRollbackOnStartFailure(t TB, modUnderTest modulex.Module) {
	t.Helper()

	rec := NewOrderRecorder()
	wrapped := Wrap(modUnderTest, rec)
	failer := NewFailingModule(inducedFailerName(modUnderTest.Name(), "start"), []string{modUnderTest.Name()}, PhaseStart, errInducedFailure)

	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	if err := mgr.RegisterModule(wrapped); err != nil {
		t.Fatalf("modtest: RegisterModule(%q): %v", modUnderTest.Name(), err)
	}
	if err := mgr.RegisterModule(failer); err != nil {
		t.Fatalf("modtest: RegisterModule(%q): %v", failer.Name(), err)
	}

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		t.Fatalf("modtest: InitModules failed unexpectedly: %v", err)
	}

	err = mgr.StartModules(ctx)
	if err == nil {
		t.Fatalf("modtest: expected StartModules to fail due to an induced failure in a module depending on %q, got nil error", modUnderTest.Name())
	}
	if !errors.Is(err, errInducedFailure) {
		t.Errorf("modtest: StartModules error does not wrap the induced failure: %v", err)
	}

	assertStoppedIfStopper(t, modUnderTest, rec)
}

// assertStoppedIfStopper asserts that mod's Stop was recorded in rec,
// provided mod implements modulex.Stopper. If mod does not implement
// modulex.Stopper, it logs that there is nothing to verify instead of
// failing, since a module with no resources to release has no cleanup
// obligation.
func assertStoppedIfStopper(t TB, mod modulex.Module, rec *OrderRecorder) {
	t.Helper()
	if _, ok := mod.(modulex.Stopper); !ok {
		t.Logf("modtest: module %q does not implement modulex.Stopper; nothing to verify for rollback cleanup", mod.Name())
		return
	}
	if idx := rec.Index(mod.Name(), PhaseStop.String()); idx == -1 {
		t.Errorf("modtest: module %q implements modulex.Stopper but its Stop was not called during rollback", mod.Name())
	}
}

func inducedFailerName(modName, phase string) string {
	return "modtest-induced-" + phase + "-failure-depending-on-" + modName
}

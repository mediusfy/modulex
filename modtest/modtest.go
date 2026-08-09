// Package modtest provides reusable, composable test helpers that verify a
// modulex.Module (or a small group of them) against Modulex's lifecycle
// contract: Init/Start ordering, rollback on failure, cancellation and
// deadline handling, health/readiness registration, and resource ownership.
//
// A module author calls these helpers from their own *_test.go file, in the
// standard testing.T idiom — modtest is not a custom test runner or DSL:
//
//	func TestBillingModuleLifecycle(t *testing.T) {
//	    modtest.AssertLifecycleOrder(t, billing.NewModule())
//	}
//
// Every assertion helper here constructs its own private *modulex.Manager (or
// otherwise drives the module directly); none of them share state, so they
// can be called from independent test functions or subtests without
// interfering with one another.
//
// # Genericity
//
// Five of the six lifecycle properties this package covers are fully
// generic: they work against any modulex.Module without requiring the module
// under test to expose anything beyond the standard Module/Starter/Stopper
// interfaces:
//
//   - Lifecycle ordering (AssertLifecycleOrder)
//   - Rollback (AssertRollbackOnInitFailure, AssertRollbackOnStartFailure)
//   - Cancellation (AssertRespectsCancellation)
//   - Deadlines (AssertRespectsDeadline)
//   - Health/readiness (AssertHealthCheck, AssertReadinessCheck)
//
// The sixth, resource ownership (AssertResourceOwnership), is NOT fully
// generic: Modulex's Module interface has no way to introspect what a module
// acquired during Init/Start, so the caller must supply a ResourceOwner —
// typically the concrete adapter instance backing the module — that reports
// whether the resource has been released. See AssertResourceOwnership's doc
// comment for the exact requirement.
package modtest

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/mediusfy/modulex"
)

// TB is the subset of testing.TB that this package's Assert* helpers and
// Boot use. Every *testing.T and *testing.B already implements TB, so real
// callers pass their test's *testing.T exactly as they would to any other
// helper — TB exists as its own type (rather than these helpers taking
// testing.TB directly) because testing.TB has an unexported method that
// only the standard library's own *testing.T/*testing.B can implement,
// which would make it impossible for modtest's own test suite to verify
// that a helper correctly reports a failure without a fake recorder. See
// modtest's internal fakeT (in its test files) for that recorder.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Cleanup(func())
}

// Phase identifies one of the three lifecycle methods a modulex.Module may
// implement: Init (always present), Start (optional, via modulex.Starter),
// and Stop (optional, via modulex.Stopper).
type Phase int

const (
	// PhaseInit identifies modulex.Module.Init.
	PhaseInit Phase = iota
	// PhaseStart identifies modulex.Starter.Start.
	PhaseStart
	// PhaseStop identifies modulex.Stopper.Stop.
	PhaseStop
)

// String returns a lower-case name for p, or "unknown" for an out-of-range
// value.
func (p Phase) String() string {
	switch p {
	case PhaseInit:
		return "Init"
	case PhaseStart:
		return "Start"
	case PhaseStop:
		return "Stop"
	default:
		return "unknown"
	}
}

// quietLogger returns a slog.Logger that discards all output, so that
// helpers driving a Manager through its lifecycle do not spam test output
// with the manager's own INFO-level bookkeeping logs. Module-under-test logs
// written via reg.Logger() are similarly discarded; callers who want to see
// them can construct their own Manager instead of using a modtest helper.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Event records that a module's lifecycle method was invoked, in the order
// the OrderRecorder observed it.
type Event struct {
	// Module is the name reported by modulex.Module.Name for the module the
	// event pertains to.
	Module string
	// Phase is the lifecycle method that was invoked ("Init", "Start", or
	// "Stop").
	Phase string
}

// OrderRecorder records the order in which wrapped modules' lifecycle
// methods are invoked by a modulex.Manager. Use Wrap to attach a recorder to
// a module under test, drive the module through a Manager as usual, then
// inspect Events or use Index to make assertions about call order.
//
// An OrderRecorder is safe for concurrent use, though Modulex itself invokes
// Init/Start/Stop sequentially per phase, so concurrent recording is not
// normally exercised.
type OrderRecorder struct {
	mu     sync.Mutex
	events []Event
}

// NewOrderRecorder creates an empty OrderRecorder.
func NewOrderRecorder() *OrderRecorder {
	return &OrderRecorder{}
}

func (r *OrderRecorder) record(module, phase string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Event{Module: module, Phase: phase})
}

// Events returns a snapshot of every event recorded so far, in the order
// they were observed.
func (r *OrderRecorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Index returns the position (0-based, in overall recording order) of the
// first event matching module and phase, or -1 if no such event was
// recorded. phase should be one of "Init", "Start", or "Stop" (see Phase's
// String method).
func (r *OrderRecorder) Index(module, phase string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if e.Module == module && e.Phase == phase {
			return i
		}
	}
	return -1
}

// orderTracker implements modulex.Module and records every Init call
// against rec before delegating to the wrapped module. It is embedded by the
// capability-specific wrapper types below so that Wrap can return a value
// that implements exactly the optional interfaces (modulex.Starter,
// modulex.Stopper) the wrapped module itself implements — mirroring the
// standard Go pattern for decorating a value that has optional capability
// interfaces (e.g. http.ResponseWriter wrapping http.Flusher/http.Hijacker).
type orderTracker struct {
	mod modulex.Module
	rec *OrderRecorder
}

func (o *orderTracker) Name() string        { return o.mod.Name() }
func (o *orderTracker) DependsOn() []string { return o.mod.DependsOn() }
func (o *orderTracker) Init(ctx context.Context, reg modulex.Registry) error {
	o.rec.record(o.mod.Name(), PhaseInit.String())
	return o.mod.Init(ctx, reg)
}

type starterTracker struct{ *orderTracker }

func (o starterTracker) Start(ctx context.Context) error {
	o.rec.record(o.mod.Name(), PhaseStart.String())
	return o.mod.(modulex.Starter).Start(ctx)
}

type stopperTracker struct{ *orderTracker }

func (o stopperTracker) Stop(ctx context.Context) error {
	o.rec.record(o.mod.Name(), PhaseStop.String())
	return o.mod.(modulex.Stopper).Stop(ctx)
}

type starterStopperTracker struct {
	*orderTracker
	starterTracker
	stopperTracker
}

// Wrap returns a modulex.Module that delegates to mod but records every
// Init/Start/Stop call to rec before calling through. The returned module
// implements exactly the optional lifecycle interfaces (modulex.Starter,
// modulex.Stopper) that mod itself implements, so wrapping does not change
// how a Manager treats the module (e.g. Wrap-ing a module that has no Start
// method does not cause the Manager to call a no-op Start on it).
//
// Wrap is non-invasive: it requires no changes to the module under test's
// own code.
func Wrap(mod modulex.Module, rec *OrderRecorder) modulex.Module {
	base := &orderTracker{mod: mod, rec: rec}
	_, isStarter := mod.(modulex.Starter)
	_, isStopper := mod.(modulex.Stopper)

	switch {
	case isStarter && isStopper:
		return starterStopperTracker{
			orderTracker:   base,
			starterTracker: starterTracker{base},
			stopperTracker: stopperTracker{base},
		}
	case isStarter:
		return starterTracker{base}
	case isStopper:
		return stopperTracker{base}
	default:
		return base
	}
}

// Boot registers mods on a fresh *modulex.Manager, drives InitModules then
// StartModules with context.Background(), and fails the test immediately
// (via t.Fatalf) if either phase returns an error. It registers a t.Cleanup
// that calls StopModules so the manager is always torn down, and returns the
// running Manager for further inspection (HealthChecks, ReadinessChecks,
// ExportDAG, ModuleContract, ResolveService, and so on).
//
// Boot is a convenience for ad hoc assertions beyond the six covered by this
// package's Assert* helpers; those helpers do not use Boot themselves
// because most of them need finer control over which lifecycle phase runs
// and when.
func Boot(t TB, mods ...modulex.Module) *modulex.Manager {
	t.Helper()

	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	for _, mod := range mods {
		if err := mgr.RegisterModule(mod); err != nil {
			t.Fatalf("modtest: RegisterModule(%q): %v", mod.Name(), err)
		}
	}

	// Register cleanup before driving the lifecycle so StopModules is always
	// attempted, even if InitModules/StartModules fails or panics.
	t.Cleanup(func() {
		if err := mgr.StopModules(context.Background()); err != nil {
			t.Errorf("modtest: StopModules during cleanup: %v", err)
		}
	})

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		t.Fatalf("modtest: InitModules: %v", err)
	}
	if err := mgr.StartModules(ctx); err != nil {
		t.Fatalf("modtest: StartModules: %v", err)
	}

	return mgr
}

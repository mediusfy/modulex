package modtest

import (
	"context"

	"github.com/mediusfy/modulex"
)

// ResourceOwner reports whether a resource has been released. A module's
// adapter (an in-memory repository, a database handle, a connection pool,
// ...) implements this — typically alongside its own domain-specific
// methods — so AssertResourceOwnership can verify it structurally, without
// the adapter's package importing modtest.
//
// Modulex's modulex.Module interface has no generic way to introspect what
// a module acquired during Init/Start, so this is the one place in modtest
// that is NOT fully generic: the module author must expose (or adapt) a
// Closed() bool on whatever resource they want verified. See
// AssertResourceOwnership's doc comment for the full requirement.
type ResourceOwner interface {
	// Closed reports whether the resource has been released.
	Closed() bool
}

// AssertResourceOwnership drives mod through Init, Start (if mod implements
// modulex.Starter), and Stop on a fresh *modulex.Manager, calling owner
// after each phase to verify the resource mod acquired is released exactly
// when expected:
//
//   - If owner() already returns a non-nil ResourceOwner before Init runs,
//     it must report Closed() == false (a sanity check on the caller's
//     setup).
//   - owner() must return a non-nil ResourceOwner reporting Closed() ==
//     false immediately after Init, and again after Start if mod implements
//     modulex.Starter (the resource must stay open while the module is
//     running).
//   - mod must implement modulex.Stopper — AssertResourceOwnership fails via
//     t.Fatalf if it does not, since there would be no Stop call to verify
//     released the resource.
//   - owner() must return a non-nil ResourceOwner reporting Closed() == true
//     after Stop returns.
//
// owner is a function, rather than a plain ResourceOwner value, because a
// module very commonly constructs its own adapter lazily inside Init rather
// than receiving one the caller already holds — the classic hexagonal
// layout this package's sibling scaffolding tool generates does exactly
// this (see module.go's Init in a generated module, which builds its
// adapters.InMemoryRepository internally and exposes it via a Repository()
// accessor for tests). owner lets AssertResourceOwnership support both
// styles:
//
//   - An adapter the caller constructs and injects (owner just closes over
//     that pre-existing value): func() modtest.ResourceOwner { return repo }
//   - An adapter the module constructs internally during Init, retrieved via
//     an accessor: func() modtest.ResourceOwner { return mod.Repository() }
//     — owner() naturally returns nil before Init runs, in which case the
//     "before Init" sanity check above is skipped.
//
// This is the one helper in modtest that is not fully generic: Modulex has
// no way to discover a module's owned resources on its own, so the caller
// must supply owner and whatever it returns must implement Closed() bool.
func AssertResourceOwnership(t TB, mod modulex.Module, owner func() ResourceOwner) {
	t.Helper()

	if o := owner(); o != nil && o.Closed() {
		t.Fatalf("modtest: resource owner for module %q already reports Closed() == true before Init; check test setup", mod.Name())
	}

	mgr, err := modulex.NewManager(modulex.WithLogger(quietLogger()))
	if err != nil {
		t.Fatalf("modtest: NewManager: %v", err)
	}
	if err := mgr.RegisterModule(mod); err != nil {
		t.Fatalf("modtest: RegisterModule(%q): %v", mod.Name(), err)
	}

	if err := mgr.InitModules(context.Background()); err != nil {
		t.Fatalf("modtest: InitModules: %v", err)
	}
	o := owner()
	if o == nil {
		t.Fatalf("modtest: owner() returned nil after Init; module %q must have acquired its resource by the time Init returns", mod.Name())
	}
	if o.Closed() {
		t.Errorf("modtest: resource owner for module %q reports Closed() == true immediately after Init; the resource must remain open while the module is running", mod.Name())
	}

	if s, ok := mod.(modulex.Starter); ok {
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("modtest: Start: %v", err)
		}
		if o := owner(); o != nil && o.Closed() {
			t.Errorf("modtest: resource owner for module %q reports Closed() == true immediately after Start; the resource must remain open while the module is running", mod.Name())
		}
	}

	stopper, ok := mod.(modulex.Stopper)
	if !ok {
		t.Fatalf("modtest: module %q does not implement modulex.Stopper; AssertResourceOwnership requires a Stop method that releases the resource", mod.Name())
	}
	if err := stopper.Stop(context.Background()); err != nil {
		t.Fatalf("modtest: Stop: %v", err)
	}

	if o := owner(); o == nil || !o.Closed() {
		t.Errorf("modtest: resource owner for module %q reports Closed() == false (or was nil) after Stop returned; the resource was not released", mod.Name())
	}
}

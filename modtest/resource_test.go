package modtest

import (
	"context"
	"testing"

	"github.com/mediusfy/modulex"
)

// fakeResource is a minimal ResourceOwner used to test AssertResourceOwnership
// against both a well-behaved (Stop actually releases the resource) and a
// broken (Stop is a no-op that leaks it) scenario.
type fakeResource struct {
	closed bool
}

func (r *fakeResource) Closed() bool { return r.closed }

func TestAssertResourceOwnership_WellBehaved(t *testing.T) {
	res := &fakeResource{}
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "resource-ok"},
		stopFn:        func(ctx context.Context) error { res.closed = true; return nil },
	}

	ft := runFake(func(t TB) { AssertResourceOwnership(t, mod, func() ResourceOwner { return res }) })
	if ft.failed {
		t.Fatalf("AssertResourceOwnership reported a failure for a module whose Stop correctly releases its resource: %v", ft.logs)
	}
}

// TestAssertResourceOwnership_DetectsLeak proves AssertResourceOwnership
// correctly fails against a real module whose Stop does not release its
// resource — the exact regression this helper exists to catch.
func TestAssertResourceOwnership_DetectsLeak(t *testing.T) {
	res := &fakeResource{}
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "resource-leak"},
		stopFn:        func(ctx context.Context) error { return nil }, // deliberately does not close res
	}

	ft := runFake(func(t TB) { AssertResourceOwnership(t, mod, func() ResourceOwner { return res }) })
	if !ft.failed {
		t.Fatalf("AssertResourceOwnership did not detect that Stop left the resource open")
	}
}

// TestAssertResourceOwnership_LazilyCreatedOwner proves AssertResourceOwnership
// supports the common case where a module constructs its own resource
// inside Init (owner() returns nil before Init runs) rather than the
// caller injecting a pre-existing one — the shape the scaffolding tool's
// generated module.go uses.
func TestAssertResourceOwnership_LazilyCreatedOwner(t *testing.T) {
	var res *fakeResource
	mod := fullFixture{
		fixtureModule: &fixtureModule{
			name: "resource-lazy",
			initFn: func(ctx context.Context, reg modulex.Registry) error {
				res = &fakeResource{}
				return nil
			},
		},
		stopFn: func(ctx context.Context) error { res.closed = true; return nil },
	}

	ft := runFake(func(t TB) {
		AssertResourceOwnership(t, mod, func() ResourceOwner {
			if res == nil {
				return nil
			}
			return res
		})
	})
	if ft.failed {
		t.Fatalf("AssertResourceOwnership reported a failure for a lazily-created, well-behaved resource owner: %v", ft.logs)
	}
}

// TestAssertResourceOwnership_DetectsNilOwnerAfterStart proves
// AssertResourceOwnership catches a module that drops its resource
// reference between Init and Start — owner() must keep reporting a non-nil
// ResourceOwner through Start, exactly as the post-Init check already
// requires, not silently pass when it goes back to nil.
//
// Stop reassigns res to a fresh, already-closed resource rather than
// leaving it nil: this isolates the post-Start check specifically, since a
// nil owner() left in place all the way to the end would instead be caught
// by the post-Stop check regardless of whether the post-Start check works
// at all — which is exactly how this gap stayed undetected.
func TestAssertResourceOwnership_DetectsNilOwnerAfterStart(t *testing.T) {
	res := &fakeResource{}
	mod := fullFixture{
		fixtureModule: &fixtureModule{name: "resource-dropped-before-start"},
		startFn:       func(ctx context.Context) error { res = nil; return nil },
		stopFn:        func(ctx context.Context) error { res = &fakeResource{closed: true}; return nil },
	}

	ft := runFake(func(t TB) {
		AssertResourceOwnership(t, mod, func() ResourceOwner {
			if res == nil {
				return nil
			}
			return res
		})
	})
	if !ft.failed {
		t.Fatalf("AssertResourceOwnership did not detect that owner() returned nil after Start")
	}
}

func TestAssertResourceOwnership_RequiresStopper(t *testing.T) {
	res := &fakeResource{}
	mod := &fixtureModule{name: "no-stopper"}

	ft := runFake(func(t TB) { AssertResourceOwnership(t, mod, func() ResourceOwner { return res }) })
	if !ft.failed {
		t.Fatalf("AssertResourceOwnership should fail when the module under test does not implement modulex.Stopper")
	}
}

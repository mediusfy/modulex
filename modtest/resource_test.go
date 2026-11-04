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
	tests := []struct {
		name  string
		build func() (modulex.Module, func() ResourceOwner)
	}{
		{
			name: "Stop correctly releases its resource",
			build: func() (modulex.Module, func() ResourceOwner) {
				res := &fakeResource{}
				mod := fullFixture{
					fixtureModule: &fixtureModule{name: "resource-ok"},
					stopFn:        func(ctx context.Context) error { res.closed = true; return nil },
				}
				return mod, func() ResourceOwner { return res }
			},
		},
		// Supports the common case where a module constructs its own resource
		// inside Init (owner() returns nil before Init runs) rather than the
		// caller injecting a pre-existing one — the shape the scaffolding
		// tool's generated module.go uses.
		{
			name: "lazily created owner",
			build: func() (modulex.Module, func() ResourceOwner) {
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
				return mod, func() ResourceOwner {
					if res == nil {
						return nil
					}
					return res
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod, owner := tt.build()
			ft := runFake(func(t TB) { AssertResourceOwnership(t, mod, owner) })
			if ft.failed {
				t.Fatalf("AssertResourceOwnership reported a failure for a well-behaved resource owner: %v", ft.logs)
			}
		})
	}
}

// TestAssertResourceOwnership_DetectsFailures proves AssertResourceOwnership
// correctly fails against real modules that leak their resource, drop it
// between Init and Start, or lack a Stopper.
func TestAssertResourceOwnership_DetectsFailures(t *testing.T) {
	tests := []struct {
		name  string
		build func() (modulex.Module, func() ResourceOwner)
	}{
		{
			// The exact regression this helper exists to catch.
			name: "Stop does not release the resource",
			build: func() (modulex.Module, func() ResourceOwner) {
				res := &fakeResource{}
				mod := fullFixture{
					fixtureModule: &fixtureModule{name: "resource-leak"},
					stopFn:        func(ctx context.Context) error { return nil }, // deliberately does not close res
				}
				return mod, func() ResourceOwner { return res }
			},
		},
		{
			// Stop reassigns res to a fresh, already-closed resource rather than
			// leaving it nil: this isolates the post-Start check specifically,
			// since a nil owner() left in place all the way to the end would
			// instead be caught by the post-Stop check regardless of whether
			// the post-Start check works at all.
			name: "owner returns nil after Start",
			build: func() (modulex.Module, func() ResourceOwner) {
				res := &fakeResource{}
				mod := fullFixture{
					fixtureModule: &fixtureModule{name: "resource-dropped-before-start"},
					startFn:       func(ctx context.Context) error { res = nil; return nil },
					stopFn:        func(ctx context.Context) error { res = &fakeResource{closed: true}; return nil },
				}
				return mod, func() ResourceOwner {
					if res == nil {
						return nil
					}
					return res
				}
			},
		},
		{
			name: "module does not implement Stopper",
			build: func() (modulex.Module, func() ResourceOwner) {
				res := &fakeResource{}
				return &fixtureModule{name: "no-stopper"}, func() ResourceOwner { return res }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod, owner := tt.build()
			ft := runFake(func(t TB) { AssertResourceOwnership(t, mod, owner) })
			if !ft.failed {
				t.Fatalf("AssertResourceOwnership did not fail for case %q", tt.name)
			}
		})
	}
}

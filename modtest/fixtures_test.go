package modtest

import (
	"context"

	"github.com/mediusfy/modulex"
)

// fixtureModule is a minimal, configurable modulex.Module used across
// modtest's own test suite. Embedding it in starterFixture/stopperFixture/
// fullFixture produces modules that implement exactly the optional
// lifecycle interfaces the test needs, mirroring how a real module author's
// types vary in which of Starter/Stopper they implement.
type fixtureModule struct {
	name   string
	deps   []string
	initFn func(ctx context.Context, reg modulex.Registry) error
}

func (f *fixtureModule) Name() string        { return f.name }
func (f *fixtureModule) DependsOn() []string { return f.deps }

func (f *fixtureModule) Init(ctx context.Context, reg modulex.Registry) error {
	if f.initFn != nil {
		return f.initFn(ctx, reg)
	}
	return nil
}

// starterFixture is a fixtureModule that also implements modulex.Starter.
type starterFixture struct {
	*fixtureModule
	startFn func(ctx context.Context) error
}

func (f starterFixture) Start(ctx context.Context) error {
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	return nil
}

// stopperFixture is a fixtureModule that also implements modulex.Stopper
// but not modulex.Starter.
type stopperFixture struct {
	*fixtureModule
	stopFn func(ctx context.Context) error
}

func (f stopperFixture) Stop(ctx context.Context) error {
	if f.stopFn != nil {
		return f.stopFn(ctx)
	}
	return nil
}

// fullFixture is a fixtureModule that implements both modulex.Starter and
// modulex.Stopper.
type fullFixture struct {
	*fixtureModule
	startFn func(ctx context.Context) error
	stopFn  func(ctx context.Context) error
}

func (f fullFixture) Start(ctx context.Context) error {
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	return nil
}

func (f fullFixture) Stop(ctx context.Context) error {
	if f.stopFn != nil {
		return f.stopFn(ctx)
	}
	return nil
}

var (
	_ modulex.Module  = (*fixtureModule)(nil)
	_ modulex.Module  = starterFixture{}
	_ modulex.Starter = starterFixture{}
	_ modulex.Module  = stopperFixture{}
	_ modulex.Stopper = stopperFixture{}
	_ modulex.Module  = fullFixture{}
	_ modulex.Starter = fullFixture{}
	_ modulex.Stopper = fullFixture{}
)

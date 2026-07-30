package modtest

import (
	"context"
	"errors"
	"testing"

	"github.com/mediusfy/modulex"
)

func healthyModule(name, checkName string) *fixtureModule {
	return &fixtureModule{
		name: name,
		initFn: func(ctx context.Context, reg modulex.Registry) error {
			return reg.RegisterHealthCheck(checkName, func(context.Context) error { return nil })
		},
	}
}

func unhealthyModule(name, checkName string) *fixtureModule {
	return &fixtureModule{
		name: name,
		initFn: func(ctx context.Context, reg modulex.Registry) error {
			return reg.RegisterHealthCheck(checkName, func(context.Context) error {
				return errors.New("dependency unreachable")
			})
		},
	}
}

func TestAssertHealthCheck_Passes(t *testing.T) {
	mod := healthyModule("health-ok", "db")

	ft := runFake(func(t TB) { AssertHealthCheck(t, mod, "db", false) })
	if ft.failed {
		t.Fatalf("AssertHealthCheck reported a failure for a passing health check: %v", ft.logs)
	}
}

func TestAssertHealthCheck_DetectsUnhealthy(t *testing.T) {
	mod := unhealthyModule("health-bad", "db")

	ft := runFake(func(t TB) { AssertHealthCheck(t, mod, "db", false) })
	if !ft.failed {
		t.Fatalf("AssertHealthCheck did not detect a health check reporting an error when wantErr=false")
	}
}

func TestAssertHealthCheck_WantErrTrueMatchesFailure(t *testing.T) {
	mod := unhealthyModule("health-bad-2", "db")

	ft := runFake(func(t TB) { AssertHealthCheck(t, mod, "db", true) })
	if ft.failed {
		t.Fatalf("AssertHealthCheck reported a failure when wantErr=true correctly matched a failing check: %v", ft.logs)
	}
}

func TestAssertHealthCheck_MissingCheckFails(t *testing.T) {
	mod := &fixtureModule{name: "no-check"}

	ft := runFake(func(t TB) { AssertHealthCheck(t, mod, "db", false) })
	if !ft.failed {
		t.Fatalf("AssertHealthCheck did not fail when the named health check was never registered")
	}
}

func readinessModule(name, checkName string, ready bool) *fixtureModule {
	return &fixtureModule{
		name: name,
		initFn: func(ctx context.Context, reg modulex.Registry) error {
			return reg.RegisterReadinessCheck(checkName, func(context.Context) error {
				if ready {
					return nil
				}
				return errors.New("cache not warm")
			})
		},
	}
}

func TestAssertReadinessCheck_Passes(t *testing.T) {
	mod := readinessModule("ready-ok", "cache", true)

	ft := runFake(func(t TB) { AssertReadinessCheck(t, mod, "cache", false) })
	if ft.failed {
		t.Fatalf("AssertReadinessCheck reported a failure for a passing readiness check: %v", ft.logs)
	}
}

func TestAssertReadinessCheck_DetectsNotReady(t *testing.T) {
	mod := readinessModule("ready-bad", "cache", false)

	ft := runFake(func(t TB) { AssertReadinessCheck(t, mod, "cache", false) })
	if !ft.failed {
		t.Fatalf("AssertReadinessCheck did not detect a readiness check reporting not-ready when wantErr=false")
	}
}

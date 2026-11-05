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

func TestAssertHealthCheck(t *testing.T) {
	tests := []struct {
		name     string
		mod      modulex.Module
		wantErr  bool
		wantFail bool
	}{
		{name: "passing health check", mod: healthyModule("health-ok", "db")},
		{name: "unhealthy check with wantErr=false is detected", mod: unhealthyModule("health-bad", "db"), wantFail: true},
		{name: "unhealthy check with wantErr=true matches", mod: unhealthyModule("health-bad-2", "db"), wantErr: true},
		{name: "unregistered check fails", mod: &fixtureModule{name: "no-check"}, wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := runFake(func(t TB) { AssertHealthCheck(t, tt.mod, "db", tt.wantErr) })
			if ft.failed != tt.wantFail {
				t.Fatalf("AssertHealthCheck failed = %v, want %v: %v", ft.failed, tt.wantFail, ft.logs)
			}
		})
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

func TestAssertReadinessCheck(t *testing.T) {
	tests := []struct {
		name     string
		mod      modulex.Module
		wantFail bool
	}{
		{name: "passing readiness check", mod: readinessModule("ready-ok", "cache", true)},
		{name: "not-ready check with wantErr=false is detected", mod: readinessModule("ready-bad", "cache", false), wantFail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := runFake(func(t TB) { AssertReadinessCheck(t, tt.mod, "cache", false) })
			if ft.failed != tt.wantFail {
				t.Fatalf("AssertReadinessCheck failed = %v, want %v: %v", ft.failed, tt.wantFail, ft.logs)
			}
		})
	}
}

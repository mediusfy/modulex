package modtest

import (
	"context"

	"github.com/mediusfy/modulex"
)

// AssertHealthCheck registers mod on a fresh *modulex.Manager, calls
// InitModules (where a well-behaved module registers its health checks via
// modulex.HealthCheckRegisterer.RegisterHealthCheck), then looks up the
// health check named name and runs it with context.Background().
//
// It fails the test (via t.Fatalf) if no health check named name was
// registered during Init, or (via t.Errorf) if the check's result does not
// match wantErr: wantErr == true expects the check to return a non-nil
// error (an induced-unhealthy scenario); wantErr == false expects it to
// return nil.
//
// This is fully generic: it requires no cooperation from mod beyond calling
// reg.RegisterHealthCheck(name, ...) during Init, which is the standard way
// any module registers a liveness check.
func AssertHealthCheck(t TB, mod modulex.Module, name string, wantErr bool) {
	t.Helper()
	check := lookupCheck(t, mod, name, (*modulex.Manager).HealthChecks, "health")
	runCheck(t, name, "health", check, wantErr)
}

// AssertReadinessCheck registers mod on a fresh *modulex.Manager, calls
// InitModules (where a well-behaved module registers its readiness checks
// via modulex.ReadinessRegisterer.RegisterReadinessCheck), then looks up the
// readiness check named name and runs it with context.Background().
//
// It fails the test (via t.Fatalf) if no readiness check named name was
// registered during Init, or (via t.Errorf) if the check's result does not
// match wantErr: wantErr == true expects the check to return a non-nil
// error (an induced-not-ready scenario); wantErr == false expects it to
// return nil.
//
// This is fully generic: it requires no cooperation from mod beyond calling
// reg.RegisterReadinessCheck(name, ...) during Init, which is the standard
// way any module registers a readiness check.
func AssertReadinessCheck(t TB, mod modulex.Module, name string, wantErr bool) {
	t.Helper()
	check := lookupCheck(t, mod, name, (*modulex.Manager).ReadinessChecks, "readiness")
	runCheck(t, name, "readiness", check, wantErr)
}

// lookupCheck registers mod, drives InitModules, fetches the named check
// via checks (either (*modulex.Manager).HealthChecks or
// (*modulex.Manager).ReadinessChecks), and fails the test via t.Fatalf if it
// was not registered. kind ("health" or "readiness") is used only for error
// messages.
func lookupCheck(t TB, mod modulex.Module, name string, checks func(*modulex.Manager) map[string]func(context.Context) error, kind string) func(context.Context) error {
	t.Helper()

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
	t.Cleanup(func() {
		_ = mgr.StopModules(context.Background())
	})

	check, ok := checks(mgr)[name]
	if !ok {
		t.Fatalf("modtest: module %q did not register a %s check named %q during Init", mod.Name(), kind, name)
	}
	return check
}

func runCheck(t TB, name, kind string, check func(context.Context) error, wantErr bool) {
	t.Helper()
	err := check(context.Background())
	switch {
	case wantErr && err == nil:
		t.Errorf("modtest: %s check %q reported healthy/ready, want a failure", kind, name)
	case !wantErr && err != nil:
		t.Errorf("modtest: %s check %q reported an error, want healthy/ready: %v", kind, name, err)
	}
}

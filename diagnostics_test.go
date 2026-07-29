package modulex_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModuleContractDeterministic asserts ModuleContract sorts both the
// modules and each module's dependency list alphabetically, so that two
// calls against the same manager state marshal to byte-identical JSON. This
// is required for diffing the contract across releases or deployments.
func TestModuleContractDeterministic(t *testing.T) {
	t.Parallel()

	manager := newTestManager(nil)
	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "beta"})))
	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "alpha", deps: []string{"gamma", "beta"}})))
	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "gamma"})))

	first, err := json.Marshal(manager.ModuleContract())
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		again, err := json.Marshal(manager.ModuleContract())
		require.NoError(t, err)
		assert.Equal(t, first, again)
	}

	var contract modulex.ModuleContract
	require.NoError(t, json.Unmarshal(first, &contract))
	require.Len(t, contract.Modules, 3)
	assert.Equal(t, "alpha", contract.Modules[0].Name)
	assert.Equal(t, []string{"beta", "gamma"}, contract.Modules[0].DependsOn)
	assert.Equal(t, "beta", contract.Modules[1].Name)
	assert.Equal(t, []string{}, contract.Modules[1].DependsOn)
	assert.Equal(t, "gamma", contract.Modules[2].Name)
	assert.Equal(t, []string{}, contract.Modules[2].DependsOn)
}

// TestModuleContractEmptyManager asserts a manager with no registered
// modules produces valid, non-nil, empty-ish JSON rather than panicking or
// marshaling `null`.
func TestModuleContractEmptyManager(t *testing.T) {
	t.Parallel()

	manager := newTestManager(nil)
	out, err := json.Marshal(manager.ModuleContract())
	require.NoError(t, err)
	assert.JSONEq(t, `{"modules":[]}`, string(out))
}

// secretService is a sentinel service value used to assert that Diagnostics
// never exposes registered service values, only their registered names.
type secretService struct {
	APIKey string
}

// TestDiagnosticsFullLifecycle exercises Diagnostics against a manager with
// dependent modules, a registered service, a health check, a readiness
// check, and a supervised task started via Go. It asserts the JSON output is
// deterministic and contains the expected sorted names, and that a
// registered service's value is never present in the marshaled output.
func TestDiagnosticsFullLifecycle(t *testing.T) {
	t.Parallel()

	manager := newTestManager(nil)

	const secretValue = "sk-super-secret-value-12345"
	release := make(chan struct{})
	taskStarted := make(chan struct{})
	var taskHandle *modulex.TaskHandle

	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{
		name: "beta",
		onInit: func(reg modulex.Registry) {
			require.NoError(t, reg.RegisterService("beta.Secret", &secretService{APIKey: secretValue}))
			require.NoError(t, reg.RegisterHealthCheck("beta.health", func(context.Context) error { return nil }))
			require.NoError(t, reg.RegisterReadinessCheck("beta.ready", func(context.Context) error { return nil }))
		},
	})))
	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{
		name: "alpha",
		deps: []string{"beta"},
		onInit: func(reg modulex.Registry) {
			handle, err := reg.Go(context.Background(), "alpha.worker", func(ctx context.Context) error {
				close(taskStarted)
				<-release
				return nil
			})
			require.NoError(t, err)
			taskHandle = handle
		},
	})))

	require.NoError(t, manager.InitModules(context.Background()))
	require.NoError(t, manager.StartModules(context.Background()))
	<-taskStarted

	// While the task is still running, it must appear in Diagnostics as an
	// in-flight, unfinished task.
	running := manager.Diagnostics()
	require.Len(t, running.Tasks, 1)
	assert.Equal(t, "alpha.worker", running.Tasks[0].Name)
	assert.False(t, running.Tasks[0].Done)
	assert.Empty(t, running.Tasks[0].Err)

	firstJSON, err := json.Marshal(running)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(manager.Diagnostics())
	require.NoError(t, err)
	assert.Equal(t, firstJSON, secondJSON, "Diagnostics must marshal deterministically across repeated calls")

	assert.NotContains(t, string(firstJSON), secretValue,
		"Diagnostics JSON must never contain a registered service's value")

	assert.Equal(t, "running", running.State)
	require.Len(t, running.Modules.Modules, 2)
	assert.Equal(t, "alpha", running.Modules.Modules[0].Name)
	assert.Equal(t, []string{"beta"}, running.Modules.Modules[0].DependsOn)
	assert.Equal(t, "beta", running.Modules.Modules[1].Name)

	assert.Equal(t, []string{"beta.Secret"}, running.Services)
	assert.Equal(t, []string{"beta.health"}, running.HealthChecks)
	assert.Equal(t, []string{"beta.ready"}, running.ReadinessChecks)

	assert.Greater(t, int64(running.Timings.InitModules), int64(0), "InitModules total duration should be non-zero after InitModules has run")
	require.Len(t, running.Timings.ModuleInit, 2)
	for _, mt := range running.Timings.ModuleInit {
		assert.GreaterOrEqual(t, int64(mt.DurationNs), int64(0))
	}

	// StartModules has already run too.
	assert.Greater(t, int64(running.Timings.StartModules), int64(0), "StartModules total duration should be non-zero after StartModules has run")

	// Let the task finish and confirm it drops out of Diagnostics, matching
	// the manager's existing invariant that finished tasks are removed from
	// its internal tracking before waiting callers observe completion.
	close(release)
	require.NoError(t, taskHandle.Wait())

	finished := manager.Diagnostics()
	assert.Empty(t, finished.Tasks)
}

// TestDiagnosticsTaskError asserts a task's error is surfaced in
// TaskDiagnostic.Err while it is still visible (i.e. before the manager's
// completion goroutine removes it), and confirms the manager's existing
// behavior of also joining the error into StopModules's return value.
func TestDiagnosticsTaskError(t *testing.T) {
	t.Parallel()

	manager := newTestManager(nil)
	release := make(chan struct{})
	taskStarted := make(chan struct{})

	require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{
		name: "alpha",
		onInit: func(reg modulex.Registry) {
			_, err := reg.Go(context.Background(), "alpha.worker", func(ctx context.Context) error {
				close(taskStarted)
				<-release
				return assert.AnError
			})
			require.NoError(t, err)
		},
	})))

	require.NoError(t, manager.InitModules(context.Background()))
	require.NoError(t, manager.StartModules(context.Background()))
	<-taskStarted

	diag := manager.Diagnostics()
	require.Len(t, diag.Tasks, 1)
	assert.False(t, diag.Tasks[0].Done)
	assert.Empty(t, diag.Tasks[0].Err)

	close(release)
	require.Error(t, manager.StopModules(context.Background()))
}

// TestDiagnosticsEmptyManager asserts a pre-init manager with no modules,
// services, or tasks does not panic and produces valid, empty-ish JSON.
func TestDiagnosticsEmptyManager(t *testing.T) {
	t.Parallel()

	manager := newTestManager(nil)

	var diag modulex.Diagnostics
	require.NotPanics(t, func() {
		diag = manager.Diagnostics()
	})

	out, err := json.Marshal(diag)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"state": "configuring",
		"modules": {"modules": []},
		"services": [],
		"tasks": [],
		"health_checks": [],
		"readiness_checks": [],
		"timings": {}
	}`, string(out))
}

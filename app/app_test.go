package app_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/app"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// recordingModule is a minimal modulex.Module that records Init/Start/Stop
// invocations and supports an optional Init hook and an artificial Stop
// delay, used to exercise app.Run's lifecycle wiring.
type recordingModule struct {
	name      string
	deps      []string
	stopDelay time.Duration
	onInit    func(reg modulex.Registry)
	startedCh chan struct{}

	mu     sync.Mutex
	events []string
}

func (m *recordingModule) Name() string        { return m.name }
func (m *recordingModule) DependsOn() []string { return m.deps }

func (m *recordingModule) Init(_ context.Context, reg modulex.Registry) error {
	m.record("init")
	if m.onInit != nil {
		m.onInit(reg)
	}
	return nil
}

func (m *recordingModule) Start(context.Context) error {
	m.record("start")
	if m.startedCh != nil {
		close(m.startedCh)
	}
	return nil
}

func (m *recordingModule) Stop(ctx context.Context) error {
	if m.stopDelay > 0 {
		select {
		case <-time.After(m.stopDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.record("stop")
	return nil
}

func (m *recordingModule) record(event string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *recordingModule) Events() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.events...)
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRun_FullLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mod := &recordingModule{name: "mod-a", startedCh: make(chan struct{})}

	var mgr *modulex.Manager
	runErr := make(chan error, 1)
	go func() {
		runErr <- app.Run(newTestLogger(), nil, []modulex.Module{mod},
			app.WithContext(ctx),
			app.WithSetup(func(m *modulex.Manager) error {
				mgr = m
				return nil
			}),
		)
	}()

	select {
	case <-mod.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for module to start")
	}

	cancel()

	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}

	assert.Equal(t, []string{"init", "start", "stop"}, mod.Events())
	require.NotNil(t, mgr)
	assert.Equal(t, modulex.StateStopped, mgr.State())
}

func TestRun_NilLoggerUsesDefaultLogger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mod := &recordingModule{name: "nil-logger", startedCh: make(chan struct{})}
	runErr := make(chan error, 1)
	go func() {
		runErr <- app.Run(nil, nil, []modulex.Module{mod}, app.WithContext(ctx))
	}()

	select {
	case <-mod.startedCh:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for module to start")
	}

	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

func TestRun_ManagerConstructionFailure(t *testing.T) {
	err := app.Run(newTestLogger(), nil, nil,
		app.WithManagerOptions(modulex.WithPanicPolicy(modulex.PanicPolicy(99))),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, modulex.ErrInvalidPanicPolicy)
}

func TestRun_RegisterModuleFailure(t *testing.T) {
	modA := &recordingModule{name: "dup"}
	modB := &recordingModule{name: "dup"}

	err := app.Run(newTestLogger(), nil, []modulex.Module{modA, modB})
	require.Error(t, err)
	assert.ErrorIs(t, err, modulex.ErrDuplicateModule)
}

func TestRun_SetupHookRunsBeforeInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	foundMarker := make(chan bool, 1)
	mod := &recordingModule{
		name:      "mod-a",
		startedCh: make(chan struct{}),
		onInit: func(reg modulex.Registry) {
			_, err := reg.ResolveService("setup.marker")
			foundMarker <- err == nil
		},
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- app.Run(newTestLogger(), nil, []modulex.Module{mod},
			app.WithContext(ctx),
			app.WithSetup(func(m *modulex.Manager) error {
				return m.RegisterService("setup.marker", "present")
			}),
		)
	}()

	select {
	case ok := <-foundMarker:
		assert.True(t, ok, "setup hook should have registered the service before Init ran")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for module Init")
	}

	cancel()

	select {
	case err := <-runErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

func TestRun_ShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mod := &recordingModule{
		name:      "slow-stop",
		startedCh: make(chan struct{}),
		stopDelay: 2 * time.Second,
	}

	runErr := make(chan error, 1)
	go func() {
		runErr <- app.Run(newTestLogger(), nil, []modulex.Module{mod},
			app.WithContext(ctx),
			app.WithShutdownTimeout(100*time.Millisecond),
		)
	}()

	select {
	case <-mod.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for module to start")
	}

	cancel()

	select {
	case err := <-runErr:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

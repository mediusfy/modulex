package modulex_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	gochi "github.com/go-chi/chi/v5"
	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockConfig struct {
	Value string `json:"value"`
}

type MockService interface {
	DoSomething() string
}

type MockServiceImpl struct{}

func (s *MockServiceImpl) DoSomething() string {
	return "mocked"
}

type DummyModule struct {
	name          string
	deps          []string
	initCalled    bool
	startCalled   bool
	stopCalled    bool
	initSequence  *[]string
	startSequence *[]string
	stopSequence  *[]string
}

func NewDummyModule(name string, deps []string, initSeq, startSeq, stopSeq *[]string) *DummyModule {
	return &DummyModule{
		name:          name,
		deps:          deps,
		initSequence:  initSeq,
		startSequence: startSeq,
		stopSequence:  stopSeq,
	}
}

func (m *DummyModule) Name() string {
	return m.name
}

func (m *DummyModule) DependsOn() []string {
	return m.deps
}

func (m *DummyModule) Init(ctx context.Context, reg modulex.Registry) error {
	m.initCalled = true
	*m.initSequence = append(*m.initSequence, m.name)

	// Register a service for others to consume
	if m.name == "module-a" {
		_ = reg.RegisterService("module-a.Service", &MockServiceImpl{})
	}

	// Register HTTP route
	reg.Router().Get("/"+m.name, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(m.name + " active"))
	})

	return nil
}

func (m *DummyModule) Start(ctx context.Context) error {
	m.startCalled = true
	*m.startSequence = append(*m.startSequence, m.name)
	return nil
}

func (m *DummyModule) Stop(ctx context.Context) error {
	m.stopCalled = true
	*m.stopSequence = append(*m.stopSequence, m.name)
	return nil
}

func TestManagerLifecycleAndWiring(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	configLoader := func(target interface{}) error {
		cfg, ok := target.(*MockConfig)
		if !ok {
			return errors.New("invalid config type")
		}
		cfg.Value = "test-value"
		return nil
	}

	manager := modulex.NewManager(router, nil, logger, configLoader)

	var initSeq, startSeq, stopSeq []string
	// module-b depends on module-a, so module-a must initialize and start FIRST
	modB := NewDummyModule("module-b", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)
	modA := NewDummyModule("module-a", nil, &initSeq, &startSeq, &stopSeq)

	manager.RegisterModule(modB)
	manager.RegisterModule(modA)

	// 1. Initialize modules
	ctx := context.Background()
	err := manager.InitModules(ctx)
	require.NoError(t, err)

	assert.True(t, modA.initCalled)
	assert.True(t, modB.initCalled)
	assert.Equal(t, []string{"module-a", "module-b"}, initSeq)

	// Verify service registration and resolution
	svc, err := manager.ResolveService("module-a.Service")
	require.NoError(t, err)
	mockSvc, ok := svc.(MockService)
	require.True(t, ok)
	assert.Equal(t, "mocked", mockSvc.DoSomething())

	// Verify service not found error
	_, err = manager.ResolveService("non-existent")
	assert.ErrorIs(t, err, modulex.ErrServiceNotFound)

	// Verify registry locking after initialization
	err = manager.RegisterService("late.Service", &MockServiceImpl{})
	assert.ErrorIs(t, err, modulex.ErrRegistryLocked)

	// Verify HTTP Routing
	ts := httptest.NewServer(router)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/module-a")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "module-a active", string(body))

	resp, err = http.Get(ts.URL + "/module-b")
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "module-b active", string(body))

	// 2. Start modules
	err = manager.StartModules(ctx)
	require.NoError(t, err)
	assert.True(t, modA.startCalled)
	assert.True(t, modB.startCalled)
	assert.Equal(t, []string{"module-a", "module-b"}, startSeq)

	// 3. Stop modules (Should be in reverse topological order: b then a)
	err = manager.StopModules(ctx)
	require.NoError(t, err)
	assert.True(t, modA.stopCalled)
	assert.True(t, modB.stopCalled)
	assert.Equal(t, []string{"module-b", "module-a"}, stopSeq)
}

func TestCircularDependencyDetection(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	// module-a depends on module-b, module-b depends on module-a
	modA := NewDummyModule("module-a", []string{"module-b"}, &initSeq, &startSeq, &stopSeq)
	modB := NewDummyModule("module-b", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)

	manager.RegisterModule(modA)
	manager.RegisterModule(modB)

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrCircularDependency)
}

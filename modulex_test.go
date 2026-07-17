package modulex_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gochi "github.com/go-chi/chi/v5"
	"github.com/mediusfy/modulex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
	if m.initSequence != nil {
		*m.initSequence = append(*m.initSequence, m.name)
	}

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
	if m.startSequence != nil {
		*m.startSequence = append(*m.startSequence, m.name)
	}
	return nil
}

func (m *DummyModule) Stop(ctx context.Context) error {
	m.stopCalled = true
	if m.stopSequence != nil {
		*m.stopSequence = append(*m.stopSequence, m.name)
	}
	return nil
}

// InMemoryEventBus acts as a mock/in-memory EventBus implementation.
type InMemoryEventBus struct {
	mu          sync.Mutex
	subscribers map[string][]modulex.EventHandler
	published   [][]byte
}

func NewInMemoryEventBus() *InMemoryEventBus {
	return &InMemoryEventBus{
		subscribers: make(map[string][]modulex.EventHandler),
	}
}

func (eb *InMemoryEventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	eb.mu.Lock()
	handlers := eb.subscribers[topic]
	eb.published = append(eb.published, payload)
	eb.mu.Unlock()

	for _, h := range handlers {
		_ = h(ctx, payload)
	}
	return nil
}

func (eb *InMemoryEventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subscribers[topic] = append(eb.subscribers[topic], handler)
	return nil
}

func (eb *InMemoryEventBus) Close(ctx context.Context) error {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.subscribers = make(map[string][]modulex.EventHandler)
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

	eb := NewInMemoryEventBus()
	manager := modulex.NewManager(router, eb, logger, configLoader)

	var initSeq, startSeq, stopSeq []string
	modB := NewDummyModule("module-b", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)
	modA := NewDummyModule("module-a", nil, &initSeq, &startSeq, &stopSeq)

	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modA))

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
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "module-a active", string(body))

	resp, err = http.Get(ts.URL + "/module-b")
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "module-b active", string(body))

	// 2. Start modules
	err = manager.StartModules(ctx)
	require.NoError(t, err)
	assert.True(t, modA.startCalled)
	assert.True(t, modB.startCalled)
	assert.Equal(t, []string{"module-a", "module-b"}, startSeq)

	// 3. Stop modules
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
	modA := NewDummyModule("module-a", []string{"module-b"}, &initSeq, &startSeq, &stopSeq)
	modB := NewDummyModule("module-b", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrCircularDependency)
}

func TestTracesNoGaps(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)

	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", nil, &initSeq, &startSeq, &stopSeq)
	require.NoError(t, manager.RegisterModule(modA))

	ctx := context.Background()

	err := manager.InitModules(ctx)
	require.NoError(t, err)

	spans := sr.Ended()
	require.Len(t, spans, 2)

	var parentSpan, childSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "InitModules" {
			parentSpan = s
		} else if s.Name() == "InitModule:module-a" {
			childSpan = s
		}
	}

	require.NotNil(t, parentSpan)
	require.NotNil(t, childSpan)

	assert.Equal(t, parentSpan.SpanContext().SpanID(), childSpan.Parent().SpanID())
	assert.Equal(t, parentSpan.SpanContext().TraceID(), childSpan.SpanContext().TraceID())
}

func TestBackgroundGoTracingPropagation(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)

	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	ctx, parentSpan := otel.Tracer("test").Start(context.Background(), "RootTask")

	var wg sync.WaitGroup
	wg.Add(1)

	manager.Go(ctx, "BackgroundTask", func(bgCtx context.Context) {
		defer wg.Done()
		_, activeSpan := otel.Tracer("test").Start(bgCtx, "NestedTask")
		activeSpan.End()
	})

	wg.Wait()
	parentSpan.End()

	spans := sr.Ended()
	require.Len(t, spans, 3)

	var bgSpan, nestedSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "BackgroundTask" {
			bgSpan = s
		} else if s.Name() == "NestedTask" {
			nestedSpan = s
		}
	}

	require.NotNil(t, bgSpan)
	require.NotNil(t, nestedSpan)

	assert.Equal(t, parentSpan.SpanContext().TraceID(), bgSpan.SpanContext().TraceID())
	assert.Equal(t, parentSpan.SpanContext().TraceID(), nestedSpan.SpanContext().TraceID())

	assert.Equal(t, parentSpan.SpanContext().SpanID(), bgSpan.Parent().SpanID())
	assert.Equal(t, bgSpan.SpanContext().SpanID(), nestedSpan.Parent().SpanID())
}

func TestEventBusIntegration(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eb := NewInMemoryEventBus()

	manager := modulex.NewManager(router, eb, logger, nil)

	var received []byte
	err := manager.EventBus().Subscribe(context.Background(), "test.topic", func(ctx context.Context, payload []byte) error {
		received = payload
		return nil
	})
	require.NoError(t, err)

	err = manager.EventBus().Publish(context.Background(), "test.topic", []byte("hello"))
	require.NoError(t, err)

	assert.Equal(t, []byte("hello"), received)
}

func TestWatermillEventBusIntegration(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eb := modulex.NewWatermillEventBus(10, false, false)
	defer func() { _ = eb.Close(context.Background()) }()

	manager := modulex.NewManager(router, eb, logger, nil)

	var wg sync.WaitGroup
	wg.Add(1)

	var received []byte
	err := manager.EventBus().Subscribe(context.Background(), "watermill.topic", func(ctx context.Context, payload []byte) error {
		received = payload
		wg.Done()
		return nil
	})
	require.NoError(t, err)

	err = manager.EventBus().Publish(context.Background(), "watermill.topic", []byte("watermill-data"))
	require.NoError(t, err)

	wg.Wait()
	assert.Equal(t, []byte("watermill-data"), received)
}

func TestRegisterModuleValidation(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("nil module", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		err := manager.RegisterModule(nil)
		assert.ErrorIs(t, err, modulex.ErrModuleNil)
	})

	t.Run("empty module name", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("", nil, nil, nil, nil)
		err := manager.RegisterModule(mod)
		assert.ErrorIs(t, err, modulex.ErrInvalidModuleName)
	})

	t.Run("whitespace module name", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("   ", nil, nil, nil, nil)
		err := manager.RegisterModule(mod)
		assert.ErrorIs(t, err, modulex.ErrInvalidModuleName)
	})

	t.Run("duplicate module name", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))

		err := manager.RegisterModule(NewDummyModule("module-a", nil, nil, nil, nil))
		assert.ErrorIs(t, err, modulex.ErrDuplicateModule)
	})

	t.Run("module registration after init", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))

		err := manager.RegisterModule(NewDummyModule("module-b", nil, nil, nil, nil))
		assert.ErrorIs(t, err, modulex.ErrRegistryLocked)
	})

	t.Run("module registration during init", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		blocking := make(chan struct{})
		resume := make(chan struct{})

		modA := &blockingModule{
			name:   "module-a",
			block:  blocking,
			resume: resume,
		}
		require.NoError(t, manager.RegisterModule(modA))

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.InitModules(context.Background())
		}()

		<-blocking // wait until InitModules is inside module A's Init
		err := manager.RegisterModule(NewDummyModule("module-b", nil, nil, nil, nil))
		assert.ErrorIs(t, err, modulex.ErrRegistryLocked)

		close(resume)
		wg.Wait()
	})
}

type blockingModule struct {
	name   string
	block  chan struct{}
	resume chan struct{}
}

func (m *blockingModule) Name() string        { return m.name }
func (m *blockingModule) DependsOn() []string { return nil }
func (m *blockingModule) Init(ctx context.Context, reg modulex.Registry) error {
	close(m.block)
	<-m.resume
	return nil
}
func (m *blockingModule) Start(ctx context.Context) error { return nil }
func (m *blockingModule) Stop(ctx context.Context) error  { return nil }

func TestRegisterServiceValidation(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("empty service name", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		err := manager.RegisterService("", &MockServiceImpl{})
		assert.ErrorIs(t, err, modulex.ErrInvalidServiceName)
	})

	t.Run("duplicate service key", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		require.NoError(t, manager.RegisterService("svc", &MockServiceImpl{}))

		err := manager.RegisterService("svc", &MockServiceImpl{})
		assert.ErrorIs(t, err, modulex.ErrDuplicateService)
	})

	t.Run("whitespace service name", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		err := manager.RegisterService("   ", &MockServiceImpl{})
		assert.ErrorIs(t, err, modulex.ErrInvalidServiceName)
	})
}

func TestSelfDependencyDetection(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)
	require.NoError(t, manager.RegisterModule(modA))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrSelfDependency)
}

func TestUnknownDependencyDetection(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", []string{"missing-module"}, &initSeq, &startSeq, &stopSeq)
	require.NoError(t, manager.RegisterModule(modA))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrDependencyNotFound)
	assert.ErrorContains(t, err, "missing-module")
}

func TestCircularDependencyReportsPath(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", []string{"module-b"}, &initSeq, &startSeq, &stopSeq)
	modB := NewDummyModule("module-b", []string{"module-c"}, &initSeq, &startSeq, &stopSeq)
	modC := NewDummyModule("module-c", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modC))

	err := manager.InitModules(context.Background())
	require.ErrorIs(t, err, modulex.ErrCircularDependency)
	assert.ErrorContains(t, err, "module-a")
	assert.ErrorContains(t, err, "module-b")
	assert.ErrorContains(t, err, "module-c")
}

func TestRegistrationOrderTieBreak(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", nil, &initSeq, &startSeq, &stopSeq)
	modB := NewDummyModule("module-b", nil, &initSeq, &startSeq, &stopSeq)
	modC := NewDummyModule("module-c", nil, &initSeq, &startSeq, &stopSeq)

	// Register in a specific order; independent modules should initialize in that order.
	require.NoError(t, manager.RegisterModule(modC))
	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	require.NoError(t, manager.InitModules(context.Background()))
	assert.Equal(t, []string{"module-c", "module-a", "module-b"}, initSeq)
}

func TestDependencyOrderOverridesRegistrationOrder(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initSeq, startSeq, stopSeq []string
	modA := NewDummyModule("module-a", nil, &initSeq, &startSeq, &stopSeq)
	modB := NewDummyModule("module-b", []string{"module-a"}, &initSeq, &startSeq, &stopSeq)

	// Register dependent module first.
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modA))

	require.NoError(t, manager.InitModules(context.Background()))
	assert.Equal(t, []string{"module-a", "module-b"}, initSeq)
}

func TestGraphValidationTable(t *testing.T) {
	tests := []struct {
		name    string
		modules []struct {
			name string
			deps []string
		}
		wantErr   error
		wantOrder []string
	}{
		{
			name: "simple chain",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: nil},
				{name: "b", deps: []string{"a"}},
				{name: "c", deps: []string{"b"}},
			},
			wantErr:   nil,
			wantOrder: []string{"a", "b", "c"},
		},
		{
			name: "self dependency",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{"a"}},
			},
			wantErr: modulex.ErrSelfDependency,
		},
		{
			name: "missing dependency",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{"missing"}},
			},
			wantErr: modulex.ErrDependencyNotFound,
		},
		{
			name: "empty dependency name",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{""}},
			},
			wantErr: modulex.ErrInvalidDependencyName,
		},
		{
			name: "whitespace dependency name",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{"   "}},
			},
			wantErr: modulex.ErrInvalidDependencyName,
		},
		{
			name: "two node cycle",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{"b"}},
				{name: "b", deps: []string{"a"}},
			},
			wantErr: modulex.ErrCircularDependency,
		},
		{
			name: "three node cycle",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: []string{"b"}},
				{name: "b", deps: []string{"c"}},
				{name: "c", deps: []string{"a"}},
			},
			wantErr: modulex.ErrCircularDependency,
		},
		{
			name: "diamond",
			modules: []struct {
				name string
				deps []string
			}{
				{name: "a", deps: nil},
				{name: "b", deps: []string{"a"}},
				{name: "c", deps: []string{"a"}},
				{name: "d", deps: []string{"b", "c"}},
			},
			wantErr:   nil,
			wantOrder: []string{"a", "b", "c", "d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gochi.NewRouter()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			manager := modulex.NewManager(router, nil, logger, nil)

			var order []string
			for _, m := range tt.modules {
				mod := NewDummyModule(m.name, m.deps, &order, &order, &order)
				require.NoError(t, manager.RegisterModule(mod))
			}

			err := manager.InitModules(context.Background())
			if tt.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, tt.wantOrder, order)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestConcurrentRegistration(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("module-%d", i)
			mod := NewDummyModule(name, nil, nil, nil, nil)
			require.NoError(t, manager.RegisterModule(mod))
		}(i)
	}

	wg.Wait()

	// Initializing with many independent modules should succeed and preserve registration order.
	err := manager.InitModules(context.Background())
	require.NoError(t, err)
}

// failModule is a test module that can be configured to fail at Init, Start, or Stop.
type failModule struct {
	name       string
	deps       []string
	initErr    error
	startErr   error
	stopErr    error
	initCalls  *[]string
	startCalls *[]string
	stopCalls  *[]string
}

func (m *failModule) Name() string        { return m.name }
func (m *failModule) DependsOn() []string { return m.deps }
func (m *failModule) Init(ctx context.Context, reg modulex.Registry) error {
	if m.initCalls != nil {
		*m.initCalls = append(*m.initCalls, m.name)
	}
	return m.initErr
}
func (m *failModule) Start(ctx context.Context) error {
	if m.startCalls != nil {
		*m.startCalls = append(*m.startCalls, m.name)
	}
	return m.startErr
}
func (m *failModule) Stop(ctx context.Context) error {
	if m.stopCalls != nil {
		*m.stopCalls = append(*m.stopCalls, m.name)
	}
	return m.stopErr
}

func TestLifecycleStateTransitions(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("configuring -> initialized", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))

		require.NoError(t, manager.InitModules(context.Background()))
		assert.Equal(t, modulex.StateInitialized, manager.State())
	})

	t.Run("initialized -> running", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))

		require.NoError(t, manager.StartModules(context.Background()))
		assert.Equal(t, modulex.StateRunning, manager.State())
	})

	t.Run("running -> stopped", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))
		require.NoError(t, manager.StartModules(context.Background()))

		require.NoError(t, manager.StopModules(context.Background()))
		assert.Equal(t, modulex.StateStopped, manager.State())
	})

	t.Run("stop is idempotent", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))
		require.NoError(t, manager.StartModules(context.Background()))

		require.NoError(t, manager.StopModules(context.Background()))
		require.NoError(t, manager.StopModules(context.Background()))
		assert.Equal(t, modulex.StateStopped, manager.State())
	})

	t.Run("stop from configured state", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		require.NoError(t, manager.StopModules(context.Background()))
		assert.Equal(t, modulex.StateStopped, manager.State())
	})

	t.Run("stop from initialized state", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))

		require.NoError(t, manager.StopModules(context.Background()))
		assert.Equal(t, modulex.StateStopped, manager.State())
	})

	t.Run("init cannot be called twice", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))
		require.NoError(t, manager.InitModules(context.Background()))

		err := manager.InitModules(context.Background())
		assert.ErrorIs(t, err, modulex.ErrInvalidLifecycleState)
	})

	t.Run("start before init fails", func(t *testing.T) {
		manager := modulex.NewManager(router, nil, logger, nil)
		mod := NewDummyModule("module-a", nil, nil, nil, nil)
		require.NoError(t, manager.RegisterModule(mod))

		err := manager.StartModules(context.Background())
		assert.ErrorIs(t, err, modulex.ErrInvalidLifecycleState)
	})
}

func TestInitFailureRollback(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var initCalls, stopCalls []string
	modA := &failModule{name: "module-a", initCalls: &initCalls, stopCalls: &stopCalls}
	modB := &failModule{name: "module-b", deps: []string{"module-a"}, initCalls: &initCalls, stopCalls: &stopCalls, initErr: errors.New("module-b init failed")}
	modC := &failModule{name: "module-c", deps: []string{"module-b"}, initCalls: &initCalls, stopCalls: &stopCalls}

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modC))

	err := manager.InitModules(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "module-b init failed")

	// module-a initialized successfully and should be stopped during rollback.
	assert.Equal(t, []string{"module-a", "module-b"}, initCalls)
	assert.Equal(t, []string{"module-a"}, stopCalls)

	// Manager should be in stopped state.
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestStartFailureReverseStop(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	var startCalls, stopCalls []string
	modA := &failModule{name: "module-a", startCalls: &startCalls, stopCalls: &stopCalls}
	modB := &failModule{name: "module-b", deps: []string{"module-a"}, startCalls: &startCalls, stopCalls: &stopCalls, startErr: errors.New("module-b start failed")}
	modC := &failModule{name: "module-c", deps: []string{"module-b"}, startCalls: &startCalls, stopCalls: &stopCalls}

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modC))
	require.NoError(t, manager.InitModules(context.Background()))

	err := manager.StartModules(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "module-b start failed")

	// module-a started successfully and should be stopped during rollback.
	assert.Equal(t, []string{"module-a", "module-b"}, startCalls)
	assert.Equal(t, []string{"module-a"}, stopCalls)

	// Manager should be in stopped state.
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestStopModulesAggregatesErrors(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := &failModule{name: "module-a", stopErr: errors.New("module-a stop failed")}
	modB := &failModule{name: "module-b", deps: []string{"module-a"}, stopErr: errors.New("module-b stop failed")}

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.InitModules(context.Background()))
	require.NoError(t, manager.StartModules(context.Background()))

	err := manager.StopModules(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "module-a stop failed")
	assert.ErrorContains(t, err, "module-b stop failed")
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestInitContextCancellation(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := NewDummyModule("module-a", nil, nil, nil, nil)
	modB := NewDummyModule("module-b", []string{"module-a"}, nil, nil, nil)
	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.InitModules(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestStartContextCancellation(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := NewDummyModule("module-a", nil, nil, nil, nil)
	modB := NewDummyModule("module-b", []string{"module-a"}, nil, nil, nil)
	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.InitModules(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.StartModules(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestRollbackStopErrorsAreJoined(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := &failModule{name: "module-a", stopErr: errors.New("rollback stop failed")}
	modB := &failModule{name: "module-b", deps: []string{"module-a"}, initErr: errors.New("module-b init failed")}

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	err := manager.InitModules(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "module-b init failed")
	assert.ErrorContains(t, err, "rollback stop failed")
}

func TestStopModulesContextCancellation(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := NewDummyModule("module-a", nil, nil, nil, nil)
	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.InitModules(context.Background()))
	require.NoError(t, manager.StartModules(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.StopModules(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestStartModulesAfterInitFailure(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := &failModule{name: "module-a", initErr: errors.New("init failed")}
	require.NoError(t, manager.RegisterModule(modA))

	require.Error(t, manager.InitModules(context.Background()))

	err := manager.StartModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrInvalidLifecycleState)
}

func TestStopModulesAfterInitFailure(t *testing.T) {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := modulex.NewManager(router, nil, logger, nil)

	modA := &failModule{name: "module-a", initErr: errors.New("init failed")}
	require.NoError(t, manager.RegisterModule(modA))

	require.Error(t, manager.InitModules(context.Background()))

	// StopModules should be idempotent even after a failed init.
	require.NoError(t, manager.StopModules(context.Background()))
	assert.Equal(t, modulex.StateStopped, manager.State())
}

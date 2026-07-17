package modulex_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	gochi "github.com/go-chi/chi/v5"
	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// mockModuleConfig configures a mockery MockModule for use in tests.
type mockModuleConfig struct {
	name     string
	deps     []string
	initErr  error
	startErr error
	stopErr  error
	onInit   func(reg modulex.Registry)
	onStart  func()
	onStop   func()
}

func newMockModule(t *testing.T, cfg mockModuleConfig) *mocks.MockModule {
	mod := mocks.NewMockModule(t)
	mod.On("Name").Return(cfg.name).Maybe()
	mod.On("DependsOn").Return(cfg.deps).Maybe()
	mod.On("Init", mock.Anything, mock.Anything).
		Return(cfg.initErr).
		Maybe().
		Run(func(args mock.Arguments) {
			if cfg.onInit != nil {
				cfg.onInit(args.Get(1).(modulex.Registry))
			}
		})
	mod.On("Start", mock.Anything).
		Return(cfg.startErr).
		Maybe().
		Run(func(args mock.Arguments) {
			if cfg.onStart != nil {
				cfg.onStart()
			}
		})
	mod.On("Stop", mock.Anything).
		Return(cfg.stopErr).
		Maybe().
		Run(func(args mock.Arguments) {
			if cfg.onStop != nil {
				cfg.onStop()
			}
		})
	return mod
}

// InMemoryEventBus is a fake event bus used for integration-style pub/sub
// assertions. It is kept only where the test genuinely needs messages to be
// delivered between publishers and subscribers.
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

func newTestManager(eb modulex.EventBus) *modulex.Manager {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return modulex.NewManager(router, eb, logger, nil)
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

	mockEB := mocks.NewMockEventBus(t)
	mockEB.On("Close", mock.Anything).Return(nil).Maybe()

	manager := modulex.NewManager(router, mockEB, logger, configLoader)

	var initSeq, startSeq, stopSeq []string
	modA := newMockModule(t, mockModuleConfig{
		name: "module-a",
		onInit: func(reg modulex.Registry) {
			initSeq = append(initSeq, "module-a")
			_ = reg.RegisterService("module-a.Service", &MockServiceImpl{})
			reg.Router().Get("/module-a", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("module-a active"))
			})
		},
		onStart: func() { startSeq = append(startSeq, "module-a") },
		onStop:  func() { stopSeq = append(stopSeq, "module-a") },
	})
	modB := newMockModule(t, mockModuleConfig{
		name: "module-b",
		deps: []string{"module-a"},
		onInit: func(reg modulex.Registry) {
			initSeq = append(initSeq, "module-b")
			reg.Router().Get("/module-b", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("module-b active"))
			})
		},
		onStart: func() { startSeq = append(startSeq, "module-b") },
		onStop:  func() { stopSeq = append(stopSeq, "module-b") },
	})

	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modA))

	// 1. Initialize modules
	ctx := context.Background()
	err := manager.InitModules(ctx)
	require.NoError(t, err)

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
	assert.Equal(t, []string{"module-a", "module-b"}, startSeq)

	// 3. Stop modules
	err = manager.StopModules(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"module-b", "module-a"}, stopSeq)
}

func TestCircularDependencyDetection(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a", deps: []string{"module-b"}})
	modB := newMockModule(t, mockModuleConfig{name: "module-b", deps: []string{"module-a"}})

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrCircularDependency)
}

func TestTracesNoGaps(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)

	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a"})
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

	manager := newTestManager(nil)

	ctx, parentSpan := otel.Tracer("test").Start(context.Background(), "RootTask")

	handle, err := manager.Go(ctx, "BackgroundTask", func(bgCtx context.Context) error {
		_, activeSpan := otel.Tracer("test").Start(bgCtx, "NestedTask")
		activeSpan.End()
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, handle)

	require.NoError(t, handle.Wait())
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
	manager := newTestManager(NewInMemoryEventBus())

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
	eb := modulex.NewWatermillEventBus(10, false, false)
	defer func() { _ = eb.Close(context.Background()) }()

	manager := newTestManager(eb)

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
	tests := []struct {
		name      string
		act       func(t *testing.T, manager *modulex.Manager) error
		wantErr   error
		wantState modulex.LifecycleState
	}{
		{
			name: "nil module",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.RegisterModule(nil)
			},
			wantErr:   modulex.ErrModuleNil,
			wantState: modulex.StateConfiguring,
		},
		{
			name: "empty module name",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{name: ""})
				return manager.RegisterModule(mod)
			},
			wantErr:   modulex.ErrInvalidModuleName,
			wantState: modulex.StateConfiguring,
		},
		{
			name: "whitespace module name",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{name: "   "})
				return manager.RegisterModule(mod)
			},
			wantErr:   modulex.ErrInvalidModuleName,
			wantState: modulex.StateConfiguring,
		},
		{
			name: "duplicate module name",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{name: "module-a"})
				require.NoError(t, manager.RegisterModule(mod))

				return manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"}))
			},
			wantErr:   modulex.ErrDuplicateModule,
			wantState: modulex.StateConfiguring,
		},
		{
			name: "module registration after init",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{name: "module-a"})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))

				return manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-b"}))
			},
			wantErr:   modulex.ErrRegistryLocked,
			wantState: modulex.StateInitialized,
		},
		{
			name: "module registration during init",
			act: func(t *testing.T, manager *modulex.Manager) error {
				blocking := make(chan struct{})
				resume := make(chan struct{})

				modA := newMockModule(t, mockModuleConfig{
					name: "module-a",
					onInit: func(reg modulex.Registry) {
						close(blocking)
						<-resume
					},
				})
				require.NoError(t, manager.RegisterModule(modA))

				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = manager.InitModules(context.Background())
				}()

				<-blocking // wait until InitModules is inside module A's Init
				err := manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-b"}))

				close(resume)
				wg.Wait()

				return err
			},
			wantErr:   modulex.ErrRegistryLocked,
			wantState: modulex.StateInitialized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			err := tt.act(t, manager)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantState, manager.State())
		})
	}
}

func TestRegisterServiceValidation(t *testing.T) {
	tests := []struct {
		name    string
		act     func(t *testing.T, manager *modulex.Manager) error
		wantErr error
	}{
		{
			name: "empty service name",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.RegisterService("", &MockServiceImpl{})
			},
			wantErr: modulex.ErrInvalidServiceName,
		},
		{
			name: "duplicate service key",
			act: func(t *testing.T, manager *modulex.Manager) error {
				require.NoError(t, manager.RegisterService("svc", &MockServiceImpl{}))

				return manager.RegisterService("svc", &MockServiceImpl{})
			},
			wantErr: modulex.ErrDuplicateService,
		},
		{
			name: "whitespace service name",
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.RegisterService("   ", &MockServiceImpl{})
			},
			wantErr: modulex.ErrInvalidServiceName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			err := tt.act(t, manager)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestSelfDependencyDetection(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a", deps: []string{"module-a"}})
	require.NoError(t, manager.RegisterModule(modA))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrSelfDependency)
}

func TestUnknownDependencyDetection(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a", deps: []string{"missing-module"}})
	require.NoError(t, manager.RegisterModule(modA))

	err := manager.InitModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrDependencyNotFound)
	assert.ErrorContains(t, err, "missing-module")
}

func TestCircularDependencyReportsPath(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a", deps: []string{"module-b"}})
	modB := newMockModule(t, mockModuleConfig{name: "module-b", deps: []string{"module-c"}})
	modC := newMockModule(t, mockModuleConfig{name: "module-c", deps: []string{"module-a"}})

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
	manager := newTestManager(nil)

	var order []string
	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		onInit:  func(reg modulex.Registry) { order = append(order, "module-a") },
		onStart: func() { order = append(order, "module-a") },
		onStop:  func() { order = append(order, "module-a") },
	})
	modB := newMockModule(t, mockModuleConfig{
		name:    "module-b",
		onInit:  func(reg modulex.Registry) { order = append(order, "module-b") },
		onStart: func() { order = append(order, "module-b") },
		onStop:  func() { order = append(order, "module-b") },
	})
	modC := newMockModule(t, mockModuleConfig{
		name:    "module-c",
		onInit:  func(reg modulex.Registry) { order = append(order, "module-c") },
		onStart: func() { order = append(order, "module-c") },
		onStop:  func() { order = append(order, "module-c") },
	})

	// Register in a specific order; independent modules should initialize in that order.
	require.NoError(t, manager.RegisterModule(modC))
	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	require.NoError(t, manager.InitModules(context.Background()))
	assert.Equal(t, []string{"module-c", "module-a", "module-b"}, order)
}

func TestDependencyOrderOverridesRegistrationOrder(t *testing.T) {
	manager := newTestManager(nil)

	var order []string
	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		onInit:  func(reg modulex.Registry) { order = append(order, "module-a") },
		onStart: func() { order = append(order, "module-a") },
		onStop:  func() { order = append(order, "module-a") },
	})
	modB := newMockModule(t, mockModuleConfig{
		name:    "module-b",
		deps:    []string{"module-a"},
		onInit:  func(reg modulex.Registry) { order = append(order, "module-b") },
		onStart: func() { order = append(order, "module-b") },
		onStop:  func() { order = append(order, "module-b") },
	})

	// Register dependent module first.
	require.NoError(t, manager.RegisterModule(modB))
	require.NoError(t, manager.RegisterModule(modA))

	require.NoError(t, manager.InitModules(context.Background()))
	assert.Equal(t, []string{"module-a", "module-b"}, order)
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
			manager := newTestManager(nil)

			var order []string
			for _, m := range tt.modules {
				name := m.name
				mod := newMockModule(t, mockModuleConfig{
					name:    name,
					deps:    m.deps,
					onInit:  func(reg modulex.Registry) { order = append(order, name) },
					onStart: func() { order = append(order, name) },
					onStop:  func() { order = append(order, name) },
				})
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
	manager := newTestManager(nil)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("module-%d", i)
			mod := newMockModule(t, mockModuleConfig{name: name})
			require.NoError(t, manager.RegisterModule(mod))
		}(i)
	}

	wg.Wait()

	// Initializing with many independent modules should succeed and preserve registration order.
	err := manager.InitModules(context.Background())
	require.NoError(t, err)
}

func TestLifecycleStateTransitions(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, manager *modulex.Manager)
		act       func(t *testing.T, manager *modulex.Manager) error
		wantState modulex.LifecycleState
		wantErr   error
	}{
		{
			name: "configuring -> initialized",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.InitModules(context.Background())
			},
			wantState: modulex.StateInitialized,
		},
		{
			name: "initialized -> running",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
				require.NoError(t, manager.InitModules(context.Background()))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StartModules(context.Background())
			},
			wantState: modulex.StateRunning,
		},
		{
			name: "running -> stopped",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StopModules(context.Background())
			},
			wantState: modulex.StateStopped,
		},
		{
			name: "stop is idempotent",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))
				require.NoError(t, manager.StopModules(context.Background()))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StopModules(context.Background())
			},
			wantState: modulex.StateStopped,
		},
		{
			name:  "stop from configured state",
			setup: func(t *testing.T, manager *modulex.Manager) {},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StopModules(context.Background())
			},
			wantState: modulex.StateStopped,
		},
		{
			name: "stop from initialized state",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
				require.NoError(t, manager.InitModules(context.Background()))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StopModules(context.Background())
			},
			wantState: modulex.StateStopped,
		},
		{
			name: "init cannot be called twice",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
				require.NoError(t, manager.InitModules(context.Background()))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.InitModules(context.Background())
			},
			wantState: modulex.StateInitialized,
			wantErr:   modulex.ErrInvalidLifecycleState,
		},
		{
			name: "start before init fails",
			setup: func(t *testing.T, manager *modulex.Manager) {
				require.NoError(t, manager.RegisterModule(newMockModule(t, mockModuleConfig{name: "module-a"})))
			},
			act: func(t *testing.T, manager *modulex.Manager) error {
				return manager.StartModules(context.Background())
			},
			wantState: modulex.StateConfiguring,
			wantErr:   modulex.ErrInvalidLifecycleState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			tt.setup(t, manager)

			err := tt.act(t, manager)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantState, manager.State())
		})
	}
}

func TestInitFailureRollback(t *testing.T) {
	manager := newTestManager(nil)

	var initCalls, stopCalls []string
	modA := newMockModule(t, mockModuleConfig{
		name:   "module-a",
		onInit: func(reg modulex.Registry) { initCalls = append(initCalls, "module-a") },
		onStop: func() { stopCalls = append(stopCalls, "module-a") },
	})
	modB := newMockModule(t, mockModuleConfig{
		name:    "module-b",
		deps:    []string{"module-a"},
		initErr: errors.New("module-b init failed"),
		onInit:  func(reg modulex.Registry) { initCalls = append(initCalls, "module-b") },
	})
	modC := newMockModule(t, mockModuleConfig{
		name:   "module-c",
		deps:   []string{"module-b"},
		onInit: func(reg modulex.Registry) { initCalls = append(initCalls, "module-c") },
	})

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
	manager := newTestManager(nil)

	var startCalls, stopCalls []string
	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		onStart: func() { startCalls = append(startCalls, "module-a") },
		onStop:  func() { stopCalls = append(stopCalls, "module-a") },
	})
	modB := newMockModule(t, mockModuleConfig{
		name:     "module-b",
		deps:     []string{"module-a"},
		startErr: errors.New("module-b start failed"),
		onStart:  func() { startCalls = append(startCalls, "module-b") },
	})
	modC := newMockModule(t, mockModuleConfig{
		name:    "module-c",
		deps:    []string{"module-b"},
		onStart: func() { startCalls = append(startCalls, "module-c") },
	})

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
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		stopErr: errors.New("module-a stop failed"),
	})
	modB := newMockModule(t, mockModuleConfig{
		name:    "module-b",
		deps:    []string{"module-a"},
		stopErr: errors.New("module-b stop failed"),
	})

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
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a"})
	modB := newMockModule(t, mockModuleConfig{name: "module-b", deps: []string{"module-a"}})
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
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a"})
	modB := newMockModule(t, mockModuleConfig{name: "module-b", deps: []string{"module-a"}})
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
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		stopErr: errors.New("rollback stop failed"),
	})
	modB := newMockModule(t, mockModuleConfig{
		name:    "module-b",
		deps:    []string{"module-a"},
		initErr: errors.New("module-b init failed"),
	})

	require.NoError(t, manager.RegisterModule(modA))
	require.NoError(t, manager.RegisterModule(modB))

	err := manager.InitModules(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "module-b init failed")
	assert.ErrorContains(t, err, "rollback stop failed")
}

func TestStopModulesContextCancellation(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{name: "module-a"})
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
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		initErr: errors.New("init failed"),
	})
	require.NoError(t, manager.RegisterModule(modA))

	require.Error(t, manager.InitModules(context.Background()))

	err := manager.StartModules(context.Background())
	assert.ErrorIs(t, err, modulex.ErrInvalidLifecycleState)
}

func TestStopModulesAfterInitFailure(t *testing.T) {
	manager := newTestManager(nil)

	modA := newMockModule(t, mockModuleConfig{
		name:    "module-a",
		initErr: errors.New("init failed"),
	})
	require.NoError(t, manager.RegisterModule(modA))

	require.Error(t, manager.InitModules(context.Background()))

	// StopModules should be idempotent even after a failed init.
	require.NoError(t, manager.StopModules(context.Background()))
	assert.Equal(t, modulex.StateStopped, manager.State())
}

func TestSupervisedTaskGo(t *testing.T) {
	sentinelErr := errors.New("task failed")

	tests := []struct {
		name   string
		policy modulex.PanicPolicy
		act    func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error)
		assert func(t *testing.T, handle *modulex.TaskHandle, err error)
	}{
		{
			name: "successful task returns nil and exposes its name",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				return manager.Go(context.Background(), "ok-task", func(ctx context.Context) error {
					return nil
				})
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.NoError(t, err)
				require.NotNil(t, handle)
				assert.Equal(t, "ok-task", handle.Name())
				assert.NoError(t, handle.Wait())
			},
		},
		{
			name: "task error is returned by Wait",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				return manager.Go(context.Background(), "err-task", func(ctx context.Context) error {
					return sentinelErr
				})
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.NoError(t, err)
				assert.ErrorIs(t, handle.Wait(), sentinelErr)
			},
		},
		{
			name: "duplicate task name is rejected",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				_, err := manager.Go(context.Background(), "dup-task", func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				})
				require.NoError(t, err)
				return manager.Go(context.Background(), "dup-task", func(ctx context.Context) error {
					return nil
				})
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				assert.ErrorIs(t, err, modulex.ErrDuplicateTask)
			},
		},
		{
			name: "task rejected after manager is stopped",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				require.NoError(t, manager.StopModules(context.Background()))
				return manager.Go(context.Background(), "late-task", func(ctx context.Context) error {
					return nil
				})
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				assert.ErrorIs(t, err, modulex.ErrRegistryLocked)
			},
		},
		{
			name: "panic is recovered and reported by default",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				return manager.Go(context.Background(), "panic-task", func(ctx context.Context) error {
					panic("simulated panic")
				})
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.NoError(t, err)
				waitErr := handle.Wait()
				require.Error(t, waitErr)
				assert.ErrorContains(t, waitErr, "simulated panic")
			},
		},
		{
			name: "empty task name is rejected",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				return manager.Go(context.Background(), "   ", func(ctx context.Context) error { return nil })
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				assert.ErrorIs(t, err, modulex.ErrInvalidTaskName)
			},
		},
		{
			name: "nil task function is rejected",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				return manager.Go(context.Background(), "nil-fn", nil)
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.Error(t, err)
				assert.ErrorContains(t, err, "task function must not be nil")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManagerWithPanicPolicy(tt.policy)
			defer func() { _ = manager.StopModules(context.Background()) }()
			handle, err := tt.act(t, manager)
			tt.assert(t, handle, err)
		})
	}
}

func TestSupervisedTaskShutdown(t *testing.T) {
	sentinelErr := errors.New("background task failed")

	tests := []struct {
		name   string
		act    func(t *testing.T, manager *modulex.Manager) error
		assert func(t *testing.T, manager *modulex.Manager, err error)
	}{
		{
			name: "tasks are cancelled and awaited before modules stop",
			act: func(t *testing.T, manager *modulex.Manager) error {
				var handle *modulex.TaskHandle
				var taskStarted, taskFinished sync.WaitGroup
				taskStarted.Add(1)
				taskFinished.Add(1)

				mod := newMockModule(t, mockModuleConfig{
					name: "mod-a",
					onStart: func() {
						h, err := manager.Go(context.Background(), "blocking-task", func(ctx context.Context) error {
							taskStarted.Done()
							<-ctx.Done()
							taskFinished.Done()
							return nil
						})
						require.NoError(t, err)
						handle = h
					},
					onStop: func() {
						require.NotNil(t, handle)
						assert.NoError(t, handle.Wait())
					},
				})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))

				taskStarted.Wait()
				err := manager.StopModules(context.Background())
				taskFinished.Wait()
				return err
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.NoError(t, err)
				assert.Equal(t, modulex.StateStopped, manager.State())
			},
		},
		{
			name: "task errors are joined into StopModules error",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{
					name: "mod-a",
					onStart: func() {
						_, err := manager.Go(context.Background(), "failing-task", func(ctx context.Context) error {
							return sentinelErr
						})
						require.NoError(t, err)
					},
				})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))
				return manager.StopModules(context.Background())
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.Error(t, err)
				assert.ErrorIs(t, err, sentinelErr)
				assert.ErrorContains(t, err, "failing-task")
			},
		},
		{
			name: "new tasks are rejected while StopModules is in progress",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{
					name: "mod-a",
					onStart: func() {
						_, err := manager.Go(context.Background(), "blocking-task", func(ctx context.Context) error {
							<-ctx.Done()
							return nil
						})
						require.NoError(t, err)
					},
				})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))

				stopDone := make(chan error, 1)
				go func() { stopDone <- manager.StopModules(context.Background()) }()

				var startErr error
				require.Eventually(t, func() bool {
					_, startErr = manager.Go(context.Background(), "late-task", func(ctx context.Context) error {
						return nil
					})
					return errors.Is(startErr, modulex.ErrRegistryLocked)
				}, 2*time.Second, 10*time.Millisecond)

				return <-stopDone
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name: "StopModules timeout is reported when tasks ignore cancellation",
			act: func(t *testing.T, manager *modulex.Manager) error {
				mod := newMockModule(t, mockModuleConfig{
					name: "mod-a",
					onStart: func() {
						_, err := manager.Go(context.Background(), "ignore-cancel-task", func(ctx context.Context) error {
							<-make(chan struct{}) // never returns
							return nil
						})
						require.NoError(t, err)
					},
				})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				require.NoError(t, manager.StartModules(context.Background()))

				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				return manager.StopModules(ctx)
			},
			assert: func(t *testing.T, manager *modulex.Manager, err error) {
				require.Error(t, err)
				assert.ErrorIs(t, err, context.DeadlineExceeded)
				assert.ErrorContains(t, err, "timed out waiting for tasks")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			err := tt.act(t, manager)
			tt.assert(t, manager, err)
		})
	}
}

func TestSupervisedTasksConcurrentGo(t *testing.T) {
	manager := newTestManager(nil)

	const n = 100
	var started sync.WaitGroup
	started.Add(n)

	for i := 0; i < n; i++ {
		name := fmt.Sprintf("concurrent-task-%d", i)
		_, err := manager.Go(context.Background(), name, func(ctx context.Context) error {
			started.Done()
			<-ctx.Done()
			return nil
		})
		require.NoError(t, err)
	}

	started.Wait()
	require.NoError(t, manager.StopModules(context.Background()))
}

func newTestManagerWithPanicPolicy(policy modulex.PanicPolicy) *modulex.Manager {
	router := gochi.NewRouter()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return modulex.NewManager(router, nil, logger, nil, modulex.WithPanicPolicy(policy))
}

func TestSupervisedTaskLifecycleFailures(t *testing.T) {
	tests := []struct {
		name   string
		act    func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error)
		assert func(t *testing.T, handle *modulex.TaskHandle, err error)
	}{
		{
			name: "tasks started during Init are cancelled when Init fails",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				var handle *modulex.TaskHandle
				mod := newMockModule(t, mockModuleConfig{
					name: "failing-init",
					onInit: func(reg modulex.Registry) {
						h, err := manager.Go(context.Background(), "init-task", func(ctx context.Context) error {
							<-ctx.Done()
							return nil
						})
						require.NoError(t, err)
						handle = h
					},
					initErr: errors.New("init failed"),
				})
				require.NoError(t, manager.RegisterModule(mod))
				err := manager.InitModules(context.Background())
				return handle, err
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.Error(t, err)
				require.NotNil(t, handle)
				assert.NoError(t, handle.Wait())
			},
		},
		{
			name: "tasks started during Start are cancelled when Start fails",
			act: func(t *testing.T, manager *modulex.Manager) (*modulex.TaskHandle, error) {
				var handle *modulex.TaskHandle
				mod := newMockModule(t, mockModuleConfig{
					name: "failing-start",
					onStart: func() {
						h, err := manager.Go(context.Background(), "start-task", func(ctx context.Context) error {
							<-ctx.Done()
							return nil
						})
						require.NoError(t, err)
						handle = h
					},
					startErr: errors.New("start failed"),
				})
				require.NoError(t, manager.RegisterModule(mod))
				require.NoError(t, manager.InitModules(context.Background()))
				err := manager.StartModules(context.Background())
				return handle, err
			},
			assert: func(t *testing.T, handle *modulex.TaskHandle, err error) {
				require.Error(t, err)
				require.NotNil(t, handle)
				assert.NoError(t, handle.Wait())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(nil)
			handle, err := tt.act(t, manager)
			tt.assert(t, handle, err)
			assert.Equal(t, modulex.StateStopped, manager.State())
		})
	}
}

func TestPanicPolicyPropagate(t *testing.T) {
	if os.Getenv("MODULEX_PANIC_PROPAGATE_HELPER") == "1" {
		manager := newTestManagerWithPanicPolicy(modulex.PanicPolicyPropagate)
		handle, err := manager.Go(context.Background(), "panic-propagate-task", func(ctx context.Context) error {
			panic("propagated panic")
		})
		require.NoError(t, err)
		// Wait until the panic has been recorded; the task goroutine will then
		// re-panic and crash the helper process.
		_ = handle.Wait()
		<-time.After(2 * time.Second)
		t.Fatal("expected panic did not occur")
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPanicPolicyPropagate$", "-test.v")
	cmd.Env = append(os.Environ(), "MODULEX_PANIC_PROPAGATE_HELPER=1")
	cmd.Dir = "."

	out, err := cmd.CombinedOutput()
	t.Logf("subprocess output:\n%s", out)
	require.Error(t, err, "expected subprocess to exit because of an unrecovered panic")
	assert.Contains(t, string(out), "propagated panic")
}

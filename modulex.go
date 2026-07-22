package modulex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

var (
	// ErrCircularDependency is returned when registered modules contain a circular dependency.
	ErrCircularDependency = errors.New("circular dependency detected")

	// ErrServiceNotFound is returned when a requested service is not registered in the locator.
	ErrServiceNotFound = errors.New("service not found")

	// ErrRegistryLocked is returned when a module attempts to register a service after registry initialization has completed.
	ErrRegistryLocked = errors.New("registry is locked: cannot register services after initialization")

	// ErrModuleNil is returned when a nil module is passed to RegisterModule.
	ErrModuleNil = errors.New("module must not be nil")

	// ErrInvalidModuleName is returned when a module name is empty or whitespace-only.
	ErrInvalidModuleName = errors.New("module name must not be empty")

	// ErrDuplicateModule is returned when RegisterModule is called with a module name that is already registered.
	ErrDuplicateModule = errors.New("module already registered")

	// ErrDuplicateService is returned when RegisterService is called with a service key that is already registered.
	ErrDuplicateService = errors.New("service already registered")

	// ErrDuplicateTask is returned when Go is called with a task name that is already in use.
	ErrDuplicateTask = errors.New("task already exists")

	// ErrInvalidTaskName is returned when Go is called with an empty or whitespace-only task name.
	ErrInvalidTaskName = errors.New("task name must not be empty")

	// ErrDependencyNotFound is returned when a module depends on a module that has not been registered.
	ErrDependencyNotFound = errors.New("module dependency not found")

	// ErrSelfDependency is returned when a module declares itself as a dependency.
	ErrSelfDependency = errors.New("module cannot depend on itself")

	// ErrInvalidDependencyName is returned when a module declares a dependency with an empty or whitespace-only name.
	ErrInvalidDependencyName = errors.New("module dependency name must not be empty")

	// ErrInvalidServiceName is returned when a service is registered with an empty or whitespace-only key.
	ErrInvalidServiceName = errors.New("service name must not be empty")

	// ErrServiceTypeMismatch is returned when a resolved service cannot be type-asserted to the requested type.
	ErrServiceTypeMismatch = errors.New("service type mismatch")

	// ErrInvalidLifecycleState is returned when a lifecycle operation is requested while the manager is in an incompatible state.
	ErrInvalidLifecycleState = errors.New("invalid lifecycle state")

	// ErrNoConfigLoader is returned by GetConfig when no config loader was
	// configured at construction time or via WithConfigLoader.
	ErrNoConfigLoader = errors.New("no config loader configured")

	// ErrInvalidPanicPolicy is returned by NewManager when WithPanicPolicy is
	// given a value outside the defined PanicPolicy enum.
	ErrInvalidPanicPolicy = errors.New("invalid panic policy")
)

// LifecycleState represents the current phase of the Manager's lifecycle.
type LifecycleState int

const (
	StateConfiguring LifecycleState = iota
	StateInitializing
	StateInitialized
	StateStarting
	StateRunning
	StateStopping
	StateStopped
)

func (s LifecycleState) String() string {
	switch s {
	case StateConfiguring:
		return "configuring"
	case StateInitializing:
		return "initializing"
	case StateInitialized:
		return "initialized"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// PanicPolicy controls how the manager reacts to a panic in a supervised task.
type PanicPolicy int

const (
	// PanicPolicyLog recovers from the panic, records it as a task error, and logs it.
	PanicPolicyLog PanicPolicy = iota

	// PanicPolicyPropagate allows the panic to crash the application.
	PanicPolicyPropagate
)

func (p PanicPolicy) valid() bool {
	switch p {
	case PanicPolicyLog, PanicPolicyPropagate:
		return true
	default:
		return false
	}
}

// noopEventBus is a no-op EventBus used when NewManager is constructed
// without one, keeping the event bus optional for consumers that only use
// direct constructor injection.
type noopEventBus struct{}

func (noopEventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	return nil
}

func (noopEventBus) Subscribe(ctx context.Context, topic string, handler EventHandler) error {
	return nil
}

func (noopEventBus) Close(ctx context.Context) error {
	return nil
}

// TaskHandle identifies a supervised background task started by Manager.Go.
// Callers can use it to wait for completion or inspect the final error.
type TaskHandle struct {
	name string
	done chan struct{}
	mu   sync.Mutex
	err  error
}

// Name returns the task name.
func (h *TaskHandle) Name() string { return h.name }

// Wait blocks until the task finishes and returns its final error.
func (h *TaskHandle) Wait() error {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func (h *TaskHandle) finish(err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
	// close is done outside the lock so Wait() callers blocked on <-h.done can
	// observe the final error after acquiring the lock. Changing this ordering
	// would introduce a race.
	close(h.done)
}

// Tracer abstracts span creation so the core package does not depend on a
// concrete OpenTelemetry implementation. A nil Tracer defaults to a no-op
// implementation.
type Tracer interface {
	// Start creates a new span and returns a context that carries it.
	Start(ctx context.Context, spanName string, attrs map[string]any) (context.Context, Span)
	// SpanContextFromContext extracts the current span context from ctx.
	SpanContextFromContext(ctx context.Context) SpanContext
	// ContextWithSpanContext returns a new context carrying the provided span
	// context. It is used to propagate trace ancestry to manager-owned task
	// goroutines that run on a different base context.
	ContextWithSpanContext(ctx context.Context, sc SpanContext) context.Context
}

// SpanContext is an opaque span context used for trace propagation.
type SpanContext interface {
	// IsValid reports whether the span context identifies a valid span.
	IsValid() bool
	// TraceID returns the trace identifier.
	TraceID() string
	// SpanID returns the span identifier.
	SpanID() string
}

// Span is a minimal lifecycle span created by a Tracer.
type Span interface {
	// End completes the span.
	End()
	// RecordError attaches an error to the span.
	RecordError(err error)
	// SetAttributes attaches key-value pairs to the span.
	SetAttributes(attrs map[string]any)
}

// noopSpanContext is a no-op SpanContext implementation.
type noopSpanContext struct{}

func (noopSpanContext) IsValid() bool   { return false }
func (noopSpanContext) TraceID() string { return "" }
func (noopSpanContext) SpanID() string  { return "" }

// noopSpan is a no-op Span implementation.
type noopSpan struct{}

func (noopSpan) End()                               {}
func (noopSpan) RecordError(err error)              {}
func (noopSpan) SetAttributes(attrs map[string]any) {}

// noopTracer is a no-op Tracer implementation.
type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, spanName string, attrs map[string]any) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (noopTracer) SpanContextFromContext(ctx context.Context) SpanContext {
	return noopSpanContext{}
}

func (noopTracer) ContextWithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return ctx
}

// ManagerOption configures a Manager during construction.
type ManagerOption func(*Manager)

// WithPanicPolicy sets the panic policy for supervised background tasks.
func WithEventBus(eb EventBus) ManagerOption {
	return func(m *Manager) {
		m.eventBus = eb
	}
}

func WithLogger(logger *slog.Logger) ManagerOption {
	return func(m *Manager) {
		m.loggerCtx = logger
	}
}

func WithPanicPolicy(policy PanicPolicy) ManagerOption {
	return func(m *Manager) {
		m.panicPolicy = policy
	}
}

// WithConfigLoader configures the config loader after construction. This is
// useful when using the options pattern and keeps the positional configLoader
// argument nil-safe.
func WithConfigLoader(loader func(target interface{}) error) ManagerOption {
	return func(m *Manager) {
		m.configLoader = loader
	}
}

// WithTracer injects a Tracer implementation into the Manager. If nil, a no-op
// tracer is used so tracing remains optional for core consumers.
func WithTracer(tracer Tracer) ManagerOption {
	return func(m *Manager) {
		m.tracer = tracer
	}
}

// EventHandler is a generic callback function signature for incoming events.
type EventHandler func(ctx context.Context, payload []byte) error

// EventBus abstracts the underlying message broker (NATS, Kafka, RabbitMQ, etc.).
type EventBus interface {
	// Publish sends a payload to a specific topic/subject.
	Publish(ctx context.Context, topic string, payload []byte) error

	// Subscribe listens to a topic and invokes the handler when an event is received.
	Subscribe(ctx context.Context, topic string, handler EventHandler) error

	// Close gracefully disconnects from the broker, shutting down active subscribers.
	Close(ctx context.Context) error
}

// Module represents a self-contained feature module that complies with Hexagonal Architecture.
// It acts as the composition root of the feature, instantiating services and adapters,
// and wiring them through the central registry.
//
// Start and Stop are optional lifecycle capabilities. A module that needs to run
// background work during startup implements Startable; a module that owns resources
// that must be released implements Stoppable. The manager skips modules that do not
// implement these interfaces, so simple modules do not require no-op methods.
type Module interface {
	// Name returns the unique, kebab-case name of the feature module.
	Name() string

	// DependsOn returns the names of other modules that this module depends on.
	// The Manager uses this list to sort the modules topologically before initialization.
	DependsOn() []string

	// Init initializes the module with the registry.
	// This is where modules register their services, register routes, and resolve dependencies.
	Init(ctx context.Context, reg Registry) error
}

// Startable is an optional lifecycle capability for modules that begin background
// work or listeners during startup. The manager calls Start after all modules have
// been initialized successfully.
type Startable interface {
	Start(ctx context.Context) error
}

// Stoppable is an optional lifecycle capability for modules that release resources
// during shutdown. The manager calls Stop in reverse topological order when
// stopping the application or rolling back a failed init/start.
type Stoppable interface {
	Stop(ctx context.Context) error
}

// ServiceRegistrar is the capability to register a service instance under a
// unique name. Registrations are only permitted before the registry has finished
// initialization.
type ServiceRegistrar interface {
	// RegisterService registers a service implementation under a unique key (e.g. "incidents.Service").
	// Returns ErrRegistryLocked if the registry has already finished initialization.
	RegisterService(name string, svc interface{}) error
}

// ServiceResolver is the capability to resolve a previously registered service
// by name.
type ServiceResolver interface {
	// ResolveService resolves a registered service implementation by name.
	// If the service is not found, it returns ErrServiceNotFound.
	ResolveService(name string) (interface{}, error)
}

// ServiceRegistry combines service registration and resolution.
type ServiceRegistry interface {
	ServiceRegistrar
	ServiceResolver
}

// EventBusProvider is the capability to access the pluggable event bus.
type EventBusProvider interface {
	// EventBus returns the pluggable, configured event bus abstraction.
	EventBus() EventBus
}

// ConfigProvider is the capability to unmarshal configuration values into a
// target structure.
type ConfigProvider interface {
	// GetConfig unmarshals configuration values into the target structure.
	// This abstract config retrieval prevents features from directly reading global configurations.
	GetConfig(target interface{}) error
}

// LoggerProvider is the capability to access the system logger.
type LoggerProvider interface {
	// Logger returns the system logger.
	Logger() *slog.Logger
}

// TaskSpawner is the capability to start a supervised background task. Tasks
// receive a lifecycle-owned context and are cancelled during shutdown.
// HealthCheckRegistrar is the capability to register a module health (liveness)
// check.
//
// A health check answers "is this process functioning correctly?" A failing
// health check means the process is broken and should be restarted (e.g. by
// an orchestrator's liveness probe). Contrast this with ReadinessRegistrar,
// which answers "should this process currently receive traffic?" — a failing
// readiness check means the instance should be pulled from load balancing,
// not restarted.
type HealthCheckRegistrar interface {
	// RegisterHealthCheck registers a health (liveness) check function under a
	// unique name.
	RegisterHealthCheck(name string, check func(context.Context) error) error
}

// HealthCheckProvider exposes the registered health (liveness) checks.
type HealthCheckProvider interface {
	// HealthChecks returns all registered health (liveness) checks.
	HealthChecks() map[string]func(context.Context) error
}

// ReadinessRegistrar is the capability to register a module readiness check.
//
// A readiness check answers "should this process currently receive traffic?"
// A failing readiness check means the instance is temporarily unable to serve
// requests (e.g. its database pool isn't warm yet, a dependency is
// unreachable, a cache hasn't primed) and should be pulled from the load
// balancer — the process itself is otherwise healthy and should not be
// restarted. Contrast this with HealthCheckRegistrar, whose checks answer "is
// this process functioning correctly?" and whose failures indicate the
// process should be restarted.
//
// The consumer defines what "ready" means for their service by registering
// named check functions; Modulex only abstracts registration, aggregation,
// and HTTP exposure (see modulex/httpx).
type ReadinessRegistrar interface {
	// RegisterReadinessCheck registers a readiness check function under a
	// unique name.
	RegisterReadinessCheck(name string, check func(context.Context) error) error
}

// ReadinessProvider exposes the registered readiness checks.
type ReadinessProvider interface {
	// ReadinessChecks returns all registered readiness checks.
	ReadinessChecks() map[string]func(context.Context) error
}

type TaskSpawner interface {
	// Go spawns a supervised goroutine to execute background work. It creates a
	// child span when a Tracer is configured, recovers from panics according to
	// the manager's panic policy, and returns a handle that can be used to wait
	// for the task to finish. Tasks are cancelled and awaited during manager
	// shutdown. Returns ErrRegistryLocked if the manager is stopping or stopped,
	// or ErrDuplicateTask if a task with the same name already exists.
	Go(ctx context.Context, taskName string, fn func(ctx context.Context) error) (*TaskHandle, error)
}

// Registry manages the collection of features and cross-cutting platform components.
// It acts as a service locator and event bus hub, preventing features from importing each other directly
// or coupling to specific messaging architectures.
//
// Registry is a composite of smaller capability interfaces. Modules that only
// need a subset of these capabilities can depend on the narrower interfaces
// (ServiceRegistry, EventBusProvider, ConfigProvider, LoggerProvider, or
// TaskSpawner) instead of the full Registry.
type Registry interface {
	ServiceRegistry
	EventBusProvider
	ConfigProvider
	LoggerProvider
	TaskSpawner
	HealthCheckRegistrar
	HealthCheckProvider
	ReadinessRegistrar
	ReadinessProvider
}

// Manager implements the Registry interface and orchestrates the module lifecycles.
type Manager struct {
	mu              sync.RWMutex
	stateMu         sync.Mutex
	taskMu          sync.Mutex
	services        map[string]interface{}
	modules         map[string]Module
	moduleOrder     []string
	orderedMods     []Module
	eventBus        EventBus
	loggerCtx       *slog.Logger
	configLoader    func(target interface{}) error
	state           LifecycleState
	tracer          Tracer
	panicPolicy     PanicPolicy
	tasks           map[string]*TaskHandle
	taskErrs        []error
	taskCtx         context.Context
	taskCancel      context.CancelFunc
	healthMu        sync.RWMutex
	healthChecks    map[string]func(context.Context) error
	readinessMu     sync.RWMutex
	readinessChecks map[string]func(context.Context) error
}

var _ Registry = (*Manager)(nil)

// NewManager creates a new instance of Manager.
//
// eb may be nil, in which case a no-op EventBus is used. logger may be nil,
// in which case slog.Default() is used. NewManager returns
// ErrInvalidPanicPolicy if WithPanicPolicy is given a value outside the
// defined PanicPolicy enum.
func NewManager(opts ...ManagerOption) (*Manager, error) {
	taskCtx, taskCancel := context.WithCancel(context.Background())
	m := &Manager{
		services:        make(map[string]interface{}),
		modules:         make(map[string]Module),
		moduleOrder:     make([]string, 0),
		eventBus:        noopEventBus{},
		loggerCtx:       slog.Default(),
		state:           StateConfiguring,
		tracer:          noopTracer{},
		panicPolicy:     PanicPolicyLog,
		tasks:           make(map[string]*TaskHandle),
		taskErrs:        make([]error, 0),
		taskCtx:         taskCtx,
		taskCancel:      taskCancel,
		healthChecks:    make(map[string]func(context.Context) error),
		readinessChecks: make(map[string]func(context.Context) error),
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.eventBus == nil {
		m.eventBus = noopEventBus{}
	}
	if m.loggerCtx == nil {
		m.loggerCtx = slog.Default()
	}
	if m.tracer == nil {
		m.tracer = noopTracer{}
	}
	if !m.panicPolicy.valid() {
		taskCancel()
		return nil, fmt.Errorf("%w: %d", ErrInvalidPanicPolicy, m.panicPolicy)
	}
	if m.configLoader == nil {
		m.loggerCtx.Warn("no config loader configured; GetConfig will return an error until WithConfigLoader is used")
	}
	return m, nil
}

// RegisterModule registers a feature module in the manager.
// Modules should be registered before calling InitModules.
//
// Registration is rejected if the module is nil, its name is empty, another
// module with the same name is already registered, or initialization has
// already started. Independent modules preserve their registration order as
// the deterministic tie-break during topological sorting.

// RegisterHealthCheck registers a health (liveness) check function under a
// unique name.
func (m *Manager) RegisterHealthCheck(name string, check func(context.Context) error) error {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if _, exists := m.healthChecks[name]; exists {
		return fmt.Errorf("health check %q already registered", name)
	}
	m.healthChecks[name] = check
	return nil
}

// HealthChecks returns all registered health (liveness) checks.
func (m *Manager) HealthChecks() map[string]func(context.Context) error {
	m.healthMu.RLock()
	defer m.healthMu.RUnlock()
	checks := make(map[string]func(context.Context) error, len(m.healthChecks))
	for k, v := range m.healthChecks {
		checks[k] = v
	}
	return checks
}

// RegisterReadinessCheck registers a readiness check function under a unique
// name.
func (m *Manager) RegisterReadinessCheck(name string, check func(context.Context) error) error {
	m.readinessMu.Lock()
	defer m.readinessMu.Unlock()
	if _, exists := m.readinessChecks[name]; exists {
		return fmt.Errorf("readiness check %q already registered", name)
	}
	m.readinessChecks[name] = check
	return nil
}

// ReadinessChecks returns all registered readiness checks.
func (m *Manager) ReadinessChecks() map[string]func(context.Context) error {
	m.readinessMu.RLock()
	defer m.readinessMu.RUnlock()
	checks := make(map[string]func(context.Context) error, len(m.readinessChecks))
	for k, v := range m.readinessChecks {
		checks[k] = v
	}
	return checks
}

func (m *Manager) RegisterModule(mod Module) error {
	m.stateMu.Lock()
	state := m.state
	if state != StateConfiguring {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: cannot register module while in %q state", ErrRegistryLocked, state)
	}
	m.stateMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if mod == nil {
		return ErrModuleNil
	}

	name := strings.TrimSpace(mod.Name())
	if name == "" {
		return ErrInvalidModuleName
	}
	if _, exists := m.modules[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateModule, name)
	}

	m.modules[name] = mod
	m.moduleOrder = append(m.moduleOrder, name)
	return nil
}

// RegisterService implements Registry. It registers a service instance to the service locator.
// Registration is only permitted before InitModules has completed.
func (m *Manager) RegisterService(name string, svc interface{}) error {
	m.stateMu.Lock()
	state := m.state
	if state != StateConfiguring && state != StateInitializing {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: cannot register service while in %q state", ErrRegistryLocked, state)
	}
	m.stateMu.Unlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidServiceName
	}
	if _, exists := m.services[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateService, name)
	}

	m.services[name] = svc
	m.loggerCtx.Info("registered service", slog.String("service", name))
	return nil
}

// ResolveService implements Registry. It retrieves a registered service by its identifier.
func (m *Manager) ResolveService(name string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svc, ok := m.services[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrServiceNotFound, name)
	}
	return svc, nil
}

// EventBus implements Registry.
func (m *Manager) EventBus() EventBus {
	return m.eventBus
}

// GetConfig implements Registry.
func (m *Manager) GetConfig(target interface{}) error {
	if m.configLoader == nil {
		return ErrNoConfigLoader
	}
	return m.configLoader(target)
}

// Logger implements Registry.

func (m *Manager) startSpan(ctx context.Context, spanName string, attrs map[string]any) (context.Context, Span) {
	parentSC := m.tracer.SpanContextFromContext(ctx)
	childCtx, span := m.tracer.Start(ctx, spanName, attrs)
	childSC := m.tracer.SpanContextFromContext(childCtx)

	if childSC.IsValid() {
		m.loggerCtx.InfoContext(childCtx, "span started",
			slog.String("span_name", spanName),
			slog.String("trace_id", childSC.TraceID()),
			slog.String("span_id", childSC.SpanID()),
			slog.String("parent_span_id", parentSC.SpanID()),
		)
	}
	return childCtx, span
}

func (m *Manager) Logger() *slog.Logger {
	return m.loggerCtx
}

// State returns the current lifecycle state of the manager.
func (m *Manager) State() LifecycleState {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	return m.state
}

func (m *Manager) setState(s LifecycleState) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.state = s
}

func (m *Manager) closeEventBus(ctx context.Context) error {
	m.mu.Lock()
	eb := m.eventBus
	m.mu.Unlock()

	if eb == nil {
		return nil
	}
	m.loggerCtx.InfoContext(ctx, "closing event bus")
	if err := eb.Close(ctx); err != nil {
		return fmt.Errorf("failed to close event bus: %w", err)
	}
	return nil
}

// Go implements Registry. It spawns a supervised background routine while
// preserving trace ancestry when a Tracer is configured, and returns a handle
// for awaiting completion.
func (m *Manager) Go(ctx context.Context, taskName string, fn func(ctx context.Context) error) (*TaskHandle, error) {
	m.stateMu.Lock()
	state := m.state
	if state == StateStopping || state == StateStopped {
		m.stateMu.Unlock()
		return nil, fmt.Errorf("%w: cannot start task %q while in %q state", ErrRegistryLocked, taskName, state)
	}
	m.stateMu.Unlock()

	taskName = strings.TrimSpace(taskName)
	if taskName == "" {
		return nil, fmt.Errorf("%w", ErrInvalidTaskName)
	}
	if fn == nil {
		return nil, fmt.Errorf("task function must not be nil")
	}

	m.taskMu.Lock()
	if m.taskCtx.Err() != nil {
		m.taskMu.Unlock()
		return nil, fmt.Errorf("%w: cannot start task %q while shutting down", ErrRegistryLocked, taskName)
	}
	if _, exists := m.tasks[taskName]; exists {
		m.taskMu.Unlock()
		return nil, fmt.Errorf("%w: task %q", ErrDuplicateTask, taskName)
	}

	handle := &TaskHandle{name: taskName, done: make(chan struct{})}
	m.tasks[taskName] = handle

	taskCtx, taskCancel := context.WithCancel(m.taskCtx)
	m.taskMu.Unlock()

	spanCtx := m.tracer.SpanContextFromContext(ctx)
	bgCtx := m.tracer.ContextWithSpanContext(taskCtx, spanCtx)

	go func() {
		var taskErr error
		defer func() {
			m.taskMu.Lock()
			if taskErr != nil {
				m.taskErrs = append(m.taskErrs, fmt.Errorf("task %q failed: %w", taskName, taskErr))
			}
			delete(m.tasks, taskName)
			m.taskMu.Unlock()
			taskCancel()
			handle.finish(taskErr)
		}()

		execCtx, span := m.startSpan(bgCtx, taskName, nil)
		defer span.End()

		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("panic in background task %q: %v", taskName, r)
				span.RecordError(err)
				m.loggerCtx.ErrorContext(execCtx, "recovered from background task panic",
					slog.String("task", taskName),
					slog.Any("panic", r),
				)
				taskErr = err
				if m.panicPolicy == PanicPolicyPropagate {
					panic(r)
				}
			}
		}()

		taskErr = fn(execCtx)
	}()

	return handle, nil
}

// InitModules sorts the modules topologically based on dependencies,
// then initializes them sequentially in dependency order inside trace spans.
//
// If a module fails to initialize, all previously initialized modules are
// stopped in reverse order and the manager moves to the stopped state.

// ExportDAG returns a Mermaid-compatible DAG visualization of the registered modules.
func (m *Manager) ExportDAG() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("graph TD\n")
	for _, name := range names {
		mod := m.modules[name]
		fmt.Fprintf(&sb, "    %s[%s]\n", name, name)
		deps := append([]string(nil), mod.DependsOn()...)
		sort.Strings(deps)
		for _, dep := range deps {
			depName := strings.TrimSpace(dep)
			if depName != "" {
				fmt.Fprintf(&sb, "    %s --> %s\n", name, depName)
			}
		}
	}
	return sb.String()
}

func (m *Manager) InitModules(ctx context.Context) error {
	m.stateMu.Lock()
	state := m.state
	if state != StateConfiguring {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: InitModules called in %q state", ErrInvalidLifecycleState, state)
	}
	m.state = StateInitializing
	m.stateMu.Unlock()

	m.mu.Lock()
	ordered, err := m.sortModules()
	if err != nil {
		m.orderedMods = nil
		m.mu.Unlock()
		if waitErr := m.waitForTasks(ctx); waitErr != nil {
			err = errors.Join(err, waitErr)
		}
		m.setState(StateStopped)
		return err
	}
	m.orderedMods = ordered
	m.mu.Unlock()

	ctx, span := m.startSpan(ctx, "InitModules", map[string]any{
		"modulex.module_count": len(ordered),
	})
	defer span.End()

	var initErr error
	initializedCount := 0
	for _, mod := range ordered {
		if err := ctx.Err(); err != nil {
			initErr = fmt.Errorf("init cancelled: %w", err)
			break
		}

		modCtx, modSpan := m.startSpan(ctx, fmt.Sprintf("InitModule:%s", mod.Name()), map[string]any{
			"modulex.module_name": mod.Name(),
		})
		if err := mod.Init(modCtx, m); err != nil {
			modSpan.RecordError(err)
			modSpan.End()
			span.RecordError(err)
			initErr = fmt.Errorf("failed to init module %q: %w", mod.Name(), err)
			break
		}
		modSpan.End()
		initializedCount++
	}

	if initErr != nil {
		if waitErr := m.waitForTasks(ctx); waitErr != nil {
			initErr = errors.Join(initErr, waitErr)
		}
		if rollbackErr := m.rollbackInit(ctx, ordered[:initializedCount]); rollbackErr != nil {
			initErr = errors.Join(initErr, rollbackErr)
		}
		m.mu.Lock()
		m.orderedMods = nil
		m.mu.Unlock()
		m.setState(StateStopped)
		return initErr
	}

	m.setState(StateInitialized)
	return nil
}

// rollbackInit stops modules that were successfully initialized before a later
// init failure. Only modules that implement Stoppable are stopped. Errors from
// individual stops are joined together.
func (m *Manager) rollbackInit(ctx context.Context, initialized []Module) error {
	var errs []error
	for i := len(initialized) - 1; i >= 0; i-- {
		mod := initialized[i]
		if err := m.stopModule(ctx, mod, "init rollback"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StartModules starts all registered modules in topological dependency order inside trace spans.
//
// If a module fails to start, all previously started modules are stopped in
// reverse order and the manager moves to the stopped state.
func (m *Manager) StartModules(ctx context.Context) error {
	m.stateMu.Lock()
	state := m.state
	if state != StateInitialized {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: StartModules called in %q state", ErrInvalidLifecycleState, state)
	}
	m.state = StateStarting
	m.stateMu.Unlock()

	m.mu.RLock()
	mods := make([]Module, len(m.orderedMods))
	copy(mods, m.orderedMods)
	m.mu.RUnlock()

	ctx, span := m.startSpan(ctx, "StartModules", map[string]any{
		"modulex.module_count": len(mods),
	})
	defer span.End()

	var startErr error
	startedCount := 0
	for _, mod := range mods {
		if err := ctx.Err(); err != nil {
			startErr = fmt.Errorf("start cancelled: %w", err)
			break
		}

		modCtx, modSpan := m.startSpan(ctx, fmt.Sprintf("StartModule:%s", mod.Name()), map[string]any{
			"modulex.module_name": mod.Name(),
		})
		if err := m.startModule(modCtx, mod); err != nil {
			modSpan.RecordError(err)
			modSpan.End()
			span.RecordError(err)
			startErr = fmt.Errorf("failed to start module %q: %w", mod.Name(), err)
			break
		}
		modSpan.End()
		startedCount++
	}

	if startErr != nil {
		if waitErr := m.waitForTasks(ctx); waitErr != nil {
			startErr = errors.Join(startErr, waitErr)
		}
		if rollbackErr := m.rollbackStart(ctx, mods[:startedCount]); rollbackErr != nil {
			startErr = errors.Join(startErr, rollbackErr)
		}
		m.setState(StateStopped)
		return startErr
	}

	m.setState(StateRunning)
	return nil
}

// rollbackStart stops modules that were successfully started before a later
// start failure. Only modules that implement Stoppable are stopped. Errors from
// individual stops are joined together.
func (m *Manager) rollbackStart(ctx context.Context, started []Module) error {
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		mod := started[i]
		if err := m.stopModule(ctx, mod, "start rollback"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// startModule invokes mod.Start if the module implements Startable. Modules that
// do not implement Startable are skipped without error.
func (m *Manager) startModule(ctx context.Context, mod Module) error {
	if s, ok := mod.(Startable); ok {
		return s.Start(ctx)
	}
	return nil
}

// stopModule invokes mod.Stop if the module implements Stoppable. Modules that do
// not implement Stoppable are skipped without error. The phase string is used for
// error messages.
func (m *Manager) stopModule(ctx context.Context, mod Module, phase string) error {
	if s, ok := mod.(Stoppable); ok {
		if err := s.Stop(ctx); err != nil {
			m.loggerCtx.ErrorContext(ctx, "failed to stop module",
				slog.String("module", mod.Name()),
				slog.String("phase", phase),
				slog.Any("error", err),
			)
			return fmt.Errorf("failed to stop module %q during %s: %w", mod.Name(), phase, err)
		}
	}
	return nil
}

// waitForTasks cancels the task context and waits for all supervised tasks to
// finish, respecting the caller's deadline. Errors from individual tasks are
// joined together.
//
// After all tasks are awaited, the shared task context is recreated so the
// manager is not left with a permanently cancelled context after a failure or
// shutdown path. New tasks are still rejected by the lifecycle state machine.
func (m *Manager) waitForTasks(ctx context.Context) error {
	m.taskMu.Lock()
	cancel := m.taskCancel
	tasks := make([]*TaskHandle, 0, len(m.tasks))
	for _, t := range m.tasks {
		tasks = append(tasks, t)
	}
	m.taskMu.Unlock()

	if cancel != nil {
		cancel()
	}

	if len(tasks) == 0 {
		m.taskMu.Lock()
		var errs []error
		errs = append(errs, m.taskErrs...)
		m.taskErrs = nil
		m.taskMu.Unlock()
		m.resetTaskCtx()
		return errors.Join(errs...)
	}

	m.loggerCtx.InfoContext(ctx, "waiting for supervised tasks to finish", slog.Int("task_count", len(tasks)))

	g, gCtx := errgroup.WithContext(ctx)

	for _, task := range tasks {
		t := task
		g.Go(func() error {
			select {
			case <-t.done:
			case <-gCtx.Done():
				// The caller's deadline expired. Return without waiting for the
				// task to finish so the manager can report the timeout promptly.
			}
			return nil
		})
	}

	// Wait for the errgroup to finish. Goroutines intentionally return nil so
	// the group waits for every task even when one fails. The errgroup context
	// is cancelled only by the caller's context deadline.
	_ = g.Wait()

	// Every supervised task records its own error into m.taskErrs (under
	// m.taskMu, in Go's completion goroutine) before signalling t.done, whether
	// it finished before this call started, while we were waiting above, or
	// (if still running) not yet at all. m.taskErrs is therefore the single
	// source of truth for task errors here: collecting them a second time via
	// each TaskHandle would double-report errors for tasks that finish mid-wait,
	// while skipping this drain on a timed-out wait would silently lose errors
	// from tasks that had already finished (and been removed from m.tasks)
	// before waitForTasks even took its snapshot above.
	m.taskMu.Lock()
	errs := append([]error(nil), m.taskErrs...)
	m.taskErrs = nil
	m.taskMu.Unlock()

	if ctx.Err() != nil {
		errs = append(errs, fmt.Errorf("timed out waiting for tasks to finish: %w", ctx.Err()))
		m.resetTaskCtx()
		return errors.Join(errs...)
	}

	m.taskMu.Lock()
	m.tasks = make(map[string]*TaskHandle)
	m.taskMu.Unlock()

	m.resetTaskCtx()
	return errors.Join(errs...)
}

// resetTaskCtx recreates the shared task context after a shutdown or failure.
func (m *Manager) resetTaskCtx() {
	m.taskMu.Lock()
	defer m.taskMu.Unlock()
	m.taskCtx, m.taskCancel = context.WithCancel(context.Background())
}

// StopModules cancels supervised tasks, stops registered modules in reverse
// topological order when they were running, and closes the EventBus.
//
// StopModules is idempotent: calling it multiple times returns nil without
// re-executing shutdown logic. It is context-aware and joins all shutdown
// errors so that no failure is silently dropped.
func (m *Manager) StopModules(ctx context.Context) error {
	m.stateMu.Lock()
	state := m.state
	switch state {
	case StateStopped, StateStopping:
		m.stateMu.Unlock()
		return nil
	case StateConfiguring, StateInitializing, StateInitialized, StateStarting, StateRunning:
		// proceed with shutdown
	default:
		m.stateMu.Unlock()
		return fmt.Errorf("%w: StopModules called in %q state", ErrInvalidLifecycleState, state)
	}
	origState := state
	m.state = StateStopping
	// Cancel the task context while we still hold stateMu so that new Go calls
	// are rejected and existing tasks receive cancellation before we release the
	// shutdown gate.
	m.taskMu.Lock()
	m.taskCancel()
	m.taskMu.Unlock()
	m.stateMu.Unlock()

	ctx, span := m.startSpan(ctx, "StopModules", nil)
	defer span.End()

	var errs []error
	if err := m.waitForTasks(ctx); err != nil {
		errs = append(errs, err)
	}

	if origState == StateRunning {
		m.mu.RLock()
		mods := make([]Module, len(m.orderedMods))
		copy(mods, m.orderedMods)
		m.mu.RUnlock()

		span.SetAttributes(map[string]any{"modulex.module_count": len(mods)})

		for i := len(mods) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				errs = append(errs, fmt.Errorf("stop cancelled: %w", err))
				break
			}
			mod := mods[i]
			modCtx, modSpan := m.startSpan(ctx, fmt.Sprintf("StopModule:%s", mod.Name()), map[string]any{
				"modulex.module_name": mod.Name(),
			})
			if err := m.stopModule(modCtx, mod, "stop"); err != nil {
				modSpan.RecordError(err)
				errs = append(errs, err)
			}
			modSpan.End()
		}
	}

	if err := m.closeEventBus(ctx); err != nil {
		errs = append(errs, err)
	}

	m.setState(StateStopped)
	return errors.Join(errs...)
}

// sortModules performs a topological sort on registered modules.
// It returns an ordered slice of modules or an error if a circular dependency,
// missing dependency, or self-dependency is detected.
//
// Independent modules are processed in registration order, which provides a
// deterministic tie-break when multiple valid orderings exist.
func (m *Manager) sortModules() ([]Module, error) {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int)
	var order []Module
	var path []string

	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			// Build the cycle path from the first occurrence of this node in path.
			cycleStart := 0
			for i, n := range path {
				if n == name {
					cycleStart = i
					break
				}
			}
			cycle := append(path[cycleStart:], name)
			return fmt.Errorf("%w: %s", ErrCircularDependency, strings.Join(cycle, " -> "))
		case visited:
			return nil
		}

		mod, exists := m.modules[name]
		if !exists {
			return fmt.Errorf("%w: %q", ErrDependencyNotFound, name)
		}

		state[name] = visiting
		path = append(path, name)

		for _, depName := range mod.DependsOn() {
			depName = strings.TrimSpace(depName)
			if depName == "" {
				path = path[:len(path)-1]
				state[name] = visited
				return fmt.Errorf("%w: module %q has an empty dependency name", ErrInvalidDependencyName, name)
			}
			if depName == name {
				path = path[:len(path)-1]
				state[name] = visited
				return fmt.Errorf("%w: %q", ErrSelfDependency, name)
			}
			if err := visit(depName); err != nil {
				path = path[:len(path)-1]
				state[name] = visited
				return err
			}
		}

		order = append(order, mod)
		path = path[:len(path)-1]
		state[name] = visited
		return nil
	}

	for _, name := range m.moduleOrder {
		if state[name] == unvisited {
			if err := visit(name); err != nil {
				return nil, err
			}
		}
	}

	return order, nil
}

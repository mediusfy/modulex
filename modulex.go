package modulex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	gochi "github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

	// ErrDependencyNotFound is returned when a module depends on a module that has not been registered.
	ErrDependencyNotFound = errors.New("module dependency not found")

	// ErrSelfDependency is returned when a module declares itself as a dependency.
	ErrSelfDependency = errors.New("module cannot depend on itself")

	// ErrInvalidDependencyName is returned when a module declares a dependency with an empty or whitespace-only name.
	ErrInvalidDependencyName = errors.New("module dependency name must not be empty")

	// ErrInvalidServiceName is returned when a service is registered with an empty or whitespace-only key.
	ErrInvalidServiceName = errors.New("service name must not be empty")

	// ErrInvalidLifecycleState is returned when a lifecycle operation is requested while the manager is in an incompatible state.
	ErrInvalidLifecycleState = errors.New("invalid lifecycle state")
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
type Module interface {
	// Name returns the unique, kebab-case name of the feature module.
	Name() string

	// DependsOn returns the names of other modules that this module depends on.
	// The Manager uses this list to sort the modules topologically before initialization.
	DependsOn() []string

	// Init initializes the module with the registry.
	// This is where modules register their services, register routes, and resolve dependencies.
	Init(ctx context.Context, reg Registry) error

	// Start starts any background tasks or listeners for the module.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the module's background tasks.
	Stop(ctx context.Context) error
}

// Registry manages the collection of features, their HTTP endpoints, and cross-cutting platform components.
// It acts as a service locator and event bus hub, preventing features from importing each other directly
// or coupling to specific messaging architectures.
type Registry interface {
	// RegisterService registers a service implementation under a unique key (e.g. "incidents.Service").
	// Returns ErrRegistryLocked if the registry has already finished initialization.
	RegisterService(name string, svc interface{}) error

	// ResolveService resolves a registered service implementation by name.
	// If the service is not found, it returns ErrServiceNotFound.
	ResolveService(name string) (interface{}, error)

	// Router returns the Chi Router for registering HTTP endpoints.
	Router() gochi.Router

	// EventBus returns the pluggable, configured event bus abstraction.
	EventBus() EventBus

	// GetConfig unmarshals configuration values into the target structure.
	// This abstract config retrieval prevents features from directly reading global configurations.
	GetConfig(target interface{}) error

	// Logger returns the system logger.
	Logger() *slog.Logger

	// Tracer returns the OpenTelemetry Tracer instance configured for this manager.
	Tracer() trace.Tracer

	// Go spawns a new goroutine to execute background work, automatically propagating
	// the context's OpenTelemetry trace context and creating a child span for the task.
	// It also includes panic recovery to prevent the application from crashing.
	Go(ctx context.Context, taskName string, fn func(ctx context.Context))
}

// Manager implements the Registry interface and orchestrates the module lifecycles.
type Manager struct {
	mu           sync.RWMutex
	stateMu      sync.Mutex
	services     map[string]interface{}
	modules      map[string]Module
	moduleOrder  []string
	orderedMods  []Module
	router       gochi.Router
	eventBus     EventBus
	loggerCtx    *slog.Logger
	configLoader func(target interface{}) error
	state        LifecycleState
	tracer       trace.Tracer
}

var _ Registry = (*Manager)(nil)

// NewManager creates a new instance of Manager.
func NewManager(router gochi.Router, eb EventBus, logger *slog.Logger, configLoader func(target interface{}) error) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		services:     make(map[string]interface{}),
		modules:      make(map[string]Module),
		moduleOrder:  make([]string, 0),
		router:       router,
		eventBus:     eb,
		loggerCtx:    logger,
		configLoader: configLoader,
		state:        StateConfiguring,
		tracer:       otel.Tracer("github.com/mediusfy/modulex"),
	}
}

// RegisterModule registers a feature module in the manager.
// Modules should be registered before calling InitModules.
//
// Registration is rejected if the module is nil, its name is empty, another
// module with the same name is already registered, or initialization has
// already started. Independent modules preserve their registration order as
// the deterministic tie-break during topological sorting.
func (m *Manager) RegisterModule(mod Module) error {
	m.stateMu.Lock()
	if m.state != StateConfiguring {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: cannot register module while in %q state", ErrRegistryLocked, m.state)
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
	if m.state != StateConfiguring && m.state != StateInitializing {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: cannot register service while in %q state", ErrRegistryLocked, m.state)
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

// Router implements Registry.
func (m *Manager) Router() gochi.Router {
	return m.router
}

// EventBus implements Registry.
func (m *Manager) EventBus() EventBus {
	return m.eventBus
}

// GetConfig implements Registry.
func (m *Manager) GetConfig(target interface{}) error {
	if m.configLoader == nil {
		return fmt.Errorf("no config loader configured")
	}
	return m.configLoader(target)
}

// Logger implements Registry.
func (m *Manager) Logger() *slog.Logger {
	return m.loggerCtx
}

// Tracer implements Registry.
func (m *Manager) Tracer() trace.Tracer {
	return m.tracer
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
	m.loggerCtx.Info("closing event bus")
	if err := eb.Close(ctx); err != nil {
		return fmt.Errorf("failed to close event bus: %w", err)
	}
	return nil
}

// Go implements Registry. It spawns a background routine while preserving OTel span ancestry.
func (m *Manager) Go(ctx context.Context, taskName string, fn func(ctx context.Context)) {
	spanCtx := trace.SpanContextFromContext(ctx)
	bgCtx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	go func() {
		bgCtx, span := m.tracer.Start(bgCtx, taskName,
			trace.WithSpanKind(trace.SpanKindInternal),
		)
		defer span.End()

		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("panic in background task %q: %v", taskName, r)
				span.RecordError(err)
				m.loggerCtx.Error("recovered from background task panic",
					slog.String("task", taskName),
					slog.Any("panic", r),
				)
			}
		}()

		fn(bgCtx)
	}()
}

// InitModules sorts the modules topologically based on dependencies,
// then initializes them sequentially in dependency order inside trace spans.
//
// If a module fails to initialize, all previously initialized modules are
// stopped in reverse order and the manager moves to the stopped state.
func (m *Manager) InitModules(ctx context.Context) error {
	m.stateMu.Lock()
	if m.state != StateConfiguring {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: InitModules called in %q state", ErrInvalidLifecycleState, m.state)
	}
	m.state = StateInitializing
	m.stateMu.Unlock()

	m.mu.Lock()
	ordered, err := m.sortModules()
	if err != nil {
		m.mu.Unlock()
		m.setState(StateStopped)
		return err
	}
	m.orderedMods = ordered
	m.mu.Unlock()

	ctx, span := m.tracer.Start(ctx, "InitModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(ordered))),
	)
	defer span.End()

	var initErr error
	initializedCount := 0
	for _, mod := range ordered {
		if err := ctx.Err(); err != nil {
			initErr = fmt.Errorf("init cancelled: %w", err)
			break
		}

		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("InitModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
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
		if rollbackErr := m.rollbackInit(ctx, ordered[:initializedCount]); rollbackErr != nil {
			initErr = errors.Join(initErr, rollbackErr)
		}
		m.setState(StateStopped)
		return initErr
	}

	m.setState(StateInitialized)
	return nil
}

// rollbackInit stops modules that were successfully initialized before a later
// init failure. Errors from individual stops are joined together.
func (m *Manager) rollbackInit(ctx context.Context, initialized []Module) error {
	var errs []error
	for i := len(initialized) - 1; i >= 0; i-- {
		mod := initialized[i]
		if err := mod.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop module %q during init rollback: %w", mod.Name(), err))
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
	if m.state != StateInitialized {
		m.stateMu.Unlock()
		return fmt.Errorf("%w: StartModules called in %q state", ErrInvalidLifecycleState, m.state)
	}
	m.state = StateStarting
	m.stateMu.Unlock()

	m.mu.RLock()
	mods := make([]Module, len(m.orderedMods))
	copy(mods, m.orderedMods)
	m.mu.RUnlock()

	ctx, span := m.tracer.Start(ctx, "StartModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(mods))),
	)
	defer span.End()

	var startErr error
	startedCount := 0
	for _, mod := range mods {
		if err := ctx.Err(); err != nil {
			startErr = fmt.Errorf("start cancelled: %w", err)
			break
		}

		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("StartModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
		if err := mod.Start(modCtx); err != nil {
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
// start failure. Errors from individual stops are joined together.
func (m *Manager) rollbackStart(ctx context.Context, started []Module) error {
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		mod := started[i]
		if err := mod.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop module %q during start rollback: %w", mod.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// StopModules stops all registered modules in reverse topological order inside trace spans and closes the EventBus.
//
// StopModules is idempotent: calling it multiple times returns nil without
// re-executing shutdown logic. It is context-aware and joins all shutdown
// errors so that no failure is silently dropped.
func (m *Manager) StopModules(ctx context.Context) error {
	m.stateMu.Lock()
	switch m.state {
	case StateStopped, StateStopping:
		m.stateMu.Unlock()
		return nil
	case StateConfiguring, StateInitialized:
		m.state = StateStopped
		m.stateMu.Unlock()
		return m.closeEventBus(ctx)
	case StateRunning:
		m.state = StateStopping
	default:
		m.stateMu.Unlock()
		return fmt.Errorf("%w: StopModules called in %q state", ErrInvalidLifecycleState, m.state)
	}
	m.stateMu.Unlock()

	m.mu.RLock()
	mods := make([]Module, len(m.orderedMods))
	copy(mods, m.orderedMods)
	m.mu.RUnlock()

	ctx, span := m.tracer.Start(ctx, "StopModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(mods))),
	)
	defer span.End()

	var errs []error
	for i := len(mods) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("stop cancelled: %w", err))
			break
		}
		mod := mods[i]
		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("StopModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
		if err := mod.Stop(modCtx); err != nil {
			modSpan.RecordError(err)
			m.loggerCtx.Error("failed to stop module", slog.String("module", mod.Name()), slog.Any("error", err))
			errs = append(errs, fmt.Errorf("failed to stop module %q: %w", mod.Name(), err))
		}
		modSpan.End()
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

		defer func() {
			path = path[:len(path)-1]
			state[name] = visited
		}()

		for _, depName := range mod.DependsOn() {
			depName = strings.TrimSpace(depName)
			if depName == "" {
				return fmt.Errorf("%w: module %q has an empty dependency name", ErrInvalidDependencyName, name)
			}
			if depName == name {
				return fmt.Errorf("%w: %q", ErrSelfDependency, name)
			}
			if err := visit(depName); err != nil {
				return err
			}
		}

		order = append(order, mod)
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

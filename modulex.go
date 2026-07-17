package modulex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	// ErrAlreadyInitialized is returned when InitModules is called more than once.
	ErrAlreadyInitialized = errors.New("manager already initialized")
)

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
	services     map[string]interface{}
	modules      map[string]Module
	orderedMods  []Module
	router       gochi.Router
	eventBus     EventBus
	loggerCtx    *slog.Logger
	configLoader func(target interface{}) error
	initialized  bool
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
		router:       router,
		eventBus:     eb,
		loggerCtx:    logger,
		configLoader: configLoader,
		tracer:       otel.Tracer("github.com/mediusfy/modulex"),
	}
}

// RegisterModule registers a feature module in the manager.
// Modules should be registered before calling InitModules.
func (m *Manager) RegisterModule(mod Module) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modules[mod.Name()] = mod
}

// RegisterService implements Registry. It registers a service instance to the service locator.
// Registration is only permitted before InitModules has completed.
func (m *Manager) RegisterService(name string, svc interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.initialized {
		return ErrRegistryLocked
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
func (m *Manager) InitModules(ctx context.Context) error {
	m.mu.Lock()
	if m.initialized {
		m.mu.Unlock()
		return ErrAlreadyInitialized
	}

	ordered, err := m.sortModules()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.orderedMods = ordered
	m.mu.Unlock()

	ctx, span := m.tracer.Start(ctx, "InitModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(ordered))),
	)
	defer span.End()

	for _, mod := range ordered {
		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("InitModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
		if err := mod.Init(modCtx, m); err != nil {
			modSpan.RecordError(err)
			modSpan.End()
			span.RecordError(err)
			return fmt.Errorf("failed to init module %q: %w", mod.Name(), err)
		}
		modSpan.End()
	}

	m.mu.Lock()
	m.initialized = true
	m.mu.Unlock()
	return nil
}

// StartModules starts all registered modules in topological dependency order inside trace spans.
func (m *Manager) StartModules(ctx context.Context) error {
	m.mu.RLock()
	mods := make([]Module, len(m.orderedMods))
	copy(mods, m.orderedMods)
	m.mu.RUnlock()

	ctx, span := m.tracer.Start(ctx, "StartModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(mods))),
	)
	defer span.End()

	for _, mod := range mods {
		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("StartModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
		if err := mod.Start(modCtx); err != nil {
			modSpan.RecordError(err)
			modSpan.End()
			span.RecordError(err)
			return fmt.Errorf("failed to start module %q: %w", mod.Name(), err)
		}
		modSpan.End()
	}
	return nil
}

// StopModules stops all registered modules in reverse topological order inside trace spans and closes the EventBus.
func (m *Manager) StopModules(ctx context.Context) error {
	m.mu.Lock()
	eb := m.eventBus
	m.mu.Unlock()

	// Close the EventBus if configured to unsubscribe all drivers cleanly
	if eb != nil {
		m.loggerCtx.Info("closing event bus")
		_ = eb.Close(ctx)
	}

	m.mu.RLock()
	mods := make([]Module, len(m.orderedMods))
	copy(mods, m.orderedMods)
	m.mu.RUnlock()

	ctx, span := m.tracer.Start(ctx, "StopModules",
		trace.WithAttributes(attribute.Int("modulex.module_count", len(mods))),
	)
	defer span.End()

	var firstErr error
	for i := len(mods) - 1; i >= 0; i-- {
		mod := mods[i]
		modCtx, modSpan := m.tracer.Start(ctx, fmt.Sprintf("StopModule:%s", mod.Name()),
			trace.WithAttributes(attribute.String("modulex.module_name", mod.Name())),
		)
		if err := mod.Stop(modCtx); err != nil {
			modSpan.RecordError(err)
			m.loggerCtx.Error("failed to stop module", slog.String("module", mod.Name()), slog.Any("error", err))
			if firstErr == nil {
				firstErr = err
			}
		}
		modSpan.End()
	}

	if firstErr != nil {
		span.RecordError(firstErr)
	}
	return firstErr
}

// sortModules performs a topological sort on registered modules.
// It returns an ordered slice of modules or an error if a circular dependency is detected.
func (m *Manager) sortModules() ([]Module, error) {
	visited := make(map[string]int) // 0 = unvisited, 1 = visiting, 2 = visited
	var order []Module

	var visit func(name string) error
	visit = func(name string) error {
		state := visited[name]
		if state == 1 {
			return fmt.Errorf("%w: cycle involves module %q", ErrCircularDependency, name)
		}
		if state == 2 {
			return nil
		}

		visited[name] = 1

		mod, exists := m.modules[name]
		if !exists {
			return fmt.Errorf("module %q dependency not found", name)
		}

		for _, depName := range mod.DependsOn() {
			if err := visit(depName); err != nil {
				return err
			}
		}

		visited[name] = 2
		order = append(order, mod)
		return nil
	}

	for name := range m.modules {
		if visited[name] == 0 {
			if err := visit(name); err != nil {
				return nil, err
			}
		}
	}

	return order, nil
}

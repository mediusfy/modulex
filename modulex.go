package modulex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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

	// ErrInvalidHealthCheckName is returned when a health check name is empty.
	ErrInvalidHealthCheckName = errors.New("health check name must not be empty")

	// ErrInvalidReadinessCheckName is returned when a readiness check name is empty.
	ErrInvalidReadinessCheckName = errors.New("readiness check name must not be empty")

	// ErrHealthCheckNil is returned when RegisterHealthCheck is given a nil
	// check function. A registered nil check would panic if any caller
	// invoked it directly rather than defensively nil-checking first (as
	// the httpx package does); rejecting it at registration means a nil
	// check function can never reach that map in the first place.
	ErrHealthCheckNil = errors.New("health check function must not be nil")

	// ErrReadinessCheckNil is returned when RegisterReadinessCheck is given
	// a nil check function. See ErrHealthCheckNil for why this is rejected
	// at registration rather than left to each caller to guard against.
	ErrReadinessCheckNil = errors.New("readiness check function must not be nil")
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

func (h *TaskHandle) Name() string { return h.name }

// Wait blocks until the task finishes and returns its final error.
func (h *TaskHandle) Wait() error {
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// Done reports whether the task has finished, without blocking. It is safe to
// call concurrently with Wait and from diagnostics code that must not block
// on a long-running task.
func (h *TaskHandle) Done() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// Err returns the task's final error. It only reflects a meaningful value
// once Done reports true; a task that is still running always reports a nil
// error here, regardless of what it eventually returns.
func (h *TaskHandle) Err() error {
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
	IsValid() bool
	TraceID() string
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

type noopSpanContext struct{}

func (noopSpanContext) IsValid() bool   { return false }
func (noopSpanContext) TraceID() string { return "" }
func (noopSpanContext) SpanID() string  { return "" }

type noopSpan struct{}

// End is intentionally empty; a no-op span has nothing to finalize.
func (noopSpan) End() {}

// RecordError is intentionally empty; a no-op span discards recorded errors.
func (noopSpan) RecordError(err error) {}

// SetAttributes is intentionally empty; a no-op span discards attributes.
func (noopSpan) SetAttributes(attrs map[string]any) {}

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

// WithEventBus configures the pluggable event bus. If nil, a no-op event bus
// is used so event publishing remains optional.
func WithEventBus(eb EventBus) ManagerOption {
	return func(m *Manager) {
		m.eventBus = eb
	}
}

// WithLogger configures the logger used by the manager and its modules. If
// nil, slog.Default() is used.
func WithLogger(logger *slog.Logger) ManagerOption {
	return func(m *Manager) {
		m.loggerCtx = logger
	}
}

// WithPanicPolicy sets the panic policy for supervised background tasks.
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

// EventHandler processes an event delivered by an EventBus. The meaning of a
// returned error depends on the concrete adapter: some adapters ack/nack based
// on the error, others only log it. Callers coding against the EventBus
// abstraction should not assume specific retry or redelivery semantics.
type EventHandler func(ctx context.Context, payload []byte) error

// EventBus abstracts the underlying message broker (NATS, Kafka, RabbitMQ, etc.).
type EventBus interface {
	// Publish sends a payload to a specific topic/subject.
	Publish(ctx context.Context, topic string, payload []byte) (err error)

	// Subscribe listens to a topic and invokes the handler when an event is
	// received. The adapter determines how handler errors affect
	// acknowledgment, retry, and redelivery; see the adapter documentation for
	// its policy.
	Subscribe(ctx context.Context, topic string, handler EventHandler) (err error)

	// Close gracefully disconnects from the broker, shutting down active subscribers.
	Close(ctx context.Context) (err error)
}

// Publisher is the narrow capability of publishing a payload to a topic. It
// makes no promise about delivery durability beyond what the concrete
// adapter documents in its own doc comments: a Publisher may be backed by an
// at-most-once fire-and-forget transport (core NATS) or an acknowledged,
// durable one (JetStream) — the interface itself does not distinguish them.
//
// Every EventBus implementation already satisfies Publisher for free, since
// Go interfaces are structural and EventBus.Publish has this exact
// signature. Publisher exists so code that only needs to publish can depend
// on the narrower capability instead of the full EventBus.
type Publisher interface {
	Publish(ctx context.Context, topic string, payload []byte) (err error)
}

// Subscriber is the narrow capability of registering a fire-and-forget
// handler for a topic. Subscriber makes NO durability guarantee: whether a
// delivered message is retried, redelivered, or silently dropped on handler
// error is entirely adapter-defined (see each adapter's Subscribe doc
// comment for its specific policy). A caller that needs at-least-once
// delivery, explicit acknowledgement, replay, or dead-letter semantics must
// use DurableConsumer instead — Subscriber alone never implies any of that.
//
// Every EventBus implementation already satisfies Subscriber for free, since
// Go interfaces are structural and EventBus.Subscribe has this exact
// signature.
type Subscriber interface {
	Subscribe(ctx context.Context, topic string, handler EventHandler) (err error)
}

// AckDecision is the disposition a DurableHandler assigns to a message it
// was given, replacing the bare "error or nil" signal EventHandler uses.
// EventHandler's plain error return cannot distinguish "please retry this
// message" from "give up on this message without retrying" from "this
// message can never succeed, route it to a dead letter" — a durable
// consumer with real ack/nack/dead-letter semantics needs to express all
// three, so DurableHandler returns AckDecision instead of error.
type AckDecision int

const (
	// Ack acknowledges the message as successfully processed. A conforming
	// DurableConsumer will not redeliver it.
	Ack AckDecision = iota

	// Nack indicates processing failed but should be retried. A conforming
	// DurableConsumer redelivers the message, subject to whatever
	// retry/backoff/max-attempts policy it documents.
	Nack

	// DeadLetter indicates processing failed terminally: the message must
	// not be redelivered again. A conforming DurableConsumer routes it to
	// whatever dead-letter mechanism it documents (a separate subject or
	// stream, a broker-native DLQ, or simply marking it permanently failed)
	// instead of retrying it.
	DeadLetter
)

// String returns a lower_snake_case name for d, or "unknown" for an
// out-of-range value.
func (d AckDecision) String() string {
	switch d {
	case Ack:
		return "ack"
	case Nack:
		return "nack"
	case DeadLetter:
		return "dead_letter"
	default:
		return "unknown"
	}
}

// DurableMessage carries the payload and delivery metadata for one message
// given to a DurableHandler.
type DurableMessage struct {
	// Payload is the message body.
	Payload []byte

	// Redelivered reports whether this delivery attempt is a retry of a
	// message previously delivered (to this consumer or an earlier attempt
	// by the same durable consumer identity).
	Redelivered bool

	// DeliveryCount is the number of times this message has been delivered
	// to this durable consumer identity, starting at 1 for the first
	// delivery. An adapter that cannot track delivery count reports 0.
	DeliveryCount int
}

// DurableHandler processes one message delivered by a DurableConsumer and
// returns the AckDecision it should receive. See AckDecision for why this
// differs from EventHandler's bare error return.
type DurableHandler func(ctx context.Context, msg DurableMessage) (decision AckDecision)

// ReplayPolicy selects where a brand-new DurableConsumer subscription
// starts reading from a topic. It only affects the first time a given
// ConsumerName (see DurableSubscribeOptions) is used; a durable consumer
// identity with prior acknowledged progress resumes from that position
// instead of replaying, regardless of ReplayPolicy.
type ReplayPolicy int

const (
	// ReplayAll starts from the oldest message the adapter has retained.
	ReplayAll ReplayPolicy = iota

	// ReplayNew delivers only messages published after the subscription is
	// established; nothing previously retained is replayed.
	ReplayNew
)

// String returns a lower_snake_case name for p, or "unknown" for an
// out-of-range value.
func (p ReplayPolicy) String() string {
	switch p {
	case ReplayAll:
		return "replay_all"
	case ReplayNew:
		return "replay_new"
	default:
		return "unknown"
	}
}

// DurableSubscribeOptions configures a DurableConsumer subscription. Use the
// With* option functions below to set fields; the zero value is not a valid
// configuration (ConsumerName is required).
type DurableSubscribeOptions struct {
	// ConsumerName identifies the durable consumer/consumer-group identity
	// (see the "Consumer identity" semantic on DurableConsumer). Required;
	// an adapter rejects an empty ConsumerName.
	ConsumerName string

	// Replay selects where a brand-new ConsumerName starts reading from.
	// See ReplayPolicy.
	Replay ReplayPolicy
}

// DurableSubscribeOption configures a DurableSubscribeOptions value.
type DurableSubscribeOption func(*DurableSubscribeOptions)

// WithConsumerName sets the durable consumer/consumer-group identity for a
// DurableConsumer subscription. See the "Consumer identity" semantic on
// DurableConsumer.
func WithConsumerName(name string) DurableSubscribeOption {
	return func(o *DurableSubscribeOptions) {
		o.ConsumerName = name
	}
}

// WithReplayPolicy sets where a brand-new consumer identity starts reading
// from. See ReplayPolicy and the "Replay" semantic on DurableConsumer.
func WithReplayPolicy(p ReplayPolicy) DurableSubscribeOption {
	return func(o *DurableSubscribeOptions) {
		o.Replay = p
	}
}

// DurableConsumer is the capability of consuming a topic with the stronger
// guarantees Subscriber deliberately does not promise: explicit
// acknowledgement, redelivery, replay, consumer identity, and dead-letter
// routing. Not every EventBus adapter implements DurableConsumer — callers
// that need these guarantees should type-assert for it rather than assuming
// any EventBus or Subscriber provides them.
//
// DurableConsumer deliberately does not embed Subscriber. A durable handler
// needs to express more than "error or nil" (see AckDecision), so it uses
// the distinct DurableHandler signature rather than EventHandler; the two
// therefore cannot share one Subscribe method identity. An adapter is free
// to implement both Subscriber and DurableConsumer (e.g. by also embedding
// a plain EventBus), but the capabilities are independent and are checked
// independently via type assertion.
//
// DurableConsumer documents the six semantics named by MOD-54, three as the
// SubscribeDurable method contract and three as documented properties a
// correct implementation must uphold:
//
//   - Acknowledgement (method contract): DurableHandler returns an
//     AckDecision — Ack, Nack, or DeadLetter — for every message it is
//     given. The adapter is responsible for translating that decision into
//     its broker's native ack mechanism.
//   - Consumer identity (method contract, via DurableSubscribeOptions):
//     ConsumerName names a durable consumer/consumer-group. Reusing the same
//     ConsumerName resumes from its last acknowledged position rather than
//     starting over, including across process restarts. Multiple concurrent
//     subscriptions sharing one ConsumerName load-balance messages across
//     them (a competing-consumers group) rather than each receiving every
//     message.
//   - Replay (method contract, via DurableSubscribeOptions): ReplayPolicy
//     selects where a brand-new ConsumerName starts reading from — from the
//     oldest retained message (ReplayAll) or only new ones (ReplayNew).
//   - Retry (documented property): on Nack, the adapter redelivers the
//     message subject to its own documented retry/backoff/max-attempts
//     policy. This is adapter-defined rather than a method because retry
//     configuration (backoff curves, max attempts, poison-message
//     thresholds) varies too much across brokers to usefully standardize at
//     this interface's level; see the implementing adapter's doc comment for
//     its specific policy.
//   - Ordering (documented property): within a single SubscribeDurable call
//     (one ConsumerName, one subscription), an implementation must process
//     and resolve (ack/nack/dead-letter) messages in the order the broker
//     delivered them before fetching the next one, so relative order is
//     preserved for that subscription. Ordering across multiple concurrent
//     subscriptions sharing one ConsumerName (a competing-consumers group)
//     is NOT guaranteed, since the broker may deliver to whichever
//     subscription is next available.
//   - Dead-letter (documented property): on DeadLetter, the adapter must
//     never redeliver the message again through the normal retry path. How
//     it is routed instead (a separate subject/stream, a broker-native DLQ,
//     or simply discarded after being marked permanently failed) is
//     adapter-defined; see the implementing adapter's doc comment.
type DurableConsumer interface {
	// SubscribeDurable registers handler as the durable consumer for topic
	// under the identity and options given. opts must include
	// WithConsumerName; an adapter rejects a call without one.
	SubscribeDurable(ctx context.Context, topic string, handler DurableHandler, opts ...DurableSubscribeOption) (err error)
}

// Module represents a self-contained feature module that complies with Hexagonal Architecture.
// It acts as the composition root of the feature, instantiating services and adapters,
// and wiring them through the central registry.
//
// Start and Stop are optional lifecycle capabilities. A module that needs to run
// background work during startup implements Starter; a module that owns resources
// that must be released implements Stopper. The manager skips modules that do not
// implement these interfaces, so simple modules do not require no-op methods.
type Module interface {
	// Name returns the unique, kebab-case name of the feature module.
	Name() (name string)

	// DependsOn returns the names of other modules that this module depends on.
	// The Manager uses this list to sort the modules topologically before initialization.
	DependsOn() (deps []string)

	// Init initializes the module with the registry.
	// This is where modules register their services, register routes, and resolve dependencies.
	Init(ctx context.Context, reg Registry) (err error)
}

// Starter is an optional lifecycle capability for modules that begin background
// work or listeners during startup. The manager calls Start after all modules have
// been initialized successfully.
type Starter interface {
	Start(ctx context.Context) (err error)
}

// Stopper is an optional lifecycle capability for modules that release resources
// during shutdown. The manager calls Stop in reverse topological order when
// stopping the application or rolling back a failed init/start.
type Stopper interface {
	Stop(ctx context.Context) (err error)
}

// ServiceRegisterer is the capability to register a service instance under a
// unique name. Registrations are only permitted before the registry has finished
// initialization.
type ServiceRegisterer interface {
	// RegisterService registers a service implementation under a unique key (e.g. "incidents.Service").
	// Returns ErrRegistryLocked if the registry has already finished initialization.
	RegisterService(name string, svc interface{}) (err error)
}

// ServiceResolver is the capability to resolve a previously registered service
// by name.
type ServiceResolver interface {
	// ResolveService resolves a registered service implementation by name.
	// If the service is not found, it returns ErrServiceNotFound.
	ResolveService(name string) (svc interface{}, err error)
}

// ServiceRegistry combines service registration and resolution.
type ServiceRegistry interface {
	ServiceRegisterer
	ServiceResolver
}

// EventBusProvider is the capability to access the pluggable event bus.
type EventBusProvider interface {
	EventBus() (eb EventBus)
}

// ConfigProvider is the capability to unmarshal configuration values into a
// target structure.
type ConfigProvider interface {
	// GetConfig unmarshals configuration values into the target structure.
	// This abstract config retrieval prevents features from directly reading global configurations.
	GetConfig(target interface{}) (err error)
}

// LoggerProvider is the capability to access the system logger.
type LoggerProvider interface {
	Logger() (logger *slog.Logger)
}

// HealthCheckRegisterer is the capability to register a module health (liveness)
// check.
//
// A health check answers "is this process functioning correctly?" A failing
// health check means the process is broken and should be restarted (e.g. by
// an orchestrator's liveness probe). Contrast this with ReadinessRegisterer,
// which answers "should this process currently receive traffic?" — a failing
// readiness check means the instance should be pulled from load balancing,
// not restarted.
type HealthCheckRegisterer interface {
	// RegisterHealthCheck registers a health (liveness) check function under a
	// unique name.
	RegisterHealthCheck(name string, check func(context.Context) error) (err error)
}

// HealthCheckProvider exposes the registered health (liveness) checks.
type HealthCheckProvider interface {
	HealthChecks() (checks map[string]func(context.Context) error)
}

// ReadinessRegisterer is the capability to register a module readiness check.
//
// A readiness check answers "should this process currently receive traffic?"
// A failing readiness check means the instance is temporarily unable to serve
// requests (e.g. its database pool isn't warm yet, a dependency is
// unreachable, a cache hasn't primed) and should be pulled from the load
// balancer — the process itself is otherwise healthy and should not be
// restarted. Contrast this with HealthCheckRegisterer, whose checks answer "is
// this process functioning correctly?" and whose failures indicate the
// process should be restarted.
//
// The consumer defines what "ready" means for their service by registering
// named check functions; Modulex only abstracts registration, aggregation,
// and HTTP exposure (see modulex/httpx).
type ReadinessRegisterer interface {
	// RegisterReadinessCheck registers a readiness check function under a
	// unique name.
	RegisterReadinessCheck(name string, check func(context.Context) error) (err error)
}

// ReadinessProvider exposes the registered readiness checks.
type ReadinessProvider interface {
	ReadinessChecks() (checks map[string]func(context.Context) error)
}

// TaskSpawner is the capability to start a supervised background task. Tasks
// receive a lifecycle-owned context and are cancelled during shutdown.
type TaskSpawner interface {
	// Go spawns a supervised goroutine to execute background work. It creates a
	// child span when a Tracer is configured, recovers from panics according to
	// the manager's panic policy, and returns a handle that can be used to wait
	// for the task to finish. Tasks are cancelled and awaited during manager
	// shutdown. Returns ErrRegistryLocked if the manager is stopping or stopped,
	// or ErrDuplicateTask if a task with the same name already exists.
	Go(ctx context.Context, taskName string, fn func(ctx context.Context) error) (handle *TaskHandle, err error)
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
	HealthCheckRegisterer
	HealthCheckProvider
	ReadinessRegisterer
	ReadinessProvider
}

// Manager implements the Registry interface and orchestrates the module lifecycles.
type Manager struct {
	mu           sync.RWMutex
	stateMu      sync.Mutex
	taskMu       sync.Mutex
	services     map[string]interface{}
	modules      map[string]Module
	moduleOrder  []string
	orderedMods  []Module
	eventBus     EventBus
	loggerCtx    *slog.Logger
	configLoader func(target interface{}) error
	state        LifecycleState
	tracer       Tracer
	panicPolicy  PanicPolicy
	tasks        map[string]*TaskHandle
	taskErrs     []error
	// taskCtx/taskCancel are not request-scoped data threaded through a call
	// chain; together they are the current cancellation "generation" shared by
	// every supervised task started via Go, protected by taskMu, and swapped
	// out wholesale by resetTaskCtx after a shutdown. That mutable, lockable
	// pair is intentionally kept as Manager state rather than passed as
	// parameters, which is why it remains a struct field.
	taskCtx         context.Context
	taskCancel      context.CancelFunc
	healthMu        sync.RWMutex
	healthChecks    map[string]func(context.Context) error
	readinessMu     sync.RWMutex
	readinessChecks map[string]func(context.Context) error
	// timingsMu protects the lifecycle timing fields below, following the
	// existing field-per-concern locking pattern (healthMu, readinessMu).
	timingsMu    sync.RWMutex
	initTotal    time.Duration
	startTotal   time.Duration
	initTimings  map[string]time.Duration
	startTimings map[string]time.Duration
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

// RegisterHealthCheck registers a health (liveness) check function under a
// unique name. check must not be nil.
func (m *Manager) RegisterHealthCheck(name string, check func(context.Context) error) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidHealthCheckName
	}
	if check == nil {
		return ErrHealthCheckNil
	}

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
// name. check must not be nil.
func (m *Manager) RegisterReadinessCheck(name string, check func(context.Context) error) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidReadinessCheckName
	}
	if check == nil {
		return ErrReadinessCheckNil
	}

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

// RegisterModule registers a feature module in the manager.
// Modules should be registered before calling InitModules.
//
// Registration is rejected if the module is nil, its name is empty, another
// module with the same name is already registered, or initialization has
// already started. Independent modules preserve their registration order as
// the deterministic tie-break during topological sorting.
func (m *Manager) RegisterModule(mod Module) error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.state != StateConfiguring {
		return fmt.Errorf("%w: cannot register module while in %q state", ErrRegistryLocked, m.state)
	}

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
	defer m.stateMu.Unlock()
	if m.state != StateConfiguring && m.state != StateInitializing {
		return fmt.Errorf("%w: cannot register service while in %q state", ErrRegistryLocked, m.state)
	}

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
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrInvalidServiceName
	}

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
func (m *Manager) Logger() *slog.Logger {
	return m.loggerCtx
}

func (m *Manager) startSpan(ctx context.Context, spanName string, attrs map[string]any) (context.Context, Span) {
	parentSC := m.tracer.SpanContextFromContext(ctx)
	childCtx, span := m.tracer.Start(ctx, spanName, attrs)
	childSC := m.tracer.SpanContextFromContext(childCtx)

	if childSC.IsValid() {
		if m.loggerCtx.Enabled(childCtx, slog.LevelDebug) {
			m.loggerCtx.DebugContext(childCtx, "span started",
				slog.String("span_name", spanName),
				slog.String("trace_id", childSC.TraceID()),
				slog.String("span_id", childSC.SpanID()),
				slog.String("parent_span_id", parentSC.SpanID()),
			)
		}
	}
	return childCtx, span
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

	// taskCancel is deliberately NOT deferred here: its lifetime is the spawned
	// task goroutine below, not this function. Go returns immediately after
	// spawning the goroutine, so a defer at this scope would cancel taskCtx
	// (and thus the task) before or as it starts running. taskCancel is called
	// from the goroutine's own completion defer once fn has returned.
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

// mermaidSafeIDPattern matches a module name that is already safe to use
// verbatim as both a Mermaid node ID and an unquoted label — this is every
// name RegisterModule accepts in practice (module names are conventionally
// kebab-case or similar). Any name outside this pattern is rendered via a
// synthetic ID plus a quoted, escaped label instead (see mermaidNodeID and
// mermaidEscapeLabel), so ExportDAG's output for every already-well-formed
// module name this repository (or any known consumer) has ever used is
// byte-identical to before this safety check was added.
var mermaidSafeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// mermaidNodeIDs assigns every distinct name in names a Mermaid node
// identifier, in a single pass so it can safely be called once with the
// union of every registered module's own name and every name any module's
// DependsOn references — including a dangling dependency name that does
// not correspond to a registered module. A name matching
// mermaidSafeIDPattern keeps itself as its ID; any other name gets a
// synthetic "n<counter>" identifier from a single shared, monotonically
// increasing counter, so two different unsafe names (whether both are
// registered modules, both are dangling dependency references, or one of
// each) can never collide on the same synthetic ID — unlike assigning
// synthetic IDs from each name's own, independently-scoped position (e.g.
// a sorted-module-list index for registered names and a constant sentinel
// for dangling ones), which would let two unsafe dangling names collide on
// one shared ID. A module name is arbitrary, caller-supplied text —
// RegisterModule only requires it to be non-empty — so a name containing
// Mermaid syntax (e.g. "]", "-->", a newline, or an HTML-like sequence)
// must never be used as a raw node ID: doing so can produce malformed or
// misleading diagrams, or, if the resulting Mermaid source is ever
// rendered in a context that permits inline HTML/script in labels, worse.
func mermaidNodeIDs(names []string) map[string]string {
	ids := make(map[string]string, len(names))
	counter := 0
	for _, name := range names {
		if _, exists := ids[name]; exists {
			continue
		}
		if mermaidSafeIDPattern.MatchString(name) {
			ids[name] = name
			continue
		}
		ids[name] = fmt.Sprintf("n%d", counter)
		counter++
	}
	return ids
}

// mermaidEscapeLabel escapes name for safe use inside a double-quoted
// Mermaid label. Mermaid decodes "#quot;"/"#lt;"/"#gt;"-style numeric/named
// character references inside quoted label text, so using them here embeds
// the literal characters as visible text rather than as Mermaid or
// (in a permissive rendering configuration) HTML/script syntax. A literal
// newline is replaced with a space, since Mermaid's grammar is line-based
// and an embedded newline would otherwise split the label across two lines
// of source, corrupting the diagram.
func mermaidEscapeLabel(name string) string {
	replacer := strings.NewReplacer(
		`"`, "#quot;",
		"<", "#lt;",
		">", "#gt;",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(name)
}

// mermaidNode renders one module name as a Mermaid flowchart node
// declaration: "id[label]" for an already-safe name (byte-identical to
// ExportDAG's behavior before Mermaid-safety escaping was added), or
// "id[\"escaped label\"]" for a name containing Mermaid-significant
// characters.
func mermaidNode(id, name string) string {
	if mermaidSafeIDPattern.MatchString(name) {
		return fmt.Sprintf("%s[%s]", id, name)
	}
	return fmt.Sprintf("%s[%q]", id, mermaidEscapeLabel(name))
}

// ExportDAG returns a Mermaid-compatible DAG visualization of the registered modules.
func (m *Manager) ExportDAG() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Strings(names)

	// allDeps collects every dependency name (registered or dangling) each
	// module declares, per module, in the same sorted order rendered below
	// — computed once here so mermaidNodeIDs can assign IDs for the full
	// set of names this call will ever reference (registered modules AND
	// any dangling dependency names) before any node/edge line is written.
	allDeps := make(map[string][]string, len(names))
	idInputs := append([]string(nil), names...)
	for _, name := range names {
		deps := append([]string(nil), m.modules[name].DependsOn()...)
		sort.Strings(deps)
		var trimmed []string
		for _, dep := range deps {
			depName := strings.TrimSpace(dep)
			if depName == "" {
				continue
			}
			trimmed = append(trimmed, depName)
			idInputs = append(idInputs, depName)
		}
		allDeps[name] = trimmed
	}
	ids := mermaidNodeIDs(idInputs)

	var sb strings.Builder
	sb.WriteString("graph TD\n")
	for _, name := range names {
		fmt.Fprintf(&sb, "    %s\n", mermaidNode(ids[name], name))
		for _, depName := range allDeps[name] {
			fmt.Fprintf(&sb, "    %s --> %s\n", ids[name], ids[depName])
		}
	}
	return sb.String()
}

// recordInitTiming stores the total InitModules duration and the per-module
// init durations captured by runPhase, replacing any previous values. It is
// safe to call after a partial failure: whatever modules were attempted
// before the error still have their durations recorded.
func (m *Manager) recordInitTiming(total time.Duration, perModule map[string]time.Duration) {
	m.timingsMu.Lock()
	defer m.timingsMu.Unlock()
	m.initTotal = total
	m.initTimings = perModule
}

// recordStartTiming stores the total StartModules duration and the
// per-module start durations captured by runPhase, replacing any previous
// values.
func (m *Manager) recordStartTiming(total time.Duration, perModule map[string]time.Duration) {
	m.timingsMu.Lock()
	defer m.timingsMu.Unlock()
	m.startTotal = total
	m.startTimings = perModule
}

// lifecycleTimings returns a snapshot of the recorded lifecycle timings.
func (m *Manager) lifecycleTimings() LifecycleTimings {
	m.timingsMu.RLock()
	defer m.timingsMu.RUnlock()
	return LifecycleTimings{
		InitModules:  m.initTotal,
		StartModules: m.startTotal,
		ModuleInit:   sortedModuleTimings(m.initTimings),
		ModuleStart:  sortedModuleTimings(m.startTimings),
	}
}

// sortedModuleTimings converts a name -> duration map into a slice sorted by
// module name, so that lifecycle timings marshal deterministically like
// every other diagnostics field. It returns nil for an empty or nil input so
// the field is omitted from JSON via omitempty rather than rendered as an
// empty array before any phase has run.
func sortedModuleTimings(in map[string]time.Duration) []ModuleTiming {
	if len(in) == 0 {
		return nil
	}
	out := make([]ModuleTiming, 0, len(in))
	for name, d := range in {
		out = append(out, ModuleTiming{Name: name, DurationNs: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// runPhase executes a lifecycle phase (e.g. InitModules or StartModules)
// over ordered modules. It returns the number of modules successfully
// processed, a map of per-module wall-clock durations for the modules that
// were attempted (keyed by module name, populated regardless of whether the
// phase ultimately succeeded), and the first error encountered. phase names
// the parent span and appears in the cancellation message; moduleSpanPrefix
// names each per-module span; action appears in error messages ("init",
// "start", etc.).
func (m *Manager) runPhase(
	ctx context.Context,
	phase string,
	moduleSpanPrefix string,
	action string,
	ordered []Module,
	runFn func(context.Context, Module) error,
) (int, map[string]time.Duration, error) {
	ctx, span := m.startSpan(ctx, phase, map[string]any{
		"modulex.module_count": len(ordered),
	})
	defer span.End()

	timings := make(map[string]time.Duration, len(ordered))
	count := 0
	for _, mod := range ordered {
		if err := ctx.Err(); err != nil {
			return count, timings, fmt.Errorf("%s cancelled: %w", action, err)
		}

		modCtx, modSpan := m.startSpan(ctx, fmt.Sprintf("%s:%s", moduleSpanPrefix, mod.Name()), map[string]any{
			"modulex.module_name": mod.Name(),
		})
		modStart := time.Now()
		err := runFn(modCtx, mod)
		timings[mod.Name()] = time.Since(modStart)
		if err != nil {
			modSpan.RecordError(err)
			modSpan.End()
			span.RecordError(err)
			return count, timings, fmt.Errorf("failed to %s module %q: %w", action, mod.Name(), err)
		}
		modSpan.End()
		count++
	}
	return count, timings, nil
}

// InitModules sorts the modules topologically based on dependencies,
// then initializes them sequentially in dependency order inside trace spans.
//
// If a module fails to initialize, all previously initialized modules are
// stopped in reverse order and the manager moves to the stopped state.
func (m *Manager) InitModules(ctx context.Context) error {
	state, err := m.transitionToInitializing()
	if err != nil {
		return fmt.Errorf("%w: InitModules called in %q state", ErrInvalidLifecycleState, state)
	}

	ordered, err := m.lockSortedModules()
	if err != nil {
		return m.finalizeInitFailure(ctx, err, nil)
	}

	phaseStart := time.Now()
	initializedCount, moduleTimings, initErr := m.runPhase(ctx, "InitModules", "InitModule", "init", ordered, func(ctx context.Context, mod Module) error {
		return mod.Init(ctx, m)
	})
	m.recordInitTiming(time.Since(phaseStart), moduleTimings)
	if initErr != nil {
		return m.finalizeInitFailure(ctx, initErr, ordered[:initializedCount])
	}

	m.setState(StateInitialized)
	return nil
}

func (m *Manager) transitionToInitializing() (LifecycleState, error) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if m.state != StateConfiguring {
		return m.state, fmt.Errorf("transition requested in %q state", m.state)
	}
	m.state = StateInitializing
	return StateInitializing, nil
}

func (m *Manager) lockSortedModules() ([]Module, error) {
	m.mu.Lock()
	ordered, err := m.sortModules()
	if err != nil {
		m.orderedMods = nil
		m.mu.Unlock()
		return nil, err
	}
	m.orderedMods = ordered
	m.mu.Unlock()
	return ordered, nil
}

func (m *Manager) finalizeInitFailure(ctx context.Context, err error, initialized []Module) error {
	return m.finalizeFailure(ctx, err,
		func(ctx context.Context) error { return m.rollback(ctx, initialized, "init rollback") },
		func() {
			m.mu.Lock()
			m.orderedMods = nil
			m.mu.Unlock()
		},
	)
}

// finalizeFailure joins shutdown-related errors, rolls back the phase, runs
// an optional cleanup hook, closes the event bus, and transitions the manager
// to the stopped state.
func (m *Manager) finalizeFailure(ctx context.Context, err error, rollback func(context.Context) error, cleanup func()) error {
	if waitErr := m.waitForTasks(ctx); waitErr != nil {
		err = errors.Join(err, waitErr)
	}
	if rollbackErr := rollback(ctx); rollbackErr != nil {
		err = errors.Join(err, rollbackErr)
	}
	if closeErr := m.closeEventBus(ctx); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if cleanup != nil {
		cleanup()
	}
	m.setState(StateStopped)
	return err
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

	phaseStart := time.Now()
	startedCount, moduleTimings, startErr := m.runPhase(ctx, "StartModules", "StartModule", "start", mods, m.startModule)
	m.recordStartTiming(time.Since(phaseStart), moduleTimings)
	if startErr != nil {
		return m.finalizeFailure(ctx, startErr,
			func(ctx context.Context) error { return m.rollback(ctx, mods[:startedCount], "start rollback") },
			nil,
		)
	}

	m.setState(StateRunning)
	return nil
}

// rollback stops modules in reverse order during failure recovery. Only
// modules that implement Stopper are stopped. Errors from individual stops
// are joined together.
func (m *Manager) rollback(ctx context.Context, mods []Module, phase string) error {
	var errs []error
	for i := len(mods) - 1; i >= 0; i-- {
		mod := mods[i]
		if err := m.stopModule(ctx, mod, phase); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// startModule invokes mod.Start if the module implements Starter. Modules that
// do not implement Starter are skipped without error.
func (m *Manager) startModule(ctx context.Context, mod Module) error {
	if s, ok := mod.(Starter); ok {
		return s.Start(ctx)
	}
	return nil
}

// stopModule invokes mod.Stop if the module implements Stopper. Modules that do
// not implement Stopper are skipped without error. The phase string is used for
// error messages.
func (m *Manager) stopModule(ctx context.Context, mod Module, phase string) error {
	if s, ok := mod.(Stopper); ok {
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
//
// StopModules returns ErrInvalidLifecycleState if called while InitModules or
// StartModules is concurrently in progress on another goroutine (i.e. the
// manager is in StateInitializing or StateStarting). Those phases iterate
// modules without holding the manager's state lock, so racing a concurrent
// StopModules against them cannot be done safely: it would let StopModules
// tear down tasks and the event bus while a module's Init/Start is still
// running, and could have the in-progress phase overwrite StateStopped with
// StateInitialized/StateRunning once it completes, silently breaking the
// idempotency guarantee above. To cancel an in-flight InitModules or
// StartModules call, cancel the context passed to it instead; call
// StopModules once it returns.
func (m *Manager) StopModules(ctx context.Context) error {
	m.stateMu.Lock()
	state := m.state
	switch state {
	case StateStopped, StateStopping:
		m.stateMu.Unlock()
		return nil
	case StateConfiguring, StateInitialized, StateRunning:
		// proceed with shutdown
	case StateInitializing, StateStarting:
		m.stateMu.Unlock()
		return fmt.Errorf("%w: StopModules called while InitModules/StartModules is in progress (%q); cancel its context and wait for it to return instead", ErrInvalidLifecycleState, state)
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

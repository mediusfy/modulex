// Package nats provides a Modulex EventBus adapter backed by NATS.
package nats

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/internal/handlerpanic"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	// logKeyTopic is the structured log key for the NATS subject/topic name.
	logKeyTopic = "topic"
	// logKeyError is the structured log key for an error value.
	logKeyError = "error"
)

// EventBus implements modulex.EventBus by wrapping a concrete NATS connection.
type EventBus struct {
	conn    *nats.Conn
	logger  *slog.Logger
	subsMu  sync.Mutex
	subs    []*nats.Subscription
	cancels []context.CancelFunc
}

// Option configures an EventBus during construction.
type Option func(*EventBus)

// WithLogger sets the logger used to report handler errors encountered while
// consuming messages. If not provided, or if nil, slog.Default() is used.
func WithLogger(logger *slog.Logger) Option {
	return func(n *EventBus) {
		n.logger = logger
	}
}

// NewEventBus instantiates the NATS event bus driver.
//
// The EventBus does not take ownership of conn: the caller creates and closes
// the underlying *nats.Conn, typically after modulex.Manager.StopModules has
// closed the EventBus. This lets a single connection be shared across
// multiple concerns outside the module lifecycle if desired.
func NewEventBus(conn *nats.Conn, opts ...Option) *EventBus {
	n := &EventBus{conn: conn, logger: slog.Default()}
	for _, opt := range opts {
		opt(n)
	}
	if n.logger == nil {
		n.logger = slog.Default()
	}
	return n
}

// Publish implements modulex.EventBus.
func (n *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// The NATS connection is safe for concurrent use by multiple goroutines.
	msg := nats.NewMsg(topic)
	msg.Data = payload

	// Propagate the active trace context into NATS message headers so subscribers
	// can continue the span across process boundaries.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(msg.Header))

	return n.conn.PublishMsg(msg)
}

// Subscribe implements modulex.EventBus. It registers a NATS subscription,
// adapting the incoming message to the generic EventHandler signature.
//
// The subscriber's context is propagated into the handler. If the incoming NATS
// message carries W3C trace context headers, they are extracted and merged so
// OpenTelemetry span continuity is preserved across the broker.
//
// NATS core has no acknowledgement semantics, so a failing handler cannot be
// redelivered or retried by the broker. The error is logged so failures are
// visible instead of silently discarded; this mirrors the acknowledge-and-log
// policy used by the other EventBus adapters in this module (see
// rabbitmq.EventBus.Subscribe and watermill.EventBus.Subscribe).
func (n *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	if handler == nil {
		return fmt.Errorf("failed to subscribe to %s: handler must not be nil", topic)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	n.subsMu.Lock()
	defer n.subsMu.Unlock()

	subCtx, cancel := context.WithCancel(ctx)
	sub, err := n.conn.Subscribe(topic, func(msg *nats.Msg) {
		msgCtx := messageContext(subCtx, msg)
		// handlerpanic.Recover converts a panicking handler into an error so
		// it is reported through the same "handler error" log line as any
		// other handler failure, instead of a separate log shape — see the
		// package doc comment's acknowledge-and-log policy note.
		if err := handlerpanic.Recover(func() error { return handler(msgCtx, msg.Data) }); err != nil {
			n.logger.ErrorContext(msgCtx, "handler error",
				slog.String(logKeyTopic, topic),
				slog.Any(logKeyError, err),
			)
		}
	})
	if err != nil {
		cancel()
		return fmt.Errorf("failed to subscribe to %s: %w", topic, err)
	}

	n.subs = append(n.subs, sub)
	n.cancels = append(n.cancels, cancel)
	return nil
}

// messageContext returns the subscriber context enriched with any trace context
// carried by NATS message headers. The subscriber context is always used as the
// base so cancellation is respected even when no headers are present.
func messageContext(base context.Context, msg *nats.Msg) context.Context {
	if msg.Header == nil {
		return base
	}
	return otel.GetTextMapPropagator().Extract(base, propagation.HeaderCarrier(msg.Header))
}

// Close implements modulex.EventBus. It unsubscribes all registered NATS
// subscriptions but does not close the underlying *nats.Conn, which the
// caller owns.
func (n *EventBus) Close(ctx context.Context) error {
	n.subsMu.Lock()
	defer n.subsMu.Unlock()

	for _, sub := range n.subs {
		_ = sub.Unsubscribe()
	}
	for _, cancel := range n.cancels {
		cancel()
	}
	n.subs = nil
	n.cancels = nil
	return nil
}

var (
	_ modulex.EventBus   = (*EventBus)(nil)
	_ modulex.Publisher  = (*EventBus)(nil)
	_ modulex.Subscriber = (*EventBus)(nil)
)

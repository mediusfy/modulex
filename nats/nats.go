// Package nats provides a Modulex EventBus adapter backed by NATS.
package nats

import (
	"context"
	"fmt"
	"sync"

	"github.com/mediusfy/modulex"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// EventBus implements modulex.EventBus by wrapping a concrete NATS connection.
type EventBus struct {
	conn   *nats.Conn
	subsMu sync.Mutex
	subs   []*nats.Subscription
}

// NewEventBus instantiates the NATS event bus driver.
//
// The EventBus does not take ownership of conn: the caller creates and closes
// the underlying *nats.Conn, typically after modulex.Manager.StopModules has
// closed the EventBus. This lets a single connection be shared across
// multiple concerns outside the module lifecycle if desired.
func NewEventBus(conn *nats.Conn) *EventBus {
	return &EventBus{conn: conn}
}

// Publish implements modulex.EventBus.
func (n *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
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
func (n *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	n.subsMu.Lock()
	defer n.subsMu.Unlock()

	sub, err := n.conn.Subscribe(topic, func(msg *nats.Msg) {
		msgCtx := messageContext(ctx, msg)
		_ = handler(msgCtx, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to %s: %w", topic, err)
	}

	n.subs = append(n.subs, sub)
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
	n.subs = nil
	return nil
}

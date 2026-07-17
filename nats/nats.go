// Package nats provides a Modulex EventBus adapter backed by NATS.
package nats

import (
	"context"
	"fmt"
	"sync"

	"github.com/mediusfy/modulex"
	"github.com/nats-io/nats.go"
)

// EventBus implements modulex.EventBus by wrapping a concrete NATS connection.
type EventBus struct {
	conn *nats.Conn
	mu   sync.Mutex
	subs []*nats.Subscription
}

// NewEventBus instantiates the NATS event bus driver.
func NewEventBus(conn *nats.Conn) *EventBus {
	return &EventBus{conn: conn}
}

// Publish implements modulex.EventBus.
func (n *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	return n.conn.Publish(topic, payload)
}

// Subscribe implements modulex.EventBus. It registers a NATS subscription,
// adapting the incoming message to the generic EventHandler signature.
func (n *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	sub, err := n.conn.Subscribe(topic, func(msg *nats.Msg) {
		// Invoke the generic handler signature. In a production environment,
		// context propagation can be managed here (e.g. extracting tracing headers).
		_ = handler(context.Background(), msg.Data)
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to %s: %w", topic, err)
	}

	n.subs = append(n.subs, sub)
	return nil
}

// Close implements modulex.EventBus. It unsubscribes all registered NATS subscriptions.
func (n *EventBus) Close(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	for _, sub := range n.subs {
		_ = sub.Unsubscribe()
	}
	n.subs = nil
	return nil
}

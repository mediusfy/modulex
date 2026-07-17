package modulex

import (
	"context"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
)

// NATSEventBus implements EventBus by wrapping a concrete NATS connection.
type NATSEventBus struct {
	conn *nats.Conn
	mu   sync.Mutex
	subs []*nats.Subscription
}

// NewNATSEventBus instantiates the NATS event bus driver.
func NewNATSEventBus(conn *nats.Conn) *NATSEventBus {
	return &NATSEventBus{conn: conn}
}

// Publish implements EventBus.
func (n *NATSEventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	return n.conn.Publish(topic, payload)
}

// Subscribe implements EventBus. It registers a NATS subscription, adapting the
// incoming message to the generic EventHandler signature.
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string, handler EventHandler) error {
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

// Close implements EventBus. It unsubscribes all registered NATS subscriptions.
func (n *NATSEventBus) Close(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	for _, sub := range n.subs {
		_ = sub.Unsubscribe()
	}
	n.subs = nil
	return nil
}

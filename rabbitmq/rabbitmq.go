// Package rabbitmq provides a Modulex EventBus adapter backed by RabbitMQ.
package rabbitmq

import (
	"context"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mediusfy/modulex"
)

// EventBus implements modulex.EventBus by wrapping a RabbitMQ channel.
type EventBus struct {
	ch      *amqp.Channel
	mu      sync.Mutex
	cancels []func() // tracks consumer cancellation contexts
}

// NewEventBus instantiates the RabbitMQ event bus driver.
func NewEventBus(ch *amqp.Channel) *EventBus {
	return &EventBus{ch: ch}
}

// Publish implements modulex.EventBus.
func (r *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	return r.ch.PublishWithContext(ctx,
		"",    // exchange
		topic, // routing key
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType: "application/octet-stream",
			Body:        payload,
		},
	)
}

// Subscribe implements modulex.EventBus. It consumes messages from the queue in a background routine.
func (r *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	msgs, err := r.ch.Consume(
		topic, // queue
		"",    // consumer name
		true,  // auto-ack
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		return err
	}

	subCtx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancels = append(r.cancels, cancel)
	r.mu.Unlock()

	// Spin up consumer loop
	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case d, ok := <-msgs:
				if !ok {
					return
				}
				_ = handler(subCtx, d.Body)
			}
		}
	}()

	return nil
}

// Close implements modulex.EventBus. It cancels all active queue consumers.
func (r *EventBus) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = nil
	return nil
}

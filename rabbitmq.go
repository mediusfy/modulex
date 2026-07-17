package modulex

import (
	"context"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQEventBus implements EventBus by wrapping a RabbitMQ channel.
type RabbitMQEventBus struct {
	ch      *amqp.Channel
	mu      sync.Mutex
	cancels []func() // tracks consumer cancellation contexts
}

// NewRabbitMQEventBus instantiates the RabbitMQ event bus driver.
func NewRabbitMQEventBus(ch *amqp.Channel) *RabbitMQEventBus {
	return &RabbitMQEventBus{ch: ch}
}

// Publish implements EventBus.
func (r *RabbitMQEventBus) Publish(ctx context.Context, topic string, payload []byte) error {
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

// Subscribe implements EventBus. It consumes messages from the queue in a background routine.
func (r *RabbitMQEventBus) Subscribe(ctx context.Context, topic string, handler EventHandler) error {
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

// Close implements EventBus. It cancels all active queue consumers.
func (r *RabbitMQEventBus) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = nil
	return nil
}

// Package rabbitmq provides a Modulex EventBus adapter backed by RabbitMQ.
package rabbitmq

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mediusfy/modulex"
	"go.opentelemetry.io/otel"
)

const (
	// logKeyQueue is the structured log key for the RabbitMQ queue/topic name.
	logKeyQueue = "queue"
	// logKeyError is the structured log key for an error value.
	logKeyError = "error"
)

// EventBus implements modulex.EventBus by wrapping a RabbitMQ channel.

type amqpHeadersCarrier map[string]interface{}

func (c amqpHeadersCarrier) Get(key string) string {
	if v, ok := c[key].(string); ok {
		return v
	}
	return ""
}

func (c amqpHeadersCarrier) Set(key string, value string) {
	c[key] = value
}

func (c amqpHeadersCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

type EventBus struct {
	ch      *amqp.Channel
	logger  *slog.Logger
	mu      sync.Mutex
	nextTag int
	tags    []string
	cancels []context.CancelFunc // tracks consumer cancellation contexts
}

// Option configures an EventBus during construction.
type Option func(*EventBus)

// WithLogger sets the logger used to report handler errors encountered while
// consuming messages. If not provided, or if nil, slog.Default() is used.
func WithLogger(logger *slog.Logger) Option {
	return func(r *EventBus) {
		r.logger = logger
	}
}

// NewEventBus instantiates the RabbitMQ event bus driver.
//
// The EventBus does not take ownership of ch: the caller creates and closes
// the underlying *amqp.Channel (and its connection), typically after
// modulex.Manager.StopModules has closed the EventBus. This lets a single
// channel or connection be shared across multiple concerns outside the
// module lifecycle if desired.
func NewEventBus(ch *amqp.Channel, opts ...Option) *EventBus {
	r := &EventBus{ch: ch, logger: slog.Default()}
	for _, opt := range opts {
		opt(r)
	}
	if r.logger == nil {
		r.logger = slog.Default()
	}
	return r
}

// Publish implements modulex.EventBus.
//
// Publishing uses the RabbitMQ default exchange (""), where the routing key is
// interpreted as the target queue name. Therefore the topic parameter is the
// queue to which the message is delivered. For routed exchanges, use a
// broker-specific publisher instead of this adapter.
func (r *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	headers := make(amqp.Table)
	otel.GetTextMapPropagator().Inject(ctx, amqpHeadersCarrier(headers))
	return r.ch.PublishWithContext(ctx,
		"",    // default exchange
		topic, // routing key == queue name on the default exchange
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType: "application/octet-stream",
			Headers:     headers,
			Body:        payload,
		},
	)
}

// Subscribe implements modulex.EventBus. It declares the target queue and then
// consumes messages from it in a background routine. The queue is declared as
// durable and non-exclusive so the adapter works out of the box. If the caller
// cancels the supplied context, the consumer goroutine exits.
//
// Messages are acknowledged manually: a successful handler call acks the
// message, and a failing handler call nacks it without requeue and logs the
// error. Requeuing is deliberately not attempted here since a persistently
// failing handler would otherwise redeliver the same message forever; this
// matches the acknowledge-and-log policy used by the other EventBus adapters
// in this module (see watermill.EventBus.Subscribe).
func (r *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	if _, err := r.ch.QueueDeclare(
		topic, // name
		true,  // durable
		false, // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	); err != nil {
		return fmt.Errorf("failed to declare queue %q: %w", topic, err)
	}

	r.mu.Lock()
	tag := fmt.Sprintf("modulex-consumer-%d", r.nextTag)
	r.nextTag++
	r.mu.Unlock()

	msgs, err := r.ch.Consume(
		topic, // queue
		tag,   // consumer name
		false, // auto-ack (messages are acked/nacked manually below)
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("failed to consume queue %q: %w", topic, err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.tags = append(r.tags, tag)
	r.cancels = append(r.cancels, cancel)
	r.mu.Unlock()

	// Spin up consumer loop
	go func() {
		defer cancel()
		for {
			select {
			case <-subCtx.Done():
				return
			case d, ok := <-msgs:
				if !ok {
					return
				}
				var headers amqp.Table
				if d.Headers != nil {
					headers = d.Headers
				} else {
					headers = make(amqp.Table)
				}
				msgCtx := otel.GetTextMapPropagator().Extract(subCtx, amqpHeadersCarrier(headers))
				if err := handler(msgCtx, d.Body); err != nil {
					r.logger.ErrorContext(msgCtx, "handler error, message nacked without requeue",
						slog.String(logKeyQueue, topic),
						slog.Any(logKeyError, err),
					)
					if nackErr := d.Nack(false, false); nackErr != nil {
						r.logger.ErrorContext(msgCtx, "failed to nack message",
							slog.String(logKeyQueue, topic),
							slog.Any(logKeyError, nackErr),
						)
					}
					continue
				}
				if ackErr := d.Ack(false); ackErr != nil {
					r.logger.ErrorContext(msgCtx, "failed to ack message",
						slog.String(logKeyQueue, topic),
						slog.Any(logKeyError, ackErr),
					)
				}
			}
		}
	}()

	return nil
}

// Close implements modulex.EventBus. It cancels all active queue consumers
// but does not close the underlying *amqp.Channel or its connection, which
// the caller owns.
func (r *EventBus) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tag := range r.tags {
		_ = r.ch.Cancel(tag, false)
	}
	for _, cancel := range r.cancels {
		cancel()
	}
	r.tags = nil
	r.cancels = nil
	return nil
}

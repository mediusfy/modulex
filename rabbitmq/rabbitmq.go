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

func (c amqpHeadersCarrier) Set(key, value string) {
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
	closed  bool
	active  int
	stopped chan struct{}
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
	stopped := make(chan struct{})
	close(stopped)
	r := &EventBus{ch: ch, logger: slog.Default(), stopped: stopped}
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
	if err := ctx.Err(); err != nil {
		return err
	}

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
	if handler == nil {
		return fmt.Errorf("failed to subscribe to queue %q: handler must not be nil", topic)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.checkClosed(); err != nil {
		return fmt.Errorf("failed to subscribe to queue %q: %w", topic, err)
	}

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

	tag, msgs, err := r.startConsumer(topic)
	if err != nil {
		return err
	}

	subCtx, cancel := context.WithCancel(ctx)
	stopped, err := r.registerConsumer(tag, cancel)
	if err != nil {
		cancel()
		_ = r.ch.Cancel(tag, false)
		return fmt.Errorf("failed to subscribe to queue %q: %w", topic, err)
	}

	go r.consumeLoop(subCtx, topic, handler, msgs, cancel, stopped)
	return nil
}

func (r *EventBus) checkClosed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("event bus is closed")
	}
	return nil
}

func (r *EventBus) startConsumer(topic string) (string, <-chan amqp.Delivery, error) {
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
		return "", nil, fmt.Errorf("failed to consume queue %q: %w", topic, err)
	}
	return tag, msgs, nil
}

func (r *EventBus) registerConsumer(tag string, cancel context.CancelFunc) (chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("event bus is closed")
	}
	if r.active == 0 {
		r.stopped = make(chan struct{})
	}
	r.active++
	r.tags = append(r.tags, tag)
	r.cancels = append(r.cancels, cancel)
	return r.stopped, nil
}

func (r *EventBus) consumeLoop(ctx context.Context, topic string, handler modulex.EventHandler, msgs <-chan amqp.Delivery, cancel context.CancelFunc, stopped chan struct{}) {
	defer func() {
		cancel()
		r.mu.Lock()
		r.active--
		if r.active == 0 {
			close(stopped)
		}
		r.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-msgs:
			if !ok {
				return
			}
			r.handleDelivery(ctx, topic, handler, d)
		}
	}
}

func (r *EventBus) handleDelivery(ctx context.Context, topic string, handler modulex.EventHandler, d amqp.Delivery) {
	var headers amqp.Table
	if d.Headers != nil {
		headers = d.Headers
	} else {
		headers = make(amqp.Table)
	}
	msgCtx := otel.GetTextMapPropagator().Extract(ctx, amqpHeadersCarrier(headers))
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
		return
	}
	if ackErr := d.Ack(false); ackErr != nil {
		r.logger.ErrorContext(msgCtx, "failed to ack message",
			slog.String(logKeyQueue, topic),
			slog.Any(logKeyError, ackErr),
		)
	}
}

// Close implements modulex.EventBus. It cancels all active queue consumers,
// waits for their goroutines to exit, and does not close the underlying
// *amqp.Channel or its connection, which the caller owns.
func (r *EventBus) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	tags := append([]string(nil), r.tags...)
	cancels := append([]context.CancelFunc(nil), r.cancels...)
	stopped := r.stopped
	r.tags = nil
	r.cancels = nil
	r.mu.Unlock()

	for _, tag := range tags {
		_ = r.ch.Cancel(tag, false)
	}
	for _, cancel := range cancels {
		cancel()
	}

	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	_ modulex.EventBus   = (*EventBus)(nil)
	_ modulex.Publisher  = (*EventBus)(nil)
	_ modulex.Subscriber = (*EventBus)(nil)
)

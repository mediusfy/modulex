package rabbitmq

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/internal/handlerpanic"
	"github.com/mediusfy/modulex/workerpool"
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

	// consumeMu serializes each consumer's Qos+Consume pair on the shared
	// channel, so a SubscribeWithOptions call's pooled prefetch can never be
	// interleaved with (and leak onto) a concurrent plain Subscribe call's
	// consumer, or vice versa. It is a buffered channel used as a
	// context-cancellable mutex (acquire = send, release = receive) rather
	// than a sync.Mutex, so a Subscribe call waiting for it can still be
	// unblocked by its own ctx instead of hanging until the lock holder's
	// (potentially stalled) broker RPC returns.
	consumeMu chan struct{}

	// qosOwnedByPool records that a SubscribeWithOptions call has changed
	// this channel's per-consumer prefetch default (Channel.Qos with
	// global=false applies to every consumer created after the call, not
	// just the one immediately following it, and there is no API to read a
	// channel's current Qos value back in order to restore it). Once set,
	// a later plain Subscribe on this EventBus is rejected instead of
	// silently creating a consumer bound by the pool's prefetch: only
	// accessed while holding consumeMu, alongside the Qos/Consume pair it
	// guards.
	qosOwnedByPool bool
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
	r := &EventBus{ch: ch, logger: slog.Default(), stopped: stopped, consumeMu: make(chan struct{}, 1)}
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
//
// Subscribe fails if SubscribeWithOptions has already been called on this
// EventBus: RabbitMQ's per-consumer Qos default is scoped to the whole
// channel and stays changed for every consumer created afterward, so a
// plain consumer created after a pooled one would silently inherit the
// pool's prefetch instead of the channel's original value. Use a dedicated
// *amqp.Channel (and EventBus) for plain subscriptions made after any
// SubscribeWithOptions call.
func (r *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	return r.subscribe(ctx, topic, handler, nil, 0)
}

// SubscribeWithOptions enables opt-in bounded concurrent processing. RabbitMQ
// prefetch is set to Workers plus QueueCapacity, messages are acknowledged or
// nacked only after their handler completes, and processing order is not
// guaranteed when Workers is greater than one.
//
// Calling SubscribeWithOptions changes this channel's default prefetch for
// every consumer created afterward, for the rest of this EventBus's
// lifetime; see Subscribe's doc comment for why a plain Subscribe call made
// after this one is rejected.
func (r *EventBus) SubscribeWithOptions(ctx context.Context, topic string, handler modulex.EventHandler, options workerpool.Options) error {
	if handler == nil {
		return fmt.Errorf("failed to subscribe to queue %q: handler must not be nil", topic)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.checkClosed(); err != nil {
		return fmt.Errorf("failed to subscribe to queue %q: %w", topic, err)
	}
	processor, err := workerpool.New(options)
	if err != nil {
		return fmt.Errorf("failed to subscribe to queue %q: invalid worker options: %w", topic, err)
	}
	if err := r.subscribe(ctx, topic, handler, processor, options.Workers+options.QueueCapacity); err != nil {
		_ = processor.Close(context.Background())
		return err
	}
	return nil
}

// subscribe declares topic and starts consuming it. prefetch configures the
// channel's Qos immediately before the consumer is created; 0 (used by plain
// Subscribe) leaves the channel's existing prefetch untouched entirely,
// since the channel may be shared with code outside this EventBus that
// depends on its own Qos setting (see NewEventBus's doc comment) and plain
// Subscribe must not silently override it. If prefetch is 0 but a prior
// call with prefetch > 0 already changed the channel's default (see
// qosOwnedByPool), startConsumer rejects the call instead of silently
// creating a consumer bound by that leaked prefetch. See consumeMu's doc
// comment for why the Qos call, when made, must happen atomically with
// Consume.
func (r *EventBus) subscribe(ctx context.Context, topic string, handler modulex.EventHandler, processor *workerpool.Processor, prefetch int) error {
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

	tag, msgs, err := r.startConsumer(ctx, topic, prefetch)
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

	go r.consumeLoop(subCtx, topic, handler, msgs, cancel, stopped, processor)
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

func (r *EventBus) startConsumer(ctx context.Context, topic string, prefetch int) (string, <-chan amqp.Delivery, error) {
	r.mu.Lock()
	tag := fmt.Sprintf("modulex-consumer-%d", r.nextTag)
	r.nextTag++
	r.mu.Unlock()

	// Qos and Consume must be applied atomically with respect to other
	// Subscribe/SubscribeWithOptions calls on this shared channel: Qos with
	// global=false takes effect for every consumer created after the call
	// (not only the one immediately following it), so an interleaved Qos
	// call from another goroutine here would leak its prefetch onto this
	// consumer (or vice versa). consumeMu is acquired via select so a
	// caller's ctx can still interrupt the wait instead of blocking forever
	// behind a stalled Qos/Consume broker round trip held by another
	// goroutine.
	select {
	case r.consumeMu <- struct{}{}:
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	defer func() { <-r.consumeMu }()

	if prefetch > 0 {
		if err := r.ch.Qos(prefetch, 0, false); err != nil {
			return "", nil, fmt.Errorf("failed to configure prefetch for queue %q: %w", topic, err)
		}
		// Recorded only on success: this Qos call has now changed the
		// channel's default prefetch for every consumer created after it,
		// for the remaining lifetime of this EventBus. There is no way to
		// read the prior value back in order to restore it once this
		// subscription stops, so the leaked default is treated as
		// permanent for this channel.
		r.qosOwnedByPool = true
	} else if r.qosOwnedByPool {
		return "", nil, fmt.Errorf("failed to consume queue %q: a SubscribeWithOptions call has already changed this "+
			"channel's default prefetch; a plain Subscribe consumer created now would silently inherit it instead of "+
			"the channel's original prefetch. Use a dedicated *amqp.Channel for plain Subscribe calls made after "+
			"SubscribeWithOptions on this EventBus", topic)
	}

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

func (r *EventBus) consumeLoop(ctx context.Context, topic string, handler modulex.EventHandler, msgs <-chan amqp.Delivery,
	cancel context.CancelFunc, stopped chan struct{}, processor *workerpool.Processor) {
	defer func() {
		cancel()
		if processor != nil {
			_ = processor.Close(context.Background())
		}
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
			if processor == nil {
				r.handleDelivery(ctx, topic, handler, d)
			} else {
				r.submitDelivery(ctx, topic, handler, d, processor)
			}
		}
	}
}

func (r *EventBus) submitDelivery(ctx context.Context, topic string, handler modulex.EventHandler, d amqp.Delivery, processor *workerpool.Processor) {
	// ctx (the consume loop's subscription-scoped context), not a context
	// derived from d's headers, is submitted here: header extraction is
	// deferred to the worker goroutine below so it runs in parallel across
	// workers instead of serializing on this single consume-loop goroutine.
	_, err := processor.Submit(ctx, func(taskCtx context.Context) (herr error) {
		var headers amqp.Table
		if d.Headers != nil {
			headers = d.Headers
		} else {
			headers = make(amqp.Table)
		}
		msgCtx := otel.GetTextMapPropagator().Extract(taskCtx, amqpHeadersCarrier(headers))

		// This defer guarantees the delivery is always resolved (acked or
		// nacked) exactly once, even if handler panics: workerpool.runTask's
		// own recover happens after this closure has already unwound, which
		// would otherwise skip the Ack/Nack below entirely. The panic is
		// re-thrown after nacking so workerpool still records it as a failed
		// task via its own panic recovery.
		defer func() {
			if p := recover(); p != nil {
				if nackErr := d.Nack(false, false); nackErr != nil {
					r.logger.ErrorContext(msgCtx, "failed to nack message after handler panic",
						slog.String(logKeyQueue, topic),
						slog.Any(logKeyError, nackErr),
					)
				}
				panic(p)
			}
			if herr != nil {
				r.logger.ErrorContext(msgCtx, "handler error, message nacked without requeue",
					slog.String(logKeyQueue, topic),
					slog.Any(logKeyError, herr),
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
		}()

		return handler(msgCtx, d.Body)
	})
	if err != nil {
		// The handler never ran here (the pool rejected the submission
		// itself, e.g. because the subscription is shutting down), so the
		// message is requeued for redelivery rather than dropped.
		r.logger.ErrorContext(ctx, "worker pool rejected message, requeuing for redelivery",
			slog.String(logKeyQueue, topic),
			slog.Any(logKeyError, err),
		)
		if nackErr := d.Nack(false, true); nackErr != nil {
			r.logger.ErrorContext(ctx, "failed to nack rejected message",
				slog.String(logKeyQueue, topic),
				slog.Any(logKeyError, nackErr),
			)
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

	err := handlerpanic.Recover(func() error { return handler(msgCtx, d.Body) })

	if err != nil {
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

	if stopped == nil {
		return nil
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

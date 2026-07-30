package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mediusfy/modulex"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// ErrJetStreamSubscribeUnsupported is returned by
// JetStreamEventBus.Subscribe. JetStream consumption requires substantially
// more configuration (durable vs ephemeral consumers, ack policies,
// delivery subjects, replay policy) than the core NATS EventBus's
// fire-and-forget Subscribe can express, so JetStreamEventBus is
// deliberately publish-only via the plain modulex.EventBus/Subscriber
// capability. Use EventBus.Subscribe (core NATS, no durability) for
// fire-and-forget consumption, or SubscribeDurable (modulex.DurableConsumer)
// on this same type for durable, acknowledged consumption.
var ErrJetStreamSubscribeUnsupported = errors.New("nats: JetStreamEventBus does not support Subscribe; use nats.EventBus or SubscribeDurable")

// ErrDurableConsumerNameRequired is returned by SubscribeDurable when called
// without modulex.WithConsumerName (or with an empty/whitespace-only name).
var ErrDurableConsumerNameRequired = errors.New("nats: SubscribeDurable requires a non-empty ConsumerName")

const (
	// logKeyConsumer is the structured log key for a durable consumer name.
	logKeyConsumer = "consumer_name"

	// defaultDurableMaxDeliver is the default maximum number of delivery
	// attempts (including the first) JetStream makes for a message before
	// it stops redelivering, absent WithDurableMaxDeliver.
	defaultDurableMaxDeliver = 5

	// defaultDurableAckWait is the default time JetStream waits for an
	// ack/nack/term before considering a delivery unacknowledged and
	// eligible for redelivery, absent WithDurableAckWait.
	defaultDurableAckWait = 30 * time.Second

	// defaultDurableBatchSize is the default number of messages fetched per
	// pull request, absent WithDurableBatchSize.
	defaultDurableBatchSize = 10

	// defaultDurableFetchWait is the default maximum time a single pull
	// request waits for at least one message before returning empty, absent
	// WithDurableFetchWait.
	defaultDurableFetchWait = 5 * time.Second

	// defaultDeadLetterSuffix is appended to a topic to form its
	// dead-letter subject, absent WithDurableDeadLetterSuffix.
	defaultDeadLetterSuffix = ".DEAD"
)

// JetStreamEventBus implements modulex.EventBus's Publish and Close using
// NATS JetStream for at-least-once, acknowledged publishing, and
// additionally implements modulex.DurableConsumer via SubscribeDurable for
// durable, explicitly-acknowledged consumption.
//
// Use JetStreamEventBus when a module needs to publish (fire-and-confirm)
// to a JetStream stream, and optionally also consume durably from it with
// explicit ack/nack/dead-letter control — e.g. sourcing and/or consuming
// domain events shared with other services.
type JetStreamEventBus struct {
	js     nats.JetStreamContext
	logger *slog.Logger

	durableMaxDeliver int
	durableAckWait    time.Duration
	durableBatchSize  int
	durableFetchWait  time.Duration
	deadLetterSuffix  string

	mu             sync.Mutex
	closed         bool
	durableSubs    []*nats.Subscription
	durableCancels []context.CancelFunc
	durableWG      sync.WaitGroup
}

// JetStreamOption configures a JetStreamEventBus during construction.
type JetStreamOption func(*JetStreamEventBus)

// WithJetStreamLogger sets the logger used to report errors. If not
// provided, or if nil, slog.Default() is used.
func WithJetStreamLogger(logger *slog.Logger) JetStreamOption {
	return func(j *JetStreamEventBus) {
		j.logger = logger
	}
}

// WithDurableMaxDeliver sets the maximum number of delivery attempts
// (including the first) a durable consumer makes for a message before
// JetStream stops redelivering it, for all SubscribeDurable subscriptions
// on this JetStreamEventBus. This is a construction-time option (rather
// than a per-call modulex.DurableSubscribeOption) because it is a
// NATS/JetStream-specific knob that the adapter-agnostic core interface
// does not carry. n must be positive; non-positive values are ignored and
// the default (5) is used.
func WithDurableMaxDeliver(n int) JetStreamOption {
	return func(j *JetStreamEventBus) {
		if n > 0 {
			j.durableMaxDeliver = n
		}
	}
}

// WithDurableAckWait sets how long JetStream waits for an ack/nack/term
// before considering a delivery unacknowledged and eligible for
// redelivery, for all SubscribeDurable subscriptions on this
// JetStreamEventBus. d must be positive; non-positive values are ignored
// and the default (30s) is used.
func WithDurableAckWait(d time.Duration) JetStreamOption {
	return func(j *JetStreamEventBus) {
		if d > 0 {
			j.durableAckWait = d
		}
	}
}

// WithDurableBatchSize sets how many messages a durable consumer's pull
// loop requests per fetch, for all SubscribeDurable subscriptions on this
// JetStreamEventBus. n must be positive; non-positive values are ignored
// and the default (10) is used.
func WithDurableBatchSize(n int) JetStreamOption {
	return func(j *JetStreamEventBus) {
		if n > 0 {
			j.durableBatchSize = n
		}
	}
}

// WithDurableFetchWait sets the maximum time a single pull request waits
// for at least one message before returning empty (and the loop checks for
// context cancellation again), for all SubscribeDurable subscriptions on
// this JetStreamEventBus. d must be positive; non-positive values are
// ignored and the default (5s) is used.
func WithDurableFetchWait(d time.Duration) JetStreamOption {
	return func(j *JetStreamEventBus) {
		if d > 0 {
			j.durableFetchWait = d
		}
	}
}

// WithDurableDeadLetterSuffix sets the suffix appended to a topic to form
// the subject a dead-lettered message (AckDecision DeadLetter) is
// republished to before the original delivery is terminated, for all
// SubscribeDurable subscriptions on this JetStreamEventBus. The resulting
// subject must be covered by a JetStream stream (the same stream, if its
// subject list includes a matching wildcard, or a separate one) or the
// republish fails; the original message is still terminated in that case so
// it is not redelivered forever, and the republish failure is logged.
//
// Pass an empty suffix to disable republishing: DeadLetter then only
// terminates the original delivery. The default suffix is ".DEAD".
func WithDurableDeadLetterSuffix(suffix string) JetStreamOption {
	return func(j *JetStreamEventBus) {
		j.deadLetterSuffix = suffix
	}
}

// NewJetStreamEventBus instantiates an EventBus backed by JetStream that
// publishes via Publish/Close and, when SubscribeDurable is used, also
// provides durable consumption (modulex.DurableConsumer). js is typically
// obtained via (*nats.Conn).JetStream(); the EventBus does not take
// ownership of the underlying connection, matching the other EventBus
// adapters in this module.
func NewJetStreamEventBus(js nats.JetStreamContext, opts ...JetStreamOption) *JetStreamEventBus {
	j := &JetStreamEventBus{
		js:                js,
		logger:            slog.Default(),
		durableMaxDeliver: defaultDurableMaxDeliver,
		durableAckWait:    defaultDurableAckWait,
		durableBatchSize:  defaultDurableBatchSize,
		durableFetchWait:  defaultDurableFetchWait,
		deadLetterSuffix:  defaultDeadLetterSuffix,
	}
	for _, opt := range opts {
		opt(j)
	}
	if j.logger == nil {
		j.logger = slog.Default()
	}
	return j
}

// Publish implements modulex.EventBus. It publishes to the JetStream stream
// whose subject matches topic and waits for the broker's acknowledgement.
func (j *JetStreamEventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	msg := nats.NewMsg(topic)
	msg.Data = payload

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(msg.Header))

	if _, err := j.js.PublishMsg(msg, nats.Context(ctx)); err != nil {
		return fmt.Errorf("failed to publish to jetstream subject %q: %w", topic, err)
	}
	return nil
}

// Subscribe implements modulex.EventBus. It always returns
// ErrJetStreamSubscribeUnsupported; see the JetStreamEventBus doc comment.
// Use SubscribeDurable for durable JetStream consumption.
func (j *JetStreamEventBus) Subscribe(context.Context, string, modulex.EventHandler) error {
	return ErrJetStreamSubscribeUnsupported
}

// SubscribeDurable implements modulex.DurableConsumer using a JetStream
// pull-based durable consumer.
//
// Acknowledgement: handler's returned modulex.AckDecision is translated to
// JetStream's native ack API — Ack calls msg.Ack(), Nack calls msg.Nak(),
// and DeadLetter republishes the message to the configured dead-letter
// subject (see WithDurableDeadLetterSuffix) and then calls msg.Term() so
// JetStream never redelivers it. An unrecognized AckDecision value (e.g.
// the zero value of a differently-typed constant) is treated as Nack,
// erring toward retry rather than silently acking or dropping.
//
// Retry: on Nack, JetStream redelivers the message after
// WithDurableAckWait, up to WithDurableMaxDeliver total attempts
// (including the first); beyond that JetStream itself stops redelivering
// even without an explicit DeadLetter decision (this is JetStream's own
// max-deliver behavior, not something this adapter enforces separately).
//
// Replay: modulex.WithReplayPolicy(modulex.ReplayAll) (the default) creates
// the durable consumer with JetStream's DeliverAll policy;
// modulex.ReplayNew creates it with DeliverNew. This only affects a
// brand-new ConsumerName — JetStream resumes a pre-existing durable
// consumer from its last acknowledged position regardless of ReplayPolicy.
//
// Ordering: this implementation runs one sequential pull loop per
// SubscribeDurable call — it fetches a batch, resolves (ack/nack/dead-
// letter) every message in the batch in order, and only then fetches the
// next batch — so relative delivery order is preserved for that
// subscription. If multiple SubscribeDurable calls share one ConsumerName,
// JetStream load-balances deliveries across them and order across those
// concurrent pull loops is not guaranteed.
//
// Consumer identity: modulex.WithConsumerName (required) becomes the
// JetStream durable consumer name. Reusing it resumes the same consumer's
// ack progress, including across process restarts; multiple concurrent
// SubscribeDurable calls with the same name form a JetStream
// competing-consumers group.
//
// Dead-letter: see Acknowledgement above.
func (j *JetStreamEventBus) SubscribeDurable(ctx context.Context, topic string, handler modulex.DurableHandler, opts ...modulex.DurableSubscribeOption) error {
	if handler == nil {
		return fmt.Errorf("failed to subscribe durably to %q: handler must not be nil", topic)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var o modulex.DurableSubscribeOptions
	for _, opt := range opts {
		opt(&o)
	}
	if strings.TrimSpace(o.ConsumerName) == "" {
		return fmt.Errorf("failed to subscribe durably to %q: %w", topic, ErrDurableConsumerNameRequired)
	}

	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return fmt.Errorf("failed to subscribe durably to %q: event bus is closed", topic)
	}
	j.mu.Unlock()

	// The durable name is already conveyed via PullSubscribe's second
	// argument below; passing nats.Durable(o.ConsumerName) here as well
	// would set the same option twice and nats.go rejects that.
	subOpts := []nats.SubOpt{
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.MaxDeliver(j.durableMaxDeliver),
		nats.AckWait(j.durableAckWait),
	}
	switch o.Replay {
	case modulex.ReplayNew:
		subOpts = append(subOpts, nats.DeliverNew())
	default:
		subOpts = append(subOpts, nats.DeliverAll())
	}

	sub, err := j.js.PullSubscribe(topic, o.ConsumerName, subOpts...)
	if err != nil {
		return fmt.Errorf("failed to create durable consumer %q for %q: %w", o.ConsumerName, topic, err)
	}

	subCtx, cancel := context.WithCancel(ctx)

	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		cancel()
		_ = sub.Unsubscribe()
		return fmt.Errorf("failed to subscribe durably to %q: event bus is closed", topic)
	}
	j.durableSubs = append(j.durableSubs, sub)
	j.durableCancels = append(j.durableCancels, cancel)
	j.durableWG.Add(1)
	j.mu.Unlock()

	go j.durableConsumeLoop(subCtx, topic, o.ConsumerName, sub, handler)
	return nil
}

// durableConsumeLoop pulls and resolves messages for one SubscribeDurable
// subscription until ctx is cancelled. It fetches at most one outstanding
// batch at a time, resolving every message in a batch before fetching the
// next, so that messages delivered to this subscription are processed in
// order (see the "Ordering" semantic on SubscribeDurable).
func (j *JetStreamEventBus) durableConsumeLoop(ctx context.Context, topic, consumerName string, sub *nats.Subscription, handler modulex.DurableHandler) {
	defer j.durableWG.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// nats.go rejects passing both nats.Context and nats.MaxWait to
		// Fetch, so cancellation is not plumbed into the Fetch call itself;
		// instead each Fetch is bounded by durableFetchWait, and the
		// ctx.Done() check at the top of this loop re-runs at least that
		// often, bounding cancellation latency by durableFetchWait.
		msgs, err := sub.Fetch(j.durableBatchSize, nats.MaxWait(j.durableFetchWait))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, nats.ErrTimeout) {
				// No messages arrived within the fetch window; poll again.
				continue
			}
			j.logger.ErrorContext(ctx, "durable fetch error",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any(logKeyError, err),
			)
			continue
		}

		for _, msg := range msgs {
			j.handleDurableMessage(ctx, topic, consumerName, msg, handler)
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}

// handleDurableMessage invokes handler for one delivered message and
// translates its AckDecision into the corresponding JetStream ack call.
func (j *JetStreamEventBus) handleDurableMessage(ctx context.Context, topic, consumerName string, msg *nats.Msg, handler modulex.DurableHandler) {
	dm := modulex.DurableMessage{Payload: msg.Data}
	if meta, metaErr := msg.Metadata(); metaErr == nil && meta != nil {
		dm.DeliveryCount = int(meta.NumDelivered)
		dm.Redelivered = meta.NumDelivered > 1
	}

	msgCtx := ctx
	if msg.Header != nil {
		msgCtx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(msg.Header))
	}

	switch decision := handler(msgCtx, dm); decision {
	case modulex.Ack:
		if err := msg.Ack(); err != nil {
			j.logger.ErrorContext(msgCtx, "failed to ack durable message",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any(logKeyError, err),
			)
		}
	case modulex.DeadLetter:
		j.deadLetter(msgCtx, topic, consumerName, msg)
	default:
		if decision != modulex.Nack {
			j.logger.ErrorContext(msgCtx, "unrecognized ack decision, nacking for retry",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any("decision", int(decision)),
			)
		}
		if err := msg.Nak(); err != nil {
			j.logger.ErrorContext(msgCtx, "failed to nack durable message",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any(logKeyError, err),
			)
		}
	}
}

// deadLetter republishes msg to the configured dead-letter subject (if any)
// and then terminates the original delivery so JetStream never redelivers
// it. A failure to republish is logged but does not prevent termination:
// the original delivery must still stop retrying even if the dead-letter
// copy could not be made.
func (j *JetStreamEventBus) deadLetter(ctx context.Context, topic, consumerName string, msg *nats.Msg) {
	if j.deadLetterSuffix != "" {
		dlSubject := topic + j.deadLetterSuffix
		dl := nats.NewMsg(dlSubject)
		dl.Data = msg.Data
		for k, v := range msg.Header {
			dl.Header[k] = append([]string(nil), v...)
		}
		if _, err := j.js.PublishMsg(dl, nats.Context(ctx)); err != nil {
			j.logger.ErrorContext(ctx, "failed to publish dead-lettered message",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.String("dead_letter_subject", dlSubject),
				slog.Any(logKeyError, err),
			)
		}
	}
	if err := msg.Term(); err != nil {
		j.logger.ErrorContext(ctx, "failed to terminate dead-lettered message",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyConsumer, consumerName),
			slog.Any(logKeyError, err),
		)
	}
}

// Close implements modulex.EventBus. It cancels all active SubscribeDurable
// pull loops, unsubscribes their JetStream consumers, and waits for the
// loop goroutines to exit (bounded by ctx). JetStreamContext has no
// separate connection to close; the underlying *nats.Conn is caller-owned,
// matching the other EventBus adapters in this module.
func (j *JetStreamEventBus) Close(ctx context.Context) error {
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.closed = true
	subs := append([]*nats.Subscription(nil), j.durableSubs...)
	cancels := append([]context.CancelFunc(nil), j.durableCancels...)
	j.durableSubs = nil
	j.durableCancels = nil
	j.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}

	done := make(chan struct{})
	go func() {
		j.durableWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("jetstream durable consumer close timed out: %w", ctx.Err())
	}
}

var (
	_ modulex.EventBus        = (*JetStreamEventBus)(nil)
	_ modulex.Publisher       = (*JetStreamEventBus)(nil)
	_ modulex.Subscriber      = (*JetStreamEventBus)(nil)
	_ modulex.DurableConsumer = (*JetStreamEventBus)(nil)
)

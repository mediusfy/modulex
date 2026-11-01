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
	"github.com/mediusfy/modulex/internal/busshutdown"
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

	// initialDurableFetchBackoff is the delay before the first retry after a
	// non-timeout durableConsumeLoop Fetch error, and the delay it resets to
	// after any successful (or timed-out, which is itself "successful" in the
	// sense of "the server is fine, there was just nothing to deliver")
	// Fetch.
	initialDurableFetchBackoff = 100 * time.Millisecond

	// maxDurableFetchBackoff caps the exponential backoff applied between
	// durableConsumeLoop Fetch retries so a persistently failing fetch still
	// polls periodically rather than backing off forever.
	maxDurableFetchBackoff = 30 * time.Second

	// maxDurableNakDelay caps the exponential redelivery delay
	// handleDurableMessage's Nack path requests via msg.NakWithDelay, so a
	// message that has already failed many times still gets redelivered on
	// a bounded cadence rather than an ever-growing one.
	maxDurableNakDelay = 5 * time.Minute
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
// JetStream's native ack API — Ack calls msg.Ack(), Nack calls
// msg.NakWithDelay() with an exponential backoff derived from the message's
// delivery count (capped at maxDurableNakDelay) rather than an immediate
// msg.Nak(), and DeadLetter republishes the message to the configured
// dead-letter subject (see WithDurableDeadLetterSuffix) and then calls
// msg.Term() so JetStream never redelivers it. An unrecognized AckDecision
// value (e.g. the zero value of a differently-typed constant) is treated as
// Nack, erring toward retry rather than silently acking or dropping.
//
// Retry: on Nack, JetStream redelivers the message after the backoff
// described above (which starts at WithDurableAckWait and doubles per
// delivery attempt), up to WithDurableMaxDeliver total attempts (including
// the first); once a Nack would be the delivery that reaches
// WithDurableMaxDeliver, this adapter proactively dead-letters the message
// (as if the handler had itself returned DeadLetter) instead of nacking it,
// so the dead-letter republish/log path runs even when the handler itself
// never asked for it. Beyond WithDurableMaxDeliver, JetStream itself would
// also stop redelivering even without this (this is JetStream's own
// max-deliver behavior, not something this adapter enforces separately) —
// the proactive dead-letter above only makes that last attempt visible via
// the configured dead-letter subject instead of the message silently
// stopping.
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

	deliverPolicy := nats.DeliverAllPolicy
	if o.Replay == modulex.ReplayNew {
		deliverPolicy = nats.DeliverNewPolicy
	}

	// Create (or reconcile) the durable consumer ourselves before
	// PullSubscribe attaches to it. This matters because nats.go only
	// marks a *nats.Subscription's underlying JetStream consumer for
	// deletion on Unsubscribe/Close when nats.go itself created that
	// consumer as part of the subscribe call; a consumer PullSubscribe
	// merely finds already-present and attaches to is left alone. Letting
	// PullSubscribe create the consumer would therefore delete it (and its
	// acked position) on every graceful Close, undoing the
	// "resumes ... across process restarts" guarantee documented above.
	deliverPolicy, stream, err := j.ensureDurableConsumer(ctx, topic, o.ConsumerName, deliverPolicy)
	if err != nil {
		return fmt.Errorf("failed to create durable consumer %q for %q: %w", o.ConsumerName, topic, err)
	}

	// The durable name is already conveyed via PullSubscribe's second
	// argument below; passing nats.Durable(o.ConsumerName) here as well
	// would set the same option twice and nats.go rejects that.
	subOpts := []nats.SubOpt{
		nats.ManualAck(),
		nats.AckExplicit(),
		nats.MaxDeliver(j.durableMaxDeliver),
		nats.AckWait(j.durableAckWait),
	}
	// Use the policy the consumer actually has (which may differ from the
	// requested Replay; see ensureDurableConsumer), since PullSubscribe
	// rejects a config that disagrees with the existing consumer.
	if deliverPolicy == nats.DeliverNewPolicy {
		subOpts = append(subOpts, nats.DeliverNew())
	} else {
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

	go j.durableConsumeLoop(subCtx, stream, topic, o.ConsumerName, sub, handler)
	return nil
}

// ensureDurableConsumer creates the named durable consumer via
// JetStreamManager.AddConsumer, or reconciles it via UpdateConsumer if it
// already exists with a different configuration, before SubscribeDurable's
// caller attaches to it with PullSubscribe.
//
// This is deliberately not left to nats.go's own "create if missing"
// behavior inside PullSubscribe: when nats.go creates the JetStream
// consumer itself, it also marks the resulting *nats.Subscription for
// deletion on Unsubscribe/Close, which would delete this durable consumer
// (and its acked position) on every graceful shutdown — undoing the
// "resumes across process restarts" guarantee documented on
// SubscribeDurable. Pre-creating the consumer here means PullSubscribe
// below finds it already present and attaches to it instead, so nats.go
// never marks it for deletion.
//
// It returns the deliver policy the consumer actually has (which is the
// requested one unless a consumer already exists with a different policy,
// as JetStream cannot change that after creation; callers must pass the
// returned policy to PullSubscribe) and the resolved stream name.
// The existing consumer's config is looked up via ConsumerInfo first, and
// UpdateConsumer is only called when it actually differs from the desired
// config: on the common restart path (same deploy, same durable options),
// this avoids an UpdateConsumer call whose effect on live delivery/ack
// counters for an unchanged config isn't guaranteed by the nats-server API
// contract.
func (j *JetStreamEventBus) ensureDurableConsumer(ctx context.Context, topic, consumerName string, deliverPolicy nats.DeliverPolicy) (nats.DeliverPolicy, string, error) {
	cfg := &nats.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: topic,
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: deliverPolicy,
		MaxDeliver:    j.durableMaxDeliver,
		AckWait:       j.durableAckWait,
	}

	stream, err := j.js.StreamNameBySubject(topic, nats.Context(ctx))
	if err != nil {
		return deliverPolicy, "", fmt.Errorf("failed to resolve stream for subject %q: %w", topic, err)
	}

	info, err := j.js.ConsumerInfo(stream, consumerName, nats.Context(ctx))
	if err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			return deliverPolicy, "", fmt.Errorf("failed to look up durable consumer %q on stream %q: %w", consumerName, stream, err)
		}
		if _, err := j.js.AddConsumer(stream, cfg, nats.Context(ctx)); err != nil {
			return deliverPolicy, "", fmt.Errorf("failed to create durable consumer %q on stream %q: %w", consumerName, stream, err)
		}
		return deliverPolicy, stream, nil
	}

	// JetStream rejects any change to a live consumer's deliver policy
	// ("deliver policy can not be updated"), so a Replay option that differs
	// from how the consumer was first created cannot take effect. Keep the
	// existing policy (and its start position) and warn, rather than
	// failing the whole subscription on a redeploy that changed Replay.
	if info.Config.DeliverPolicy != cfg.DeliverPolicy {
		j.logger.WarnContext(ctx, "durable consumer exists with a different deliver policy; keeping the existing one (JetStream cannot change it)",
			slog.String(logKeyTopic, topic),
			slog.String(logKeyConsumer, consumerName),
			slog.Any("existing", info.Config.DeliverPolicy),
			slog.Any("requested", cfg.DeliverPolicy),
		)
		cfg.DeliverPolicy = info.Config.DeliverPolicy
		cfg.OptStartSeq = info.Config.OptStartSeq
		cfg.OptStartTime = info.Config.OptStartTime
	}

	if durableConsumerConfigMatches(info.Config, cfg) {
		return cfg.DeliverPolicy, stream, nil
	}

	// The consumer already exists with a different configuration (for
	// example, a prior deploy used different durable options); bring it
	// in line so this subscription's AckWait/MaxDeliver/DeliverPolicy take
	// effect rather than silently using whatever the first deploy created.
	if _, err := j.js.UpdateConsumer(stream, cfg, nats.Context(ctx)); err != nil {
		return cfg.DeliverPolicy, "", fmt.Errorf("failed to update durable consumer %q on stream %q: %w", consumerName, stream, err)
	}
	return cfg.DeliverPolicy, stream, nil
}

// durableConsumerConfigMatches reports whether an existing consumer's
// config already matches the config ensureDurableConsumer would submit,
// so that callers can skip an unnecessary UpdateConsumer call on the common
// restart path.
func durableConsumerConfigMatches(existing nats.ConsumerConfig, desired *nats.ConsumerConfig) bool {
	return existing.FilterSubject == desired.FilterSubject &&
		existing.AckPolicy == desired.AckPolicy &&
		existing.DeliverPolicy == desired.DeliverPolicy &&
		existing.MaxDeliver == desired.MaxDeliver &&
		existing.AckWait == desired.AckWait
}

// durableConsumeLoop pulls and resolves messages for one SubscribeDurable
// subscription until ctx is cancelled. It fetches at most one outstanding
// batch at a time, resolving every message in a batch before fetching the
// next, so that messages delivered to this subscription are processed in
// order (see the "Ordering" semantic on SubscribeDurable).
//
// A non-timeout Fetch error backs off exponentially (capped at
// maxDurableFetchBackoff) before the next attempt, honoring ctx while
// waiting, instead of retrying immediately: without this, a closed
// connection or a server-side-deleted consumer would otherwise make this
// loop spin at full CPU, re-issuing a failing Fetch as fast as it can. A
// terminal error (the connection is closed, or the consumer no longer
// exists) exits the loop promptly instead of backing off forever for a
// condition that will never resolve itself.
func (j *JetStreamEventBus) durableConsumeLoop(ctx context.Context, stream, topic, consumerName string, sub *nats.Subscription, handler modulex.DurableHandler) {
	defer j.durableWG.Done()

	backoff := initialDurableFetchBackoff

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
				// No messages arrived within the fetch window; poll again
				// immediately, and reset the backoff so a single transient
				// error followed by healthy timeouts doesn't leave the loop
				// needlessly slow.
				backoff = initialDurableFetchBackoff
				continue
			}
			matched, matchedVia := isTerminalFetchErrorMatch(err)
			if !matched && errors.Is(err, nats.ErrNoResponders) && j.isDurableConsumerGone(ctx, stream, consumerName) {
				matched, matchedVia = true, "consumer_info"
			}
			if matched {
				j.logger.ErrorContext(ctx, "durable fetch terminal error, stopping consume loop",
					slog.String(logKeyTopic, topic),
					slog.String(logKeyConsumer, consumerName),
					slog.Any(logKeyError, err),
					slog.String("matchedVia", matchedVia),
				)
				return
			}
			j.logger.ErrorContext(ctx, "durable fetch error, backing off before retry",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any(logKeyError, err),
				slog.Duration("backoff", backoff),
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = nextDurableFetchBackoff(backoff)
			continue
		}
		backoff = initialDurableFetchBackoff

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

// isDurableConsumerGone checks with the JetStream server whether the durable
// consumer (or its stream) has been deleted. It is called only when
// sub.Fetch returns nats.ErrNoResponders, which is ambiguous on its own: both
// a consumer deleted between Fetch polls (whose $JS.API.CONSUMER.MSG.NEXT
// subject no longer has a responder) and a transient JetStream RAFT leader
// election or server restart return nats.ErrNoResponders. Only an explicit
// ErrConsumerNotFound / ErrStreamNotFound from ConsumerInfo is treated as
// proof that the consumer is gone; any other lookup error (including another
// ErrNoResponders or timeout during an active leader election) returns false
// so the consume loop backs off and retries instead of exiting.
func (j *JetStreamEventBus) isDurableConsumerGone(ctx context.Context, stream, consumerName string) bool {
	if j.js == nil || stream == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, j.durableFetchWait)
	defer cancel()
	_, err := j.js.ConsumerInfo(stream, consumerName, nats.Context(probeCtx))
	return errors.Is(err, nats.ErrConsumerNotFound) || errors.Is(err, nats.ErrStreamNotFound)
}

// nextDurableFetchBackoff doubles d, capped at maxDurableFetchBackoff, for
// durableConsumeLoop's retry-after-error backoff. Extracted as its own
// function (rather than inlined arithmetic in the loop) so the
// growth/capping behavior itself is directly unit-testable.
func nextDurableFetchBackoff(d time.Duration) time.Duration {
	d *= 2
	if d <= 0 || d > maxDurableFetchBackoff {
		return maxDurableFetchBackoff
	}
	return d
}

// isTerminalFetchError reports whether err from sub.Fetch indicates a
// condition that will never resolve itself by retrying: the connection is
// permanently closed, or the pull consumer no longer exists server-side
// (deleted, or the subscription/consumer was otherwise torn down). Retrying
// forever (even with backoff) against either condition would just spin the
// loop uselessly until the caller's ctx happens to be cancelled, so
// durableConsumeLoop exits immediately instead.
//
// nats.ErrNoResponders is deliberately NOT terminal on its own: although a
// deleted consumer's Fetch can surface it, so can a transient JetStream
// leader election or server restart, and exiting the loop unconditionally
// there would silently end the subscription for good (SubscribeDurable has
// already returned and nothing resubscribes). Instead, durableConsumeLoop
// disambiguates nats.ErrNoResponders via isDurableConsumerGone (a
// ConsumerInfo lookup that only treats an explicit ErrConsumerNotFound /
// ErrStreamNotFound as terminal) and backs off and retries otherwise.
//
// The trailing strings.Contains checks are a fallback for JetStream server
// error text that isn't always wrapped as a typed *nats.Error by the
// client, and are inherently fragile to upstream wording changes. If they
// under-match, the only consequence is that durableConsumeLoop falls back
// to its (already-implemented) exponential backoff instead of exiting
// immediately — not a hot loop — so isTerminalFetchErrorMatch's matchedVia
// result lets the caller log when this fallback, rather than a sentinel,
// is what matched, making that drift observable instead of silent.
func isTerminalFetchError(err error) bool {
	matched, _ := isTerminalFetchErrorMatch(err)
	return matched
}

// isTerminalFetchErrorMatch is isTerminalFetchError's implementation, also
// reporting whether the match came from a typed sentinel ("sentinel") or
// the string-substring fallback ("substring"), for caller-side logging.
func isTerminalFetchErrorMatch(err error) (matched bool, matchedVia string) {
	switch {
	case errors.Is(err, nats.ErrConnectionClosed),
		errors.Is(err, nats.ErrConsumerNotFound),
		errors.Is(err, nats.ErrConsumerDeleted),
		errors.Is(err, nats.ErrBadSubscription),
		errors.Is(err, nats.ErrSubscriptionClosed):
		return true, "sentinel"
	case strings.Contains(err.Error(), "consumer not found"),
		strings.Contains(err.Error(), "consumer deleted"):
		return true, "substring"
	default:
		return false, ""
	}
}

// invokeDurableHandler calls handler and recovers a panic from it, treating
// a panic exactly like an explicit modulex.Nack: logged, with the message
// redelivered subject to the consumer's normal retry policy. Without this,
// a single panicking handler invocation would crash the entire process
// hosting durableConsumeLoop's goroutine (an unrecovered panic in a
// goroutine terminates the whole program, not just that goroutine) — too
// high a blast radius for one bad message or one buggy handler path. This
// mirrors the core Manager's own PanicPolicyLog default for supervised
// tasks (see modulex.go): a durable consumer, whose entire purpose is
// resilient message processing, should be at least as forgiving of a
// panicking callback as the task supervisor already is.
func (j *JetStreamEventBus) invokeDurableHandler(ctx context.Context, topic, consumerName string, handler modulex.DurableHandler, dm modulex.DurableMessage) (decision modulex.AckDecision) {
	defer func() {
		if r := recover(); r != nil {
			j.logger.ErrorContext(ctx, "durable handler panicked, nacking for retry",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any("panic", r),
			)
			decision = modulex.Nack
		}
	}()
	return handler(ctx, dm)
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

	switch decision := j.invokeDurableHandler(msgCtx, topic, consumerName, handler, dm); decision {
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
		if dm.DeliveryCount > 0 && dm.DeliveryCount >= j.durableMaxDeliver {
			// This Nack would be (or already is past) the delivery attempt
			// that exhausts WithDurableMaxDeliver. JetStream would stop
			// redelivering on its own at that point regardless of what this
			// adapter does, but nacking here would let the message vanish
			// silently instead of going through the configured dead-letter
			// path. Route it through DeadLetter explicitly so it is still
			// republished/logged like any other dead-lettered message.
			j.deadLetter(msgCtx, topic, consumerName, msg)
			return
		}
		// NakWithDelay (not an immediate Nak) so a failing message backs off
		// exponentially with its delivery count rather than being
		// redelivered as fast as the consumer can re-fetch it; see
		// nakDelay's doc comment.
		if err := msg.NakWithDelay(j.nakDelay(dm.DeliveryCount)); err != nil {
			j.logger.ErrorContext(msgCtx, "failed to nack durable message",
				slog.String(logKeyTopic, topic),
				slog.String(logKeyConsumer, consumerName),
				slog.Any(logKeyError, err),
			)
		}
	}
}

// nakDelay returns the redelivery delay handleDurableMessage's Nack path
// requests via msg.NakWithDelay, growing exponentially with deliveryCount
// (starting at WithDurableAckWait, doubling per attempt, capped at
// maxDurableNakDelay) so a repeatedly failing message backs off instead of
// being redelivered as fast as the consumer can re-fetch it. deliveryCount
// less than 1 (e.g. metadata was unavailable) is treated as 1, the minimum
// meaningful delivery count.
func (j *JetStreamEventBus) nakDelay(deliveryCount int) time.Duration {
	if deliveryCount < 1 {
		deliveryCount = 1
	}
	delay := j.durableAckWait
	// Capping the number of doublings (rather than only the final result)
	// avoids any risk of the repeated multiplication overflowing
	// time.Duration for a very large deliveryCount before the maxDelay
	// comparison below ever gets a chance to clamp it.
	shifts := deliveryCount - 1
	const maxShifts = 20
	if shifts > maxShifts {
		shifts = maxShifts
	}
	for i := 0; i < shifts && delay < maxDurableNakDelay; i++ {
		delay *= 2
	}
	if delay > maxDurableNakDelay {
		delay = maxDurableNakDelay
	}
	return delay
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

	return busshutdown.WaitGroup(ctx, &j.durableWG, "jetstream durable consumer")
}

var (
	_ modulex.EventBus        = (*JetStreamEventBus)(nil)
	_ modulex.Publisher       = (*JetStreamEventBus)(nil)
	_ modulex.Subscriber      = (*JetStreamEventBus)(nil)
	_ modulex.DurableConsumer = (*JetStreamEventBus)(nil)
)

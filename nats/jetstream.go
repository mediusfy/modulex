package nats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
// deliberately publish-only. Use EventBus.Subscribe (core NATS, no
// durability) or a direct JetStream consumer for message consumption.
var ErrJetStreamSubscribeUnsupported = errors.New("nats: JetStreamEventBus does not support Subscribe; use nats.EventBus or a direct JetStream consumer")

// JetStreamEventBus implements modulex.EventBus's Publish and Close using
// NATS JetStream for at-least-once, acknowledged publishing.
//
// Use JetStreamEventBus when a module only needs to publish (fire-and-
// confirm) to a JetStream stream, e.g. sourcing domain events for other
// services to consume via their own JetStream consumers.
type JetStreamEventBus struct {
	js     nats.JetStreamContext
	logger *slog.Logger
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

// NewJetStreamEventBus instantiates a publish-only EventBus backed by
// JetStream. js is typically obtained via (*nats.Conn).JetStream(); the
// EventBus does not take ownership of the underlying connection, matching
// the other EventBus adapters in this module.
func NewJetStreamEventBus(js nats.JetStreamContext, opts ...JetStreamOption) *JetStreamEventBus {
	j := &JetStreamEventBus{js: js, logger: slog.Default()}
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
func (j *JetStreamEventBus) Subscribe(context.Context, string, modulex.EventHandler) error {
	return ErrJetStreamSubscribeUnsupported
}

// Close implements modulex.EventBus. JetStreamContext has no separate
// connection to close; the underlying *nats.Conn is caller-owned, matching
// the other EventBus adapters in this module.
func (j *JetStreamEventBus) Close(context.Context) error {
	return nil
}

var _ modulex.EventBus = (*JetStreamEventBus)(nil)

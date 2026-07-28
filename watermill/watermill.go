// Package watermill provides a Modulex EventBus adapter backed by Watermill's
// in-memory GoChannel PubSub.
package watermill

import (
	"context"
	"fmt"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"github.com/mediusfy/modulex"
)

// EventBus implements modulex.EventBus using Watermill's GoChannel.
type EventBus struct {
	pubSub *gochannel.GoChannel
	logger watermill.LoggerAdapter

	mu        sync.Mutex
	nextSubID uint64
	cancels   map[uint64]context.CancelFunc
}

// NewEventBus creates a configured in-memory GoChannel PubSub.
//
// Unlike the nats and rabbitmq adapters, the EventBus owns the underlying
// GoChannel PubSub it creates: Close shuts it down. There is no separate
// connection for the caller to manage.
func NewEventBus(bufferSize int64, persistent bool, debug bool) *EventBus {
	logger := watermill.NewStdLogger(debug, debug)

	pubSub := gochannel.NewGoChannel(
		gochannel.Config{
			OutputChannelBuffer: bufferSize, // Prevents publisher blocking if slow consumers
			Persistent:          persistent, // If true, new subscribers get past messages
		},
		logger,
	)

	return &EventBus{
		pubSub:  pubSub,
		logger:  logger,
		cancels: make(map[uint64]context.CancelFunc),
	}
}

// Publish generates a Watermill-compatible message, propagates context/spans, and sends it.
func (w *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Generate a unique Watermill UUID for tracing/deduplication
	msg := message.NewMessage(watermill.NewUUID(), payload)

	// Propagate distributed tracing metadata context
	msg.SetContext(ctx)

	if err := w.pubSub.Publish(topic, msg); err != nil {
		return fmt.Errorf("watermill publish failed: %w", err)
	}
	return nil
}

// Subscribe listens to a topic and handles messages in the background, maintaining span continuity.
func (w *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	if handler == nil {
		return fmt.Errorf("watermill subscription failed: handler must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	messages, err := w.pubSub.Subscribe(ctx, topic)
	if err != nil {
		return fmt.Errorf("watermill subscription failed: %w", err)
	}

	// Derive the consumer context from the caller's context so cancellation of
	// Subscribe's ctx also stops the consumer goroutine.
	subCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	subID := w.nextSubID
	w.nextSubID++
	w.cancels[subID] = cancel
	w.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			w.mu.Lock()
			delete(w.cancels, subID)
			w.mu.Unlock()
		}()
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return // Channel closed (likely EventBus shutting down)
				}

				msgCtx := msg.Context()
				if err := handler(msgCtx, msg.Payload); err != nil {
					// Watermill's in-memory GoChannel redelivers Nack'd messages, which
					// can cause infinite loops for a handler that persistently fails.
					// For this local reference adapter we acknowledge and log the error
					// instead, leaving dead-letter / retry semantics to production brokers.
					msg.Ack()
					w.logger.Error("handler error, message acknowledged to prevent redelivery", err, nil)
				} else {
					msg.Ack()
				}
			}
		}
	}()

	return nil
}

// Close gracefully stops the underlying GoChannel.
func (w *EventBus) Close(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Cancel all consumer loops
	for _, cancel := range w.cancels {
		cancel()
	}
	w.cancels = make(map[uint64]context.CancelFunc)

	// Shut down the pub/sub engine
	if err := w.pubSub.Close(); err != nil {
		return fmt.Errorf("failed to close watermill: %w", err)
	}
	return nil
}

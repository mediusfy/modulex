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

	mu         sync.Mutex
	cancelFunc []context.CancelFunc
}

// NewEventBus creates a configured in-memory GoChannel PubSub.
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
		pubSub: pubSub,
		logger: logger,
	}
}

// Publish generates a Watermill-compatible message, propagates context/spans, and sends it.
func (w *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
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
	// Subscribe returns a read-only channel of messages
	messages, err := w.pubSub.Subscribe(ctx, topic)
	if err != nil {
		return fmt.Errorf("watermill subscription failed: %w", err)
	}

	subCtx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.cancelFunc = append(w.cancelFunc, cancel)
	w.mu.Unlock()

	// Consumer thread loop
	go func() {
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return // Channel closed (likely EventBus shutting down)
				}

				// Retrieve tracing context from incoming message
				msgCtx := msg.Context()

				// Execute module's generic callback
				if err := handler(msgCtx, msg.Payload); err != nil {
					// Watermill relies on explicit Ack/Nack
					msg.Nack()
					w.logger.Error("handler error, message nacked", err, nil)
				} else {
					msg.Ack() // Standard acknowledgment
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
	for _, cancel := range w.cancelFunc {
		cancel()
	}
	w.cancelFunc = nil

	// Shut down the pub/sub engine
	if err := w.pubSub.Close(); err != nil {
		return fmt.Errorf("failed to close watermill: %w", err)
	}
	return nil
}

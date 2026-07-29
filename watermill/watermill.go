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
	wg        sync.WaitGroup
	nextSubID uint64
	cancels   map[uint64]context.CancelFunc
}

// NewEventBus creates a configured in-memory GoChannel PubSub.
func NewEventBus(bufferSize int64, persistent bool, debug bool) *EventBus {
	logger := watermill.NewStdLogger(debug, debug)

	pubSub := gochannel.NewGoChannel(
		gochannel.Config{
			OutputChannelBuffer: bufferSize,
			Persistent:          persistent,
		},
		logger,
	)

	return &EventBus{
		pubSub:  pubSub,
		logger:  logger,
		cancels: make(map[uint64]context.CancelFunc),
	}
}

// Publish generates a Watermill-compatible message, propagates context, and sends it.
func (w *EventBus) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	msg := message.NewMessage(watermill.NewUUID(), payload)
	msg.SetContext(ctx)

	if err := w.pubSub.Publish(topic, msg); err != nil {
		return fmt.Errorf("watermill publish failed: %w", err)
	}
	return nil
}

// Subscribe listens to a topic and handles messages in the background.
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

	subCtx, cancel := context.WithCancel(ctx)

	w.mu.Lock()
	subID := w.nextSubID
	w.nextSubID++
	w.cancels[subID] = cancel
	w.mu.Unlock()

	w.wg.Add(1)
	go func() {
		defer func() {
			cancel()
			w.mu.Lock()
			delete(w.cancels, subID)
			w.mu.Unlock()
			w.wg.Done()
		}()

		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-messages:
				if !ok {
					return
				}

				msgCtx := msg.Context()
				if err := handler(msgCtx, msg.Payload); err != nil {
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

// Close cancels all active subscriptions, waits for running handlers to exit,
// and shuts down the underlying GoChannel.
func (w *EventBus) Close(ctx context.Context) error {
	// Snapshot and clear cancels under lock
	w.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(w.cancels))
	for _, cancel := range w.cancels {
		cancels = append(cancels, cancel)
	}
	w.cancels = make(map[uint64]context.CancelFunc)
	w.mu.Unlock()

	// Signal all loops to stop
	for _, cancel := range cancels {
		cancel()
	}

	// Close channel to unblock any receivers sitting on <-messages
	if err := w.pubSub.Close(); err != nil {
		return fmt.Errorf("failed to close watermill pubsub: %w", err)
	}

	// Wait for background worker goroutines to finish with context timeout support
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("watermill eventbus close timed out: %w", ctx.Err())
	}
}

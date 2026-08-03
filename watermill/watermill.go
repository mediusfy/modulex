package watermill

import (
	"context"
	"fmt"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/workerpool"
)

// EventBus implements modulex.EventBus using Watermill's Channel.
type EventBus struct {
	pubSub *gochannel.GoChannel
	logger watermill.LoggerAdapter

	mu        sync.Mutex
	wg        sync.WaitGroup
	closeOnce sync.Once
	nextSubID uint64
	subs      map[uint64]subscription
}

type subscription struct {
	cancel    context.CancelFunc
	processor *workerpool.Processor
}

// SubscribeOptions enables bounded concurrent handler processing. The zero
// value is not valid; use Subscribe for the existing sequential behavior.
type SubscribeOptions struct {
	Workers       int
	QueueCapacity int
}

// NewEventBus creates a configured in-memory Channel PubSub.
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
		pubSub: pubSub,
		logger: logger,
		subs:   make(map[uint64]subscription),
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
	return w.subscribe(ctx, topic, handler, nil)
}

// SubscribeWithOptions subscribes with an opt-in bounded processor. Handler
// errors retain Watermill's existing acknowledge-and-log policy. Messages are
// acknowledged only after the handler returns. Processing order is not
// guaranteed when Workers is greater than one.
func (w *EventBus) SubscribeWithOptions(ctx context.Context, topic string, handler modulex.EventHandler, options SubscribeOptions) error {
	if handler == nil {
		return fmt.Errorf("watermill subscription failed: handler must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	processor, err := workerpool.New(workerpool.Options{
		Workers:       options.Workers,
		QueueCapacity: options.QueueCapacity,
	})
	if err != nil {
		return fmt.Errorf("watermill subscription failed: invalid worker options: %w", err)
	}
	if err := w.subscribe(ctx, topic, handler, processor); err != nil {
		_ = processor.Close(context.Background())
		return err
	}
	return nil
}

func (w *EventBus) subscribe(ctx context.Context, topic string, handler modulex.EventHandler, processor *workerpool.Processor) error {
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

	// Detach from the caller's transient context so the subscription remains
	// active until EventBus.Close() is called.
	subCtx, cancel := context.WithCancel(context.Background())

	w.mu.Lock()
	subID := w.nextSubID
	w.nextSubID++
	w.subs[subID] = subscription{cancel: cancel, processor: processor}
	w.mu.Unlock()

	w.wg.Add(1)
	go func() {
		defer func() {
			cancel()
			w.mu.Lock()
			delete(w.subs, subID)
			w.mu.Unlock()
			if processor != nil {
				_ = processor.Close(context.Background())
			}
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

				if processor == nil {
					msgCtx := msg.Context()
					if err := handler(msgCtx, msg.Payload); err != nil {
						w.logger.Error("handler error, message acknowledged to prevent redelivery", err, nil)
					}
					msg.Ack()
					continue
				}

				_, err := processor.Submit(msg.Context(), func(taskCtx context.Context) error {
					err := handler(taskCtx, msg.Payload)
					if err != nil {
						w.logger.Error("handler error, message acknowledged to prevent redelivery", err, nil)
					}
					msg.Ack()
					return err
				})
				if err != nil {
					w.logger.Error("worker pool rejected message, nacking for redelivery", err, nil)
					msg.Nack()
				}
			}
		}
	}()

	return nil
}

// Close cancels all active subscriptions, waits for running handlers to exit,
// and shuts down the underlying Channel.
func (w *EventBus) Close(ctx context.Context) error {
	var closeErr error

	w.closeOnce.Do(func() {
		// Snapshot and clear cancels under lock
		w.mu.Lock()
		cancels := make([]context.CancelFunc, 0, len(w.subs))
		for _, sub := range w.subs {
			cancels = append(cancels, sub.cancel)
		}
		w.subs = make(map[uint64]subscription)
		w.mu.Unlock()

		// Signal all loops to stop
		for _, cancel := range cancels {
			cancel()
		}

		// Close channel to unblock any receivers sitting on <-messages
		if err := w.pubSub.Close(); err != nil {
			closeErr = fmt.Errorf("failed to close watermill pubsub: %w", err)
		}
	})

	if closeErr != nil {
		return closeErr
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

var (
	_ modulex.EventBus   = (*EventBus)(nil)
	_ modulex.Publisher  = (*EventBus)(nil)
	_ modulex.Subscriber = (*EventBus)(nil)
)

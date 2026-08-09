package watermill

import (
	"context"
	"fmt"
	"sync"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/gochannel"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/internal/handlerpanic"
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
//
// ctx governs the subscription's lifetime as well as this call: cancelling
// ctx stops the subscription and releases its goroutine, exactly like
// calling EventBus.Close would for it. This means a bounded or per-call
// context (e.g. one scoped only to a module's Init phase, cancelled via a
// deferred cancel() shortly after Subscribe returns) will silently end the
// subscription early. Pass a context whose lifetime you intend the
// subscription to share — typically the same long-lived context used for
// the surrounding Manager's InitModules/StartModules call, or
// context.Background() paired with relying on EventBus.Close() alone to
// stop it.
func (w *EventBus) Subscribe(ctx context.Context, topic string, handler modulex.EventHandler) error {
	return w.subscribe(ctx, topic, handler, nil)
}

// SubscribeWithOptions subscribes with an opt-in bounded processor. Handler
// errors retain Watermill's existing acknowledge-and-log policy. Messages are
// acknowledged only after the handler returns. Processing order is not
// guaranteed when Workers is greater than one. See Subscribe's doc comment
// for how ctx governs this subscription's lifetime, including after this
// call returns.
func (w *EventBus) SubscribeWithOptions(ctx context.Context, topic string, handler modulex.EventHandler, options workerpool.Options) error {
	if handler == nil {
		return fmt.Errorf("watermill subscription failed: handler must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	processor, err := workerpool.New(options)
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

	// Derive the subscription context from the caller so cancellation
	// propagates correctly through the subscription lifecycle. The stored
	// cancel func is still invoked by Close() to speed up shutdown.
	subCtx, cancel := context.WithCancel(ctx)

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
					err := handlerpanic.Recover(func() error { return handler(msgCtx, msg.Payload) })
					if err != nil {
						w.logger.Error("handler error, message acknowledged to prevent redelivery", err, nil)
					}
					msg.Ack()
					continue
				}

				// subCtx, not msg.Context(), is submitted here so that a
				// Submit call blocked waiting for pool capacity is released
				// by EventBus.Close() (which cancels subCtx): msg.Context()
				// is derived from gochannel's own subscription context, not
				// from subCtx, and may never be cancelled by Close().
				_, err := processor.Submit(subCtx, func(context.Context) (herr error) {
					// This defer guarantees msg.Ack() always runs, even if
					// handler panics: workerpool.runTask's own recover
					// happens after this closure has already unwound, which
					// would otherwise leave msg permanently unresolved. The
					// panic is re-thrown after acking so workerpool still
					// records it as a failed task via its own recovery.
					defer func() {
						if p := recover(); p != nil {
							msg.Ack()
							panic(p)
						}
						if herr != nil {
							w.logger.Error("handler error, message acknowledged to prevent redelivery", herr, nil)
						}
						msg.Ack()
					}()
					return handler(msg.Context(), msg.Payload)
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

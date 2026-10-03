// Package busshutdown provides the shared "wait for subscription loops to
// exit, bounded by ctx" tail every EventBus adapter's Close(ctx) method
// (nats, rabbitmq, watermill) ends with, after it has already marked itself
// closed, canceled its subscriptions, and performed its own
// unsubscribe/disconnect bookkeeping. Before this package existed, each
// adapter reimplemented the same select-on-done-vs-ctx.Done() tail
// independently; a fix to this pattern (e.g. the handle-finish/map-delete
// ordering race supervised tasks once had) could land in one adapter's
// Close and be missed in the others.
package busshutdown

import (
	"context"
	"fmt"
	"sync"
)

// Wait waits for done to close, or ctx to be done, whichever comes first.
// name identifies the adapter in the returned timeout error (e.g.
// "rabbitmq", "jetstream durable consumer").
func Wait(ctx context.Context, done <-chan struct{}, name string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%s close timed out: %w", name, ctx.Err())
	}
}

// WaitGroup waits for wg to finish, or ctx to be done, whichever comes
// first — for an adapter that tracks its running subscription-loop
// goroutines with a *sync.WaitGroup rather than an already-existing
// "stopped" channel (see Wait for that case). name identifies the adapter
// in the returned timeout error.
func WaitGroup(ctx context.Context, wg *sync.WaitGroup, name string) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	return Wait(ctx, done, name)
}

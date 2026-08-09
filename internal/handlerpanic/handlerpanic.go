// Package handlerpanic provides a single, shared recover-and-convert helper
// for the EventBus adapters (nats, rabbitmq, watermill) so a panicking
// modulex.EventHandler cannot crash the process. Before this package
// existed, each adapter reimplemented its own recover block independently,
// with subtly different conventions (nats.EventBus.Subscribe logged the
// panic and swallowed it; rabbitmq.EventBus and watermill.EventBus each
// converted it to an error via a separately-worded, separately-maintained
// closure). Centralizing the conversion here means every adapter's handler
// panic becomes the same kind of error, wrapped in the same message shape,
// with one place to change that convention in the future.
package handlerpanic

import "fmt"

// Recover invokes call and converts any panic inside it into a non-nil
// error, so a caller can treat "the handler panicked" the same way it
// treats "the handler returned an error" — logging it, nacking a message,
// or whatever else its own error path already does. If call returns an
// error without panicking, that error is returned unchanged. If call
// neither panics nor errors, Recover returns nil.
func Recover(call func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	return call()
}

// Package workerpool provides bounded, lifecycle-aware execution for work
// submitted by message adapters and other optional capabilities.
package workerpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	// ErrClosed indicates that the processor is no longer accepting work.
	ErrClosed = errors.New("worker pool is closed")
	// ErrPanic indicates that a task panicked. The original panic value is
	// retained in the wrapped error.
	ErrPanic = errors.New("worker pool task panicked")
)

// Task is one unit of work. The task context is the context supplied to
// Submit; callers should observe its cancellation where appropriate.
type Task func(context.Context) error

// Handle represents accepted work. A handle must be awaited by callers that
// need to preserve message acknowledgement semantics.
type Handle struct {
	done chan error
}

// Wait blocks until the task finishes or ctx is cancelled. Cancelling ctx does
// not cancel the task itself; task cancellation is controlled by the context
// passed to Submit.
func (h *Handle) Wait(ctx context.Context) error {
	select {
	case err := <-h.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Options configures a Processor. Workers must be positive. QueueCapacity is
// the number of tasks allowed to wait behind active workers.
type Options struct {
	Workers       int
	QueueCapacity int
}

// Stats is a point-in-time processor snapshot.
type Stats struct {
	Workers   int
	Queued    int
	Running   int
	Accepted  uint64
	Completed uint64
	Failed    uint64
	Rejected  uint64
}

type task struct {
	ctx    context.Context
	fn     Task
	result *Handle
}

// Processor executes accepted tasks with a fixed number of workers and a
// bounded waiting queue. Close drains accepted tasks before returning.
type Processor struct {
	workers int
	tasks   chan task

	mu     sync.RWMutex
	closed bool
	wg     sync.WaitGroup

	queued    atomic.Int64
	running   atomic.Int64
	accepted  atomic.Uint64
	completed atomic.Uint64
	failed    atomic.Uint64
	rejected  atomic.Uint64
}

// New creates a bounded processor and starts its workers.
func New(options Options) (*Processor, error) {
	if options.Workers <= 0 {
		return nil, fmt.Errorf("workers must be positive: got %d", options.Workers)
	}
	if options.QueueCapacity < 0 {
		return nil, fmt.Errorf("queue capacity must not be negative: got %d", options.QueueCapacity)
	}

	p := &Processor{
		workers: options.Workers,
		tasks:   make(chan task, options.QueueCapacity),
	}
	p.wg.Add(options.Workers)
	for i := 0; i < options.Workers; i++ {
		go p.worker()
	}
	return p, nil
}

// Submit waits for queue capacity, then accepts fn for execution. If ctx is
// cancelled while waiting for capacity, no work is accepted and ctx.Err() is
// returned. A successful submission returns a handle whose completion should
// be awaited before acknowledging a broker message.
func (p *Processor) Submit(ctx context.Context, fn Task) (*Handle, error) {
	if fn == nil {
		return nil, errors.New("task must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	h := &Handle{done: make(chan error, 1)}
	t := task{ctx: ctx, fn: fn, result: h}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		p.rejected.Add(1)
		return nil, ErrClosed
	}
	select {
	case p.tasks <- t:
		p.queued.Add(1)
		p.accepted.Add(1)
		p.mu.RUnlock()
		return h, nil
	case <-ctx.Done():
		p.mu.RUnlock()
		p.rejected.Add(1)
		return nil, ctx.Err()
	}
}

// Close stops accepting work, drains accepted tasks, and waits for workers to
// exit. If ctx expires first, Close returns its error while workers continue
// draining in the background; a later Close call can be used to wait again.
func (p *Processor) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.tasks)
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns a snapshot suitable for diagnostics and metrics export.
func (p *Processor) Stats() Stats {
	return Stats{
		Workers:   p.workers,
		Queued:    int(p.queued.Load()),
		Running:   int(p.running.Load()),
		Accepted:  p.accepted.Load(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Rejected:  p.rejected.Load(),
	}
}

func (p *Processor) worker() {
	defer p.wg.Done()
	for t := range p.tasks {
		p.queued.Add(-1)
		p.running.Add(1)
		err := runTask(t.ctx, t.fn)
		p.running.Add(-1)
		p.completed.Add(1)
		if err != nil {
			p.failed.Add(1)
		}
		t.result.done <- err
	}
}

func runTask(ctx context.Context, fn Task) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, value)
		}
	}()
	return fn(ctx)
}

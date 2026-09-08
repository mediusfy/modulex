// Package queue abstracts the durable task queue between the webhook
// receiver and the review worker (ADR-0035 "Fast ack, async work"). The
// production implementation is Cloud Tasks (durable retries; a transient
// worker failure re-runs rather than dropping the PR); tests and local
// development use Memory.
package queue

import (
	"context"
	"sync"

	"github.com/mediusfy/modulex/services/prreview/webhook"
)

// Enqueuer hands a review request to the worker tier.
type Enqueuer interface {
	Enqueue(ctx context.Context, req webhook.ReviewRequest) error
}

// Memory is an in-process Enqueuer that records requests (tests) and can
// optionally dispatch them synchronously to a handler (local development).
type Memory struct {
	mu       sync.Mutex
	tasks    []webhook.ReviewRequest
	Dispatch func(ctx context.Context, req webhook.ReviewRequest) error
}

func (m *Memory) Enqueue(ctx context.Context, req webhook.ReviewRequest) error {
	m.mu.Lock()
	m.tasks = append(m.tasks, req)
	dispatch := m.Dispatch
	m.mu.Unlock()
	if dispatch != nil {
		return dispatch(ctx, req)
	}
	return nil
}

// Tasks returns a copy of everything enqueued so far.
func (m *Memory) Tasks() []webhook.ReviewRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]webhook.ReviewRequest, len(m.tasks))
	copy(out, m.tasks)
	return out
}

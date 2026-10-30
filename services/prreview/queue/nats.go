package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mediusfy/modulex"
	natsadapter "github.com/mediusfy/modulex/nats"
	"github.com/nats-io/nats.go"

	"github.com/mediusfy/modulex/services/prreview/webhook"
)

// ReviewsSubject is the JetStream subject review tasks are published to and
// consumed from. It ends in a non-wildcard segment ("tasks") so a stream
// declared over the "prreview.reviews.>" wildcard also covers the
// dead-letter subject the durable consumer forms by suffixing it (see
// Consume).
const ReviewsSubject = "prreview.reviews.tasks"

// ReviewsStreamSubjects is the subject filter EnsureStream should declare
// for the stream backing ReviewsSubject.
var ReviewsStreamSubjects = []string{"prreview.reviews.>"}

// EnsureStream creates the named JetStream stream covering subjects if it
// does not already exist. Idempotent; safe to call on every process start
// (receiver and worker both do, since either may be the first to run
// against a fresh NATS deployment).
func EnsureStream(js nats.JetStreamContext, name string, subjects ...string) error {
	if _, err := js.StreamInfo(name); err != nil {
		if !errors.Is(err, nats.ErrStreamNotFound) {
			return fmt.Errorf("nats stream info %s: %w", name, err)
		}
		if _, err := js.AddStream(&nats.StreamConfig{Name: name, Subjects: subjects}); err != nil {
			return fmt.Errorf("nats add stream %s: %w", name, err)
		}
	}
	return nil
}

// NATS is the production Enqueuer backed by NATS JetStream (ADR-0035 "Fast
// ack, async work"; self-hosted k3s deployments that have traded
// Firestore/Cloud Tasks for a NATS cluster they already run). One shared
// subject for all installations — per-PR serialization is the store
// lease's job, not per-PR subjects. Bus is typically the same
// *modulex/nats.JetStreamEventBus the worker's Consume subscribes durably
// on; see Consume for the consumption side and its retry/backoff policy.
type NATS struct {
	Bus *natsadapter.JetStreamEventBus
	// Subject is the JetStream subject tasks are published to. Defaults to
	// ReviewsSubject if empty.
	Subject string
}

func (n *NATS) subject() string {
	if n.Subject != "" {
		return n.Subject
	}
	return ReviewsSubject
}

func (n *NATS) Enqueue(ctx context.Context, req webhook.ReviewRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := n.Bus.Publish(ctx, n.subject(), body); err != nil {
		return fmt.Errorf("nats enqueue %s/%s#%d: %w", req.Owner, req.Repo, req.PRNumber, err)
	}
	return nil
}

// Consume subscribes durably to subject (ReviewsSubject in production) and
// calls handle for each delivered review task, acknowledging the message
// on success and asking JetStream to redeliver (with backoff) on failure.
// It returns once the subscription is established; handle runs on the
// adapter's own consume-loop goroutine until ctx is cancelled (see
// (*modulex/nats.JetStreamEventBus).SubscribeDurable).
//
// Retry policy: bus should be constructed with
// natsadapter.WithDurableMaxDeliver(8) and
// natsadapter.WithDurableAckWait(10*time.Second) to approximate this
// service's Cloud Tasks queue (infra/prreview's google_cloud_tasks_queue
// "reviews": max_attempts 8, min_backoff 10s, max_backoff 600s). The
// approximation is not exact: the adapter's redelivery backoff doubles
// from WithDurableAckWait and caps at a fixed 5 minutes (not the Cloud
// Tasks queue's 600s), and that same AckWait value also bounds how long
// JetStream waits before considering an un-acked delivery lost and
// redelivering it — a redelivery that can race a still-legitimately-
// running review. Both are safe here because the store's per-PR lease
// (store.AcquireDecision), not the queue's delivery timing, is what
// actually serializes concurrent processing of one PR: a spurious
// redelivery while a review is genuinely in flight just re-enters Handle,
// which sees the lease held and returns store.ActionSkip.
//
// A malformed task body (one that will never become well-formed) is
// terminated via modulex.DeadLetter rather than redelivered, matching
// cmd/worker's Cloud-Tasks-push handler, which answers a malformed body
// with 400 (no retry) for the same reason.
func Consume(ctx context.Context, bus *natsadapter.JetStreamEventBus, subject, consumerName string, handle func(context.Context, webhook.ReviewRequest) error) error {
	return bus.SubscribeDurable(ctx, subject, func(ctx context.Context, dm modulex.DurableMessage) modulex.AckDecision {
		var req webhook.ReviewRequest
		if err := json.Unmarshal(dm.Payload, &req); err != nil {
			return modulex.DeadLetter
		}
		if err := handle(ctx, req); err != nil {
			return modulex.Nack
		}
		return modulex.Ack
	}, modulex.WithConsumerName(consumerName))
}

var _ Enqueuer = (*NATS)(nil)

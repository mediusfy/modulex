package nats

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
)

// TestIsTerminalFetchError locks in the fix for M3: durableConsumeLoop must
// recognize a connection-closed or consumer-gone error as terminal (stop
// retrying immediately) rather than retrying forever, while a generic
// transient error must NOT be treated as terminal (it should instead go
// through the backoff-and-retry path).
func TestIsTerminalFetchError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"connection closed is terminal", nats.ErrConnectionClosed, true},
		{"consumer not found is terminal", nats.ErrConsumerNotFound, true},
		{"consumer deleted is terminal", nats.ErrConsumerDeleted, true},
		{"bad subscription is terminal", nats.ErrBadSubscription, true},
		{"subscription closed is terminal", nats.ErrSubscriptionClosed, true},
		{"no responders is not terminal (transient leader election must not end the subscription)", nats.ErrNoResponders, false},
		{"wrapped connection closed is terminal", fmt.Errorf("fetch failed: %w", nats.ErrConnectionClosed), true},
		{"literal consumer not found string is terminal", errors.New("consumer not found"), true},
		{"literal consumer deleted string is terminal", errors.New("consumer deleted"), true},
		{"generic transient error is not terminal", errors.New("temporary network blip"), false},
		{"nats timeout is not terminal (handled separately)", nats.ErrTimeout, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isTerminalFetchError(tt.err))
		})
	}
}

// TestNextDurableFetchBackoff locks in the fix for M3: the retry delay after
// a non-terminal, non-timeout Fetch error must grow (so the loop does not
// hot-spin retrying as fast as possible) and must be capped at
// maxDurableFetchBackoff (so it does not grow unboundedly either).
func TestNextDurableFetchBackoff(t *testing.T) {
	backoff := initialDurableFetchBackoff
	seen := []time.Duration{backoff}
	for i := 0; i < 10; i++ {
		backoff = nextDurableFetchBackoff(backoff)
		seen = append(seen, backoff)
	}

	for i := 1; i < len(seen); i++ {
		assert.GreaterOrEqual(t, seen[i], seen[i-1], "backoff must never shrink")
	}
	assert.Equal(t, maxDurableFetchBackoff, seen[len(seen)-1], "backoff must eventually cap at maxDurableFetchBackoff")
	assert.LessOrEqual(t, seen[1], maxDurableFetchBackoff)
}

// TestJetStreamEventBus_NakDelay locks in the fix for M4: the delay passed
// to msg.NakWithDelay must grow with the message's delivery count (starting
// at the configured AckWait) instead of nacking immediately, and must be
// capped at maxDurableNakDelay.
func TestJetStreamEventBus_NakDelay(t *testing.T) {
	j := &JetStreamEventBus{durableAckWait: 1 * time.Second}

	assert.Equal(t, 1*time.Second, j.nakDelay(0), "deliveryCount < 1 must be treated as 1")
	assert.Equal(t, 1*time.Second, j.nakDelay(1))
	assert.Equal(t, 2*time.Second, j.nakDelay(2))
	assert.Equal(t, 4*time.Second, j.nakDelay(3))
	assert.Equal(t, 8*time.Second, j.nakDelay(4))

	// A very large delivery count must still cap at maxDurableNakDelay,
	// rather than overflowing time.Duration via repeated doubling.
	assert.Equal(t, maxDurableNakDelay, j.nakDelay(1000))
}

type stubConsumerInfoJS struct {
	nats.JetStreamContext
	err error
}

func (s stubConsumerInfoJS) ConsumerInfo(_ string, _ string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &nats.ConsumerInfo{}, nil
}

// TestJetStreamEventBus_IsDurableConsumerGone verifies how durableConsumeLoop
// disambiguates nats.ErrNoResponders from sub.Fetch: only an explicit
// ErrConsumerNotFound or ErrStreamNotFound from ConsumerInfo is treated as
// terminal (consumer deleted), whereas a live consumer or a transient lookup
// failure (such as another ErrNoResponders or timeout during a JetStream RAFT
// leader election) returns false so the consume loop backs off and retries.
func TestJetStreamEventBus_IsDurableConsumerGone(t *testing.T) {
	tests := []struct {
		name string
		js   nats.JetStreamContext
		str  string
		want bool
	}{
		{"nil JetStreamContext is not terminal", nil, "EVENTS", false},
		{"empty stream is not terminal", stubConsumerInfoJS{err: nats.ErrConsumerNotFound}, "", false},
		{"consumer still exists is not terminal", stubConsumerInfoJS{err: nil}, "EVENTS", false},
		{"consumer not found is terminal", stubConsumerInfoJS{err: nats.ErrConsumerNotFound}, "EVENTS", true},
		{"stream not found is terminal", stubConsumerInfoJS{err: nats.ErrStreamNotFound}, "EVENTS", true},
		{"no responders during leader election is not terminal", stubConsumerInfoJS{err: nats.ErrNoResponders}, "EVENTS", false},
		{"timeout during leader election is not terminal", stubConsumerInfoJS{err: nats.ErrTimeout}, "EVENTS", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JetStreamEventBus{
				js:               tt.js,
				durableFetchWait: 100 * time.Millisecond,
			}
			assert.Equal(t, tt.want, j.isDurableConsumerGone(context.Background(), tt.str, "c1"))
		})
	}
}

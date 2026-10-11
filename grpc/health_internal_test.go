package grpc

import (
	"context"
	"testing"
	"time"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// internalStaticChecker mirrors health_test.go's staticChecker, duplicated
// here (rather than exported) since this file needs package grpc itself to
// reach the unexported defaultCheckTimeout var.
type internalStaticChecker struct {
	health map[string]func(context.Context) error
}

func (c *internalStaticChecker) HealthChecks() map[string]func(context.Context) error {
	return c.health
}

func (c *internalStaticChecker) ReadinessChecks() map[string]func(context.Context) error {
	return nil
}

// TestEvaluateChecksBoundsAHungCheck proves evaluateChecks never blocks
// longer than defaultCheckTimeout even when the caller's own ctx carries no
// deadline at all (Watch's stream.Context() typically doesn't) and the
// check itself never returns on its own — the gap this test guards against
// regressing.
func TestEvaluateChecksBoundsAHungCheck(t *testing.T) {
	prev := defaultCheckTimeout
	defaultCheckTimeout = 10 * time.Millisecond
	t.Cleanup(func() { defaultCheckTimeout = prev })

	started := make(chan struct{})
	checker := &internalStaticChecker{health: map[string]func(context.Context) error{
		"hung": func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}}
	server := NewHealthServer(checker)

	done := make(chan *healthpb.HealthCheckResponse, 1)
	go func() {
		resp, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Error(err)
			return
		}
		done <- resp
	}()

	<-started
	select {
	case resp := <-done:
		if resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
			t.Errorf("Status = %v, want NOT_SERVING", resp.GetStatus())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Check did not return within 2s of a hung check and a 10ms timeout — evaluateChecks is not bounding it")
	}
}

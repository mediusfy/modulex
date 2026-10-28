package grpc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	grpcadapter "github.com/mediusfy/modulex/grpc"
)

func TestHealthServerCheckReportsLiveness(t *testing.T) {
	tests := []struct {
		name       string
		health     map[string]func(context.Context) error
		wantStatus healthpb.HealthCheckResponse_ServingStatus
	}{
		{
			name:       "all liveness checks pass",
			health:     map[string]func(context.Context) error{"db": func(context.Context) error { return nil }},
			wantStatus: healthpb.HealthCheckResponse_SERVING,
		},
		{
			name:       "no checks registered means healthy",
			health:     map[string]func(context.Context) error{},
			wantStatus: healthpb.HealthCheckResponse_SERVING,
		},
		{
			name: "a failing liveness check reports NOT_SERVING",
			health: map[string]func(context.Context) error{
				"db": func(context.Context) error { return nil },
				"mq": func(context.Context) error { return errors.New("connection refused") },
			},
			wantStatus: healthpb.HealthCheckResponse_NOT_SERVING,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &staticChecker{health: tt.health}
			server := grpcadapter.NewHealthServer(checker)

			resp, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{Service: ""})
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, resp.GetStatus())
		})
	}
}

func TestHealthServerCheckReportsReadiness(t *testing.T) {
	tests := []struct {
		name       string
		readiness  map[string]func(context.Context) error
		wantStatus healthpb.HealthCheckResponse_ServingStatus
	}{
		{
			name:       "readiness checks pass",
			readiness:  map[string]func(context.Context) error{"cache-warm": func(context.Context) error { return nil }},
			wantStatus: healthpb.HealthCheckResponse_SERVING,
		},
		{
			name: "a failing readiness check reports NOT_SERVING",
			readiness: map[string]func(context.Context) error{
				"cache-warm": func(context.Context) error { return errors.New("cache not primed") },
			},
			wantStatus: healthpb.HealthCheckResponse_NOT_SERVING,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := &staticChecker{readiness: tt.readiness}
			server := grpcadapter.NewHealthServer(checker)

			resp, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{Service: grpcadapter.ReadinessService})
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, resp.GetStatus())
		})
	}
}

// TestHealthServerCheckRunsChecksConcurrently proves the fix for L5:
// multiple registered checks are evaluated concurrently, not serially. A
// serial loop over numChecks checks, each taking checkDelay, would take at
// least numChecks*checkDelay; this asserts Check returns in well under
// that, close to a single checkDelay.
func TestHealthServerCheckRunsChecksConcurrently(t *testing.T) {
	const (
		numChecks  = 5
		checkDelay = 150 * time.Millisecond
	)

	checks := make(map[string]func(context.Context) error, numChecks)
	for i := 0; i < numChecks; i++ {
		checks[fmt.Sprintf("check-%d", i)] = func(ctx context.Context) error {
			select {
			case <-time.After(checkDelay):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	checker := &staticChecker{health: checks}
	server := grpcadapter.NewHealthServer(checker)

	start := time.Now()
	resp, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())
	// Serial evaluation of numChecks checks would take at least
	// numChecks*checkDelay (750ms). Concurrent evaluation takes roughly one
	// checkDelay; allow generous headroom for scheduling jitter while still
	// catching a regression to serial evaluation.
	assert.Less(t, elapsed, 3*checkDelay,
		"Check took %s for %d checks of %s each -- checks appear to run serially, not concurrently",
		elapsed, numChecks, checkDelay)
}

func TestHealthServerCheckUnknownServiceReturnsNotFound(t *testing.T) {
	server := grpcadapter.NewHealthServer(&staticChecker{})
	_, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "unknown.Service"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestHealthServerReflectsLiveManagerState confirms the reported status
// changes when the underlying check's result changes, proving Check
// evaluates the checker's real state on every call rather than caching a
// snapshot from construction time.
func TestHealthServerReflectsLiveManagerState(t *testing.T) {
	healthy := true
	checker := &staticChecker{health: map[string]func(context.Context) error{
		"toggle": func(context.Context) error {
			if healthy {
				return nil
			}
			return errors.New("now unhealthy")
		},
	}}
	server := grpcadapter.NewHealthServer(checker)

	resp, err := server.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())

	healthy = false

	resp, err = server.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, resp.GetStatus())
}

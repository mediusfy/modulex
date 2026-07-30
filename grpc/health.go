package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// ReadinessService is the health-check "service" name (per the standard gRPC
// health-checking protocol's HealthCheckRequest.Service field) that reports a
// HealthChecker's readiness checks instead of its liveness checks. The empty
// string reports liveness, matching the protocol's convention that an empty
// service name means "the server's overall health."
const ReadinessService = "readiness"

// defaultWatchInterval bounds how often Watch re-evaluates checks while a
// client is streaming.
const defaultWatchInterval = 5 * time.Second

// HealthChecker is the subset of modulex.Registry (and modulex.Manager, which
// implements the full Registry) that HealthServer needs to answer
// health-check requests from real, currently-registered checks rather than a
// hardcoded status.
type HealthChecker interface {
	// HealthChecks returns the currently registered liveness checks.
	HealthChecks() map[string]func(context.Context) error
	// ReadinessChecks returns the currently registered readiness checks.
	ReadinessChecks() map[string]func(context.Context) error
}

// HealthServer implements grpc_health_v1.HealthServer by evaluating a
// HealthChecker's registered health and readiness checks on every call,
// instead of reporting a hardcoded SERVING status. Register it with:
//
//	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(mgr))
//
// where mgr is a *modulex.Manager (or anything else implementing
// HealthChecker).
//
// # Service name convention
//
//   - "" (empty) or any name not recognized below evaluates the registered
//     liveness checks (modulex.Registry.RegisterHealthCheck).
//   - ReadinessService ("readiness") evaluates the registered readiness
//     checks (modulex.Registry.RegisterReadinessCheck).
//
// Unlike the standard library's google.golang.org/grpc/health.Server (which
// requires a caller to push status updates via SetServingStatus),
// HealthServer never goes stale: every Check and Watch tick re-runs the
// checker's actual check functions, so the reported status always reflects
// the Manager's real state at the moment of the call.
type HealthServer struct {
	healthpb.UnimplementedHealthServer

	checker       HealthChecker
	watchInterval time.Duration
}

// HealthServerOption configures a HealthServer during construction.
type HealthServerOption func(*HealthServer)

// WithWatchInterval overrides how often Watch re-evaluates checks while a
// client is streaming. The default is 5 seconds.
func WithWatchInterval(d time.Duration) HealthServerOption {
	return func(h *HealthServer) {
		h.watchInterval = d
	}
}

// NewHealthServer creates a HealthServer backed by checker.
func NewHealthServer(checker HealthChecker, opts ...HealthServerOption) *HealthServer {
	h := &HealthServer{checker: checker, watchInterval: defaultWatchInterval}
	for _, opt := range opts {
		opt(h)
	}
	if h.watchInterval <= 0 {
		h.watchInterval = defaultWatchInterval
	}
	return h
}

// Check implements grpc_health_v1.HealthServer.
func (h *HealthServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	checks, err := h.checksFor(req.GetService())
	if err != nil {
		return nil, err
	}
	return &healthpb.HealthCheckResponse{Status: servingStatus(evaluateChecks(ctx, checks))}, nil
}

// Watch implements grpc_health_v1.HealthServer. It sends the current status
// immediately, then re-evaluates on an interval (see WithWatchInterval),
// sending a new message only when the status changes, until the stream's
// context is done.
func (h *HealthServer) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	checks, err := h.checksFor(req.GetService())
	if err != nil {
		return err
	}

	ticker := time.NewTicker(h.watchInterval)
	defer ticker.Stop()

	lastStatus := healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	for {
		current := servingStatus(evaluateChecks(stream.Context(), checks))
		if current != lastStatus {
			if err := stream.Send(&healthpb.HealthCheckResponse{Status: current}); err != nil {
				return err
			}
			lastStatus = current
		}

		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-ticker.C:
		}
	}
}

func (h *HealthServer) checksFor(service string) (map[string]func(context.Context) error, error) {
	switch service {
	case ReadinessService:
		return h.checker.ReadinessChecks(), nil
	case "":
		return h.checker.HealthChecks(), nil
	default:
		return nil, status.Errorf(codes.NotFound, "unknown service %q", service)
	}
}

// evaluateChecks runs every check and reports whether all of them passed. A
// nil check function or a non-nil error both count as a failure, mirroring
// httpx.runChecks' treatment of nil checks.
func evaluateChecks(ctx context.Context, checks map[string]func(context.Context) error) bool {
	for _, check := range checks {
		if check == nil {
			return false
		}
		if err := check(ctx); err != nil {
			return false
		}
	}
	return true
}

func servingStatus(healthy bool) healthpb.HealthCheckResponse_ServingStatus {
	if healthy {
		return healthpb.HealthCheckResponse_SERVING
	}
	return healthpb.HealthCheckResponse_NOT_SERVING
}

var _ healthpb.HealthServer = (*HealthServer)(nil)

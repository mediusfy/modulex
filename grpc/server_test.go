package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	grpcadapter "github.com/mediusfy/modulex/grpc"
)

// staticChecker is a minimal grpcadapter.HealthChecker for tests that do not
// need a full *modulex.Manager.
type staticChecker struct {
	health    map[string]func(context.Context) error
	readiness map[string]func(context.Context) error
}

func (c *staticChecker) HealthChecks() map[string]func(context.Context) error    { return c.health }
func (c *staticChecker) ReadinessChecks() map[string]func(context.Context) error { return c.readiness }

// dialBufconn dials a *googlegrpc.ClientConn against an in-memory bufconn
// listener, using an insecure transport (this is a local, in-process test
// connection, never a real network) and no blocking dial semantics -
// grpc.NewClient does not connect eagerly, so the caller must exercise the
// connection (e.g. an RPC) to observe dial failures.
func dialBufconn(t *testing.T, lis *bufconn.Listener, opts ...googlegrpc.DialOption) *googlegrpc.ClientConn {
	t.Helper()
	dialOpts := append([]googlegrpc.DialOption{
		googlegrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		googlegrpc.WithTransportCredentials(insecure.NewCredentials()),
	}, opts...)
	conn, err := googlegrpc.NewClient("passthrough:///bufconn", dialOpts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestServerLifecycleStartCallGracefulStop(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)

	grpcServer := googlegrpc.NewServer()
	checker := &staticChecker{health: map[string]func(context.Context) error{
		"ok": func(context.Context) error { return nil },
	}}
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(checker))

	server, err := grpcadapter.NewServer(grpcServer, lis)
	require.NoError(t, err)

	require.NoError(t, server.Start(context.Background()))

	conn := dialBufconn(t, lis)
	client := healthpb.NewHealthClient(conn)

	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())

	require.NoError(t, server.Stop(context.Background()))

	// Stop is idempotent.
	require.NoError(t, server.Stop(context.Background()))
}

func TestServerGracefulShutdownWaitsForInFlightCall(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)

	release := make(chan struct{})
	callStarted := make(chan struct{})
	grpcServer := googlegrpc.NewServer()
	checker := &staticChecker{health: map[string]func(context.Context) error{
		"slow": func(context.Context) error {
			close(callStarted)
			<-release
			return nil
		},
	}}
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(checker))

	server, err := grpcadapter.NewServer(grpcServer, lis, grpcadapter.WithShutdownTimeout(5*time.Second))
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))

	conn := dialBufconn(t, lis)
	client := healthpb.NewHealthClient(conn)

	var callErr error
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, callErr = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	}()

	<-callStarted // the slow check is now blocking inside the RPC handler.

	stopDone := make(chan struct{})
	var stopErr error
	go func() {
		defer close(stopDone)
		stopErr = server.Stop(context.Background())
	}()

	// Stop must not return while the in-flight call is still blocked: give it
	// a moment to (incorrectly) return early before we release the call.
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the in-flight RPC completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	<-callDone
	require.NoError(t, callErr)

	<-stopDone
	require.NoError(t, stopErr)
}

// TestServerStopForcesShutdownAfterTimeout confirms Stop falls back to a hard
// stop when GracefulStop does not complete within the configured bound.
//
// The in-flight handler here blocks on a channel but — like any correctly
// written gRPC handler — also watches ctx.Done(), returning ctx.Err() once
// its RPC's context is canceled. This is the realistic case the fallback is
// designed for: Go cannot forcibly kill a running goroutine, so neither
// GracefulStop nor a hard Stop can unblock a handler that ignores context
// cancellation entirely (that would be a bug in the handler, not something
// any gRPC server implementation, Modulex's or otherwise, can bound). What
// Stop's forced path actually guarantees is that closing the listener and
// transport cancels every in-flight RPC's context, so a context-respecting
// handler unblocks promptly instead of waiting for the request to finish
// naturally.
func TestServerStopForcesShutdownAfterTimeout(t *testing.T) {
	lis := bufconn.Listen(1024 * 1024)

	callStarted := make(chan struct{})
	grpcServer := googlegrpc.NewServer()
	checker := &staticChecker{health: map[string]func(context.Context) error{
		"blocks-until-canceled": func(ctx context.Context) error {
			close(callStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	}}
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(checker))

	server, err := grpcadapter.NewServer(grpcServer, lis, grpcadapter.WithShutdownTimeout(50*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))

	conn := dialBufconn(t, lis)
	client := healthpb.NewHealthClient(conn)

	go func() {
		_, _ = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	}()
	<-callStarted

	start := time.Now()
	err = server.Stop(context.Background())
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 2*time.Second, "Stop should have forced shutdown near the configured timeout, not blocked indefinitely")
}

func TestNewServerRejectsNilArgs(t *testing.T) {
	lis := bufconn.Listen(1024)
	defer func() { _ = lis.Close() }()

	_, err := grpcadapter.NewServer(nil, lis)
	require.Error(t, err)

	_, err = grpcadapter.NewServer(googlegrpc.NewServer(), nil)
	require.Error(t, err)
}

func TestServerStartThenImmediateStop(t *testing.T) {
	lis := bufconn.Listen(1024)
	grpcServer := googlegrpc.NewServer()
	server, err := grpcadapter.NewServer(grpcServer, lis, grpcadapter.WithShutdownTimeout(time.Second))
	require.NoError(t, err)

	require.NoError(t, server.Start(context.Background()))
	require.NoError(t, server.Stop(context.Background()))
}

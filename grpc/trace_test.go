package grpc_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	googlegrpc "google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	grpcadapter "github.com/mediusfy/modulex/grpc"
)

// withTraceContextPropagator installs propagation.TraceContext{} as the
// global OpenTelemetry TextMapPropagator for the duration of the test and
// restores whatever was previously installed afterward.
//
// This mirrors a real requirement, not just a test fixture: otel.
// GetTextMapPropagator() defaults to a no-op composite propagator until a
// composition root calls otel.SetTextMapPropagator (typically once, at
// startup, alongside configuring the TracerProvider) — the same requirement
// the nats and rabbitmq adapters have for their header-based propagation.
// See docs/planning/grpc-adapter-guide.md.
func withTraceContextPropagator(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

// TestTraceContextPropagatedFromClientToServer confirms a trace context
// started on the client side is observable on the server side through
// TraceUnaryClientInterceptor / TraceUnaryServerInterceptor, using the health
// service (already vendored by this package) as the real RPC under test.
func TestTraceContextPropagatedFromClientToServer(t *testing.T) {
	withTraceContextPropagator(t)
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("grpc_test")

	var observed oteltrace.SpanContext
	captured := make(chan struct{})

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := googlegrpc.NewServer(
		googlegrpc.ChainUnaryInterceptor(
			grpcadapter.TraceUnaryServerInterceptor(),
			func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
				observed = oteltrace.SpanContextFromContext(ctx)
				close(captured)
				return handler(ctx, req)
			},
		),
	)
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(&staticChecker{
		health: map[string]func(context.Context) error{"ok": func(context.Context) error { return nil }},
	}))

	server, err := grpcadapter.NewServer(grpcServer, lis)
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	conn := dialBufconn(t, lis, googlegrpc.WithChainUnaryInterceptor(grpcadapter.TraceUnaryClientInterceptor()))
	client := healthpb.NewHealthClient(conn)

	ctx, span := tracer.Start(context.Background(), "client-span")
	clientSC := span.SpanContext()
	defer span.End()

	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)

	<-captured
	require.True(t, observed.IsValid(), "server did not observe a valid extracted span context")
	assert.Equal(t, clientSC.TraceID(), observed.TraceID())
	assert.Equal(t, clientSC.SpanID(), observed.SpanID())
}

// TestStreamTraceContextPropagatedFromClientToServer exercises the streaming
// variants using the health service's Watch RPC as the real streaming call
// under test.
func TestStreamTraceContextPropagatedFromClientToServer(t *testing.T) {
	withTraceContextPropagator(t)
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("grpc_test")

	var observed oteltrace.SpanContext
	captured := make(chan struct{})

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := googlegrpc.NewServer(
		googlegrpc.ChainStreamInterceptor(
			grpcadapter.TraceStreamServerInterceptor(),
			func(srv any, ss googlegrpc.ServerStream, info *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
				observed = oteltrace.SpanContextFromContext(ss.Context())
				close(captured)
				return handler(srv, ss)
			},
		),
	)
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(&staticChecker{
		health: map[string]func(context.Context) error{"ok": func(context.Context) error { return nil }},
	}))

	server, err := grpcadapter.NewServer(grpcServer, lis)
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	conn := dialBufconn(t, lis, googlegrpc.WithChainStreamInterceptor(grpcadapter.TraceStreamClientInterceptor()))
	client := healthpb.NewHealthClient(conn)

	ctx, span := tracer.Start(context.Background(), "client-span")
	clientSC := span.SpanContext()
	defer span.End()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	<-captured
	require.True(t, observed.IsValid(), "server did not observe a valid extracted span context")
	assert.Equal(t, clientSC.TraceID(), observed.TraceID())
	assert.Equal(t, clientSC.SpanID(), observed.SpanID())
}

func TestMetadataCarrierRoundTrip(t *testing.T) {
	// Sanity check that otel's own propagator round-trips through our
	// carrier without the bufconn/grpc machinery, isolating carrier bugs from
	// wire-level ones.
	withTraceContextPropagator(t)
	prop := otel.GetTextMapPropagator()

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("t").Start(context.Background(), "s")
	defer span.End()

	md := map[string][]string{}
	carrier := carrierFromMD(md)
	prop.Inject(ctx, carrier)

	extracted := prop.Extract(context.Background(), carrier)
	sc := oteltrace.SpanContextFromContext(extracted)
	assert.True(t, sc.IsValid())
	assert.Equal(t, span.SpanContext().TraceID(), sc.TraceID())
}

// carrierFromMD is a tiny standalone TextMapCarrier used only to validate
// propagation semantics independent of grpc/metadata, avoiding an internal
// test import of the unexported metadataCarrier type.
type carrierFromMD map[string][]string

func (c carrierFromMD) Get(key string) string {
	v := c[key]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
func (c carrierFromMD) Set(key, value string) { c[key] = []string{value} }
func (c carrierFromMD) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

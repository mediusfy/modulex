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

// startTraceTestServer starts an adapter server on a bufconn listener serving
// the health service behind serverOpt and returns the listener.
func startTraceTestServer(t *testing.T, serverOpt googlegrpc.ServerOption) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := googlegrpc.NewServer(serverOpt)
	healthpb.RegisterHealthServer(grpcServer, grpcadapter.NewHealthServer(&staticChecker{
		health: map[string]func(context.Context) error{"ok": func(context.Context) error { return nil }},
	}))

	server, err := grpcadapter.NewServer(grpcServer, lis)
	require.NoError(t, err)
	require.NoError(t, server.Start(context.Background()))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	return lis
}

// TestTraceContextPropagatedFromClientToServer confirms a trace context
// started on the client side is observable on the server side through the
// trace interceptors, using the health service (already vendored by this
// package) as the real RPC under test: Check for unary, Watch for streaming.
func TestTraceContextPropagatedFromClientToServer(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the server option (capturing the server-side span
		// context into observed and closing captured) and the client dial option.
		setup func(observed *oteltrace.SpanContext, captured chan struct{}) (googlegrpc.ServerOption, googlegrpc.DialOption)
		call  func(ctx context.Context, t *testing.T, client healthpb.HealthClient)
	}{
		{
			name: "unary",
			setup: func(observed *oteltrace.SpanContext, captured chan struct{}) (googlegrpc.ServerOption, googlegrpc.DialOption) {
				return googlegrpc.ChainUnaryInterceptor(
						grpcadapter.TraceUnaryServerInterceptor(),
						func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
							*observed = oteltrace.SpanContextFromContext(ctx)
							close(captured)
							return handler(ctx, req)
						},
					),
					googlegrpc.WithChainUnaryInterceptor(grpcadapter.TraceUnaryClientInterceptor())
			},
			call: func(ctx context.Context, t *testing.T, client healthpb.HealthClient) {
				_, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
				require.NoError(t, err)
			},
		},
		{
			name: "stream",
			setup: func(observed *oteltrace.SpanContext, captured chan struct{}) (googlegrpc.ServerOption, googlegrpc.DialOption) {
				return googlegrpc.ChainStreamInterceptor(
						grpcadapter.TraceStreamServerInterceptor(),
						func(srv any, ss googlegrpc.ServerStream, info *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
							*observed = oteltrace.SpanContextFromContext(ss.Context())
							close(captured)
							return handler(srv, ss)
						},
					),
					googlegrpc.WithChainStreamInterceptor(grpcadapter.TraceStreamClientInterceptor())
			},
			call: func(ctx context.Context, t *testing.T, client healthpb.HealthClient) {
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				stream, err := client.Watch(ctx, &healthpb.HealthCheckRequest{})
				require.NoError(t, err)
				_, err = stream.Recv()
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withTraceContextPropagator(t)
			tp := sdktrace.NewTracerProvider()
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
			tracer := tp.Tracer("grpc_test")

			var observed oteltrace.SpanContext
			captured := make(chan struct{})
			serverOpt, dialOpt := tt.setup(&observed, captured)

			lis := startTraceTestServer(t, serverOpt)
			client := healthpb.NewHealthClient(dialBufconn(t, lis, dialOpt))

			ctx, span := tracer.Start(context.Background(), "client-span")
			clientSC := span.SpanContext()
			defer span.End()

			tt.call(ctx, t, client)

			<-captured
			require.True(t, observed.IsValid(), "server did not observe a valid extracted span context")
			assert.Equal(t, clientSC.TraceID(), observed.TraceID())
			assert.Equal(t, clientSC.SpanID(), observed.SpanID())
		})
	}
}

func TestMetadataCarrierRoundTrip(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "trace context round trips"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
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

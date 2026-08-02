package grpc

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// metadataCarrier adapts google.golang.org/grpc/metadata.MD to
// propagation.TextMapCarrier, the same role nats.go's message headers and
// rabbitmq's AMQP table play for the other adapters' trace propagation.
type metadataCarrier metadata.MD

func (c metadataCarrier) Get(key string) string {
	values := metadata.MD(c).Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c metadataCarrier) Set(key, value string) {
	metadata.MD(c).Set(key, value)
}

func (c metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

var _ propagation.TextMapCarrier = metadataCarrier(nil)

// outgoingMetadata returns a mutable copy of ctx's outgoing gRPC metadata,
// creating an empty one if none is present yet.
func outgoingMetadata(ctx context.Context) metadata.MD {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return metadata.MD{}
	}
	return md.Copy()
}

// TraceUnaryClientInterceptor injects the active OpenTelemetry trace context
// from ctx into the outgoing gRPC metadata of every unary call, using
// otel.GetTextMapPropagator() — the same propagator the nats and rabbitmq
// adapters use for message headers.
func TraceUnaryClientInterceptor() googlegrpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *googlegrpc.ClientConn, invoker googlegrpc.UnaryInvoker,
		opts ...googlegrpc.CallOption) error {
		md := outgoingMetadata(ctx)
		otel.GetTextMapPropagator().Inject(ctx, metadataCarrier(md))
		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// TraceUnaryServerInterceptor extracts a trace context carried in the
// incoming gRPC metadata (as injected by TraceUnaryClientInterceptor, or any
// other W3C-trace-context-compatible client) and merges it into the handler's
// context, so a server-side span started from that context continues the
// client's trace.
func TraceUnaryServerInterceptor() googlegrpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = otel.GetTextMapPropagator().Extract(ctx, metadataCarrier(md))
		}
		return handler(ctx, req)
	}
}

// TraceStreamClientInterceptor injects the active OpenTelemetry trace context
// into the outgoing metadata used to open the stream. Context propagation for
// streaming RPCs is scoped to the stream's opening context only: unlike a
// unary call, a stream has no single point after which new trace context
// could be attached, so (as with the standard gRPC and OpenTelemetry gRPC
// instrumentation conventions) the trace context reflects the caller's
// context at Stream-open time.
func TraceStreamClientInterceptor() googlegrpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *googlegrpc.StreamDesc, cc *googlegrpc.ClientConn, method string,
		streamer googlegrpc.Streamer, opts ...googlegrpc.CallOption) (googlegrpc.ClientStream, error) {
		md := outgoingMetadata(ctx)
		otel.GetTextMapPropagator().Inject(ctx, metadataCarrier(md))
		ctx = metadata.NewOutgoingContext(ctx, md)
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// TraceStreamServerInterceptor extracts a trace context carried in the
// metadata that opened the stream and makes it available from the wrapped
// ServerStream's Context, so a handler that starts a span from
// ss.Context() continues the client's trace.
func TraceStreamServerInterceptor() googlegrpc.StreamServerInterceptor {
	return func(srv any, ss googlegrpc.ServerStream, info *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
		ctx := ss.Context()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = otel.GetTextMapPropagator().Extract(ctx, metadataCarrier(md))
		}
		return handler(srv, &tracedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// tracedServerStream overrides ServerStream.Context to return the context
// enriched with the extracted trace context, following the same wrapping
// pattern used throughout the gRPC ecosystem for stream interceptors that
// need to attach request-scoped values.
type tracedServerStream struct {
	googlegrpc.ServerStream
	ctx context.Context
}

func (s *tracedServerStream) Context() context.Context {
	return s.ctx
}

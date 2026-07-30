// Package grpc provides an optional gRPC topology adapter for Modulex: a
// Modulex-managed server lifecycle, OpenTelemetry context propagation
// interceptors, a consistent domain-error-to-status mapping layer, and a
// health integration that reports a modulex.Manager's real registered
// health/readiness checks over the standard gRPC health-checking protocol.
//
// # Scoping: gRPC only, not Connect
//
// ADR-0031's roadmap item 3 calls for "gRPC and/or Connect" topology
// adapters. This package implements gRPC only. connectrpc.com/connect is not
// a dependency of this module today, and adding it would be a genuinely new
// dependency; google.golang.org/grpc and google.golang.org/protobuf, by
// contrast, are already indirect dependencies of this module (pulled in
// transitively by the OTLP gRPC trace exporter), so depending on them
// directly from this package does not add a new dependency to the module's
// dependency graph — it only promotes an existing one. A future package
// (e.g. modulex/connect) can add Connect support without changing anything
// here.
//
// # Core module boundary
//
// This package is a sibling of chi, nats, rabbitmq, watermill, and otel: an
// optional integration package that the core github.com/mediusfy/modulex
// package does not import. A consumer that imports only the core package
// never pulls in google.golang.org/grpc or google.golang.org/protobuf; see
// scripts/check-consumer-boundary.sh.
//
// # What this package provides
//
//   - Server: adapts a *grpc.Server into modulex.Starter and modulex.Stopper
//     so a modulex.Manager owns starting it and gracefully stopping it,
//     mirroring how the httpx package's Serve function manages a *http.Server
//     — except Server implements the lifecycle interfaces directly, since a
//     gRPC server's shutdown (GracefulStop with a bounded fallback to Stop)
//     is a self-contained concern independent of task supervision.
//   - Trace context propagation: TraceUnaryClientInterceptor /
//     TraceUnaryServerInterceptor inject and extract the active OpenTelemetry
//     trace context via gRPC metadata, using the same
//     otel.GetTextMapPropagator() pattern the nats and rabbitmq adapters use
//     for message headers. TraceStreamClientInterceptor /
//     TraceStreamServerInterceptor do the same for streaming RPCs.
//   - Consistent error mapping: ErrorMapping lets a service map its domain
//     errors to gRPC status codes; UnaryServerErrorInterceptor applies that
//     mapping to every unary RPC's returned error. TranslateError converts a
//     status error received by a client back into one of this package's
//     sentinel errors, so callers can use errors.Is instead of inspecting
//     codes.Code directly. There is no streaming error-mapping interceptor:
//     a streaming RPC's error can surface from any Send/Recv call across the
//     life of the stream rather than from one return value, so a
//     single "wrap the terminal error" interceptor would not have a
//     consistent place to run. Callers of a streaming client should call
//     TranslateError explicitly on the error returned from Recv/CloseSend.
//   - Health integration: HealthServer implements
//     grpc_health_v1.HealthServer by evaluating a modulex.Manager's actual
//     registered health and readiness checks on every call, instead of
//     reporting a hardcoded SERVING status.
//
// # What this package does not provide
//
// Client registration is inherently tied to a specific generated service
// stub, so there is no generic "bind a port to a gRPC client" helper here.
// DialOptions bundles the reusable pieces (trace propagation and error
// translation) that any client dial should apply; the client adapter itself
// — dialing a *grpc.ClientConn and calling the generated stub — belongs next
// to the domain port it implements. See
// examples/deployment/notification/adapters/grpc_client.go for a worked
// example, and docs/planning/grpc-adapter-guide.md for the full writeup.
package grpc

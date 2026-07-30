# Modulex gRPC Adapter

This package provides an optional gRPC topology adapter for Modulex: a
Modulex-managed server lifecycle, OpenTelemetry context propagation
interceptors, a consistent domain-error-to-status mapping layer, and a
health integration backed by a `modulex.Manager`'s real registered
health/readiness checks.

This is gRPC only — Connect (`connectrpc.com/connect`) support is out of
scope for this package. See the package doc comment (`doc.go`) and
[`docs/planning/grpc-adapter-guide.md`](../docs/planning/grpc-adapter-guide.md)
for the full scoping rationale and a worked example.

## Usage

```go
import (
    googlegrpc "google.golang.org/grpc"
    healthpb "google.golang.org/grpc/health/grpc_health_v1"

    "github.com/mediusfy/modulex"
    modulexgrpc "github.com/mediusfy/modulex/grpc"
)

// Composition root, typically inside a module's Init:
grpcServer := googlegrpc.NewServer(modulexgrpc.ServerOptions(myErrorMapping)...)
myservicepb.RegisterMyServiceServer(grpcServer, myServiceImpl)
healthpb.RegisterHealthServer(grpcServer, modulexgrpc.NewHealthServer(mgr))

listener, err := net.Listen("tcp", ":50051")
if err != nil {
    return err
}

server, err := modulexgrpc.NewServer(grpcServer, listener)
if err != nil {
    return err
}
// server implements modulex.Starter and modulex.Stopper — register it (or a
// module that delegates Start/Stop to it) with the Manager so
// StartModules/StopModules own the gRPC server's lifecycle.
```

## Behavior

- `Server` adapts a `*grpc.Server` into `modulex.Starter`/`modulex.Stopper`:
  `Start` serves in the background; `Stop` performs a bounded graceful
  shutdown (`GracefulStop`, falling back to a hard `Stop` if it doesn't
  complete before the configured timeout or ctx's deadline).
- `TraceUnaryClientInterceptor`/`TraceUnaryServerInterceptor` and their
  streaming counterparts propagate the active OpenTelemetry trace context via
  gRPC metadata, using `otel.GetTextMapPropagator()` — the same mechanism the
  `nats` and `rabbitmq` adapters use for message headers. **The composition
  root must call `otel.SetTextMapPropagator(...)` at startup** (e.g.
  `propagation.TraceContext{}`); the global default is a no-op.
- `UnaryServerErrorInterceptor` maps domain errors to `status.Error` using a
  caller-supplied `ErrorMapping`; `TranslateError` converts a client-received
  status error back into one of this package's sentinel errors
  (`ErrNotFound`, `ErrInvalidInput`, etc.) for use with `errors.Is`.
- `HealthServer` implements `grpc_health_v1.HealthServer` by evaluating a
  `HealthChecker`'s (e.g. a `*modulex.Manager`'s) actual registered
  health/readiness checks on every call.

## Testing

The adapter's tests use `google.golang.org/grpc/test/bufconn` for in-memory
client/server round trips — no network or external service is required.

```bash
go test ./grpc/...
```

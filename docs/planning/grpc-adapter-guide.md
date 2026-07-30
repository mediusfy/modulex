# gRPC Adapter Guide

This guide documents MOD-55, the third roadmap item from
[ADR-0031](../adr/adr-0031-modulex-value-and-specialization-roadmap.md): an
optional gRPC topology adapter that lets a module bind a domain port to
either a local implementation or a remote gRPC client at the composition
root, with server lifecycle, client registration, graceful shutdown, context
propagation, health integration, and consistent error mapping.

## Scoping: gRPC only, not Connect

ADR-0031 calls for "gRPC and/or Connect" adapters. This work implements gRPC
only, for a dependency-cost reason specific to this module: at the time of
this change, `google.golang.org/grpc` and `google.golang.org/protobuf` were
already **indirect** dependencies of `github.com/mediusfy/modulex` (pulled in
transitively by `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`,
which this module already depends on directly for OTLP trace export).
Promoting an existing indirect dependency to direct, so a new `grpc/`
sub-package can import it, does not add a new dependency to the module's
dependency graph — `go mod tidy` only moves a `// indirect` comment.

`connectrpc.com/connect`, by contrast, is not a dependency of this module at
all today. Adding Connect support would be a genuinely new dependency, which
is a bigger commitment than this ticket's scope. Connect support, if wanted,
belongs in its own future package (e.g. `modulex/connect`) that can make that
tradeoff explicitly and independently.

## Core module boundary

`grpc/` is a sibling of `chi/`, `nats/`, `rabbitmq/`, `watermill/`, and
`otel/`: an optional integration package. The core `github.com/mediusfy/modulex`
package (`modulex.go`, `wire.go`) does not import it, and never will — a
consumer that imports only the core package does not pull in
`google.golang.org/grpc` or `google.golang.org/protobuf`. This is verified by
`make check-consumer-boundary`, which builds `examples/external-consumer` (a
standalone module that imports only the core package) and checks its full
dependency graph for any integration adapter, gRPC included.

## Server lifecycle ownership

`grpc.Server` (in `grpc/server.go`) adapts a `*grpc.Server` into
`modulex.Starter` and `modulex.Stopper`:

```go
server, err := modulexgrpc.NewServer(grpcServer, listener)
```

- **Start**: begins `Serve` in a background goroutine and returns
  immediately — it does not block for the server's lifetime, matching what
  `Manager.StartModules` expects from `Starter.Start`.
- **Stop**: calls `GracefulStop` and waits for in-flight RPCs to finish,
  bounded by `WithShutdownTimeout` (default 10s) or the ctx passed in,
  whichever is shorter. If that bound is reached before `GracefulStop`
  completes, `Stop` calls the underlying `*grpc.Server`'s hard `Stop`, which
  closes the listener and all transports — this cancels the context of every
  in-flight RPC. `Stop` always waits for the `Serve` goroutine to fully exit
  before returning, so the listener is guaranteed closed and any `Serve`
  error is captured once `Stop` returns. `Stop` is idempotent.

**The honest limit of the forced fallback**: closing the transport cancels
every in-flight RPC's context, which bounds shutdown for any handler that
behaves correctly — i.e. one that returns once its context is canceled (the
same assumption `net/http`'s `Shutdown` makes about handlers checking
`r.Context().Done()`). It cannot bound a handler that blocks on something
unrelated to its own context and never checks it. Go has no supported way to
force a running goroutine to stop from the outside; that is a bug in the
handler, not a gap this package (or grpc-go itself) can close. Write handlers
that respect `ctx.Done()`.

**Who owns what**: `Server` owns exactly the serve loop and its shutdown. It
does not build the `*grpc.Server` (the caller registers services and
`ServerOptions` before wrapping it) and does not create the `net.Listener`
(the caller supplies an already-bound one, so bind failures surface before
`NewServer` is ever called). In the notification example,
`notification.GRPCServerModule` is the thin `modulex.Module` that owns this
end-to-end: its `Init` resolves the domain service, builds the `*grpc.Server`,
binds the listener, and wraps both in a `grpc.Server`; its `Start`/`Stop`
delegate straight through. Registering `GRPCServerModule` with a
`modulex.Manager` means `Manager.StartModules`/`StopModules` — not `main()` —
start and gracefully stop the gRPC listener.

## Context propagation

`TraceUnaryClientInterceptor`/`TraceUnaryServerInterceptor` (and their
streaming counterparts, `TraceStreamClientInterceptor`/
`TraceStreamServerInterceptor`) inject and extract the active OpenTelemetry
trace context via gRPC metadata, using `otel.GetTextMapPropagator()` — the
same mechanism the `nats` and `rabbitmq` adapters use for message headers
(see `nats/nats.go`'s `Publish`/`messageContext`).

**This requires the composition root to configure a real propagator.**
`otel.GetTextMapPropagator()` defaults to a no-op composite propagator until
something calls `otel.SetTextMapPropagator(...)` — typically once, at
startup, alongside configuring the `TracerProvider`:

```go
otel.SetTextMapPropagator(propagation.TraceContext{})
```

Without this, `Inject`/`Extract` are silent no-ops: no error, but no trace
context crosses the wire either. This is not specific to gRPC — it is true
of every adapter in this module that propagates trace context via headers —
but it is easy to miss since nothing fails loudly.

Streaming trace propagation is scoped to the stream's opening context only:
`TraceStreamClientInterceptor` injects at stream-open time, and
`TraceStreamServerInterceptor` wraps the server stream's `Context()` so a
handler that starts a span from `ss.Context()` continues the client's trace.

## Consistent error mapping

`ErrorMapping` is a `func(error) codes.Code` a service implements once,
checking its own domain sentinel errors:

```go
type ErrorMapping func(err error) codes.Code
```

- **Server side**: `UnaryServerErrorInterceptor(mapping)` converts a non-nil
  error returned by a handler into `status.Error(mapping(err), err.Error())`.
  An error that already carries a gRPC status (e.g. one passed straight
  through from a nested gRPC call) is left unchanged. `ServerOptions(mapping)`
  bundles this together with the trace interceptors into one
  `[]grpc.ServerOption`.
- **Client side**: `TranslateError(err)` converts a status error into one of
  this package's sentinel errors, so a caller can write
  `errors.Is(err, grpc.ErrNotFound)` instead of inspecting `codes.Code`
  directly. `UnaryClientErrorInterceptor()` applies this automatically;
  `DialOptions()` bundles it with the trace interceptor into one
  `[]grpc.DialOption`.
- There is no streaming error-mapping interceptor. A streaming RPC's error
  can surface from any `Send`/`Recv` call across the stream's life rather
  than from one return value, so there is no single place for a "wrap the
  terminal error" interceptor to run consistently. A streaming client should
  call `TranslateError` explicitly on the error returned from `Recv`/
  `CloseSend`.

### Mapping table

| Domain condition | gRPC code | Sentinel (`TranslateError`) |
|---|---|---|
| (caller-specific, e.g. `service.ErrEmptyMessage`) | `codes.InvalidArgument` | `ErrInvalidInput` |
| (caller-specific "not found") | `codes.NotFound` | `ErrNotFound` |
| (caller-specific "duplicate") | `codes.AlreadyExists` | `ErrAlreadyExists` |
| (caller-specific "forbidden") | `codes.PermissionDenied` | `ErrPermissionDenied` |
| (caller-specific "unauthenticated") | `codes.Unauthenticated` | `ErrUnauthenticated` |
| (caller-specific "temporarily down") | `codes.Unavailable` | `ErrUnavailable` |
| `context.DeadlineExceeded` | `codes.DeadlineExceeded` | `ErrDeadlineExceeded` |
| `context.Canceled` | `codes.Canceled` | `ErrCanceled` |
| anything else | `codes.Internal` | `ErrInternal` |

`DefaultErrorMapping` implements only the bottom three rows (this package
cannot see a specific service's domain errors); a service's own `ErrorMapping`
should check its sentinels first and fall back to `DefaultErrorMapping`. The
notification example's mapping (`grpcErrorMapping` in
`examples/deployment/notification/grpc_module.go`):

```go
func grpcErrorMapping(err error) codes.Code {
    if errors.Is(err, service.ErrEmptyMessage) {
        return codes.InvalidArgument
    }
    return modulexgrpc.DefaultErrorMapping(err)
}
```

## Health integration

`HealthServer` implements `grpc_health_v1.HealthServer` by evaluating a
`HealthChecker`'s (e.g. a `*modulex.Manager`'s) actual registered health and
readiness checks **on every call** — unlike the standard library's
`google.golang.org/grpc/health.Server`, which requires a caller to push
status updates via `SetServingStatus` and can go stale if nobody remembers
to call it.

```go
healthpb.RegisterHealthServer(grpcServer, modulexgrpc.NewHealthServer(mgr))
```

Service name convention (`HealthCheckRequest.Service`):

- `""` (empty) or an unrecognized name → evaluates `HealthChecks()`
  (`modulex.Registry.RegisterHealthCheck`).
- `modulexgrpc.ReadinessService` (`"readiness"`) → evaluates
  `ReadinessChecks()` (`modulex.Registry.RegisterReadinessCheck`).

`Watch` sends the current status immediately, then re-evaluates on an
interval (`WithWatchInterval`, default 5s), sending a new message only when
the status changes, until the client's context ends.

## Client registration

There is no generic "bind a port to a gRPC client" helper in `grpc/` —
a gRPC client is inherently tied to a specific generated service stub, so
this genuinely belongs next to the domain port it implements, not in the
transport-neutral core. What `grpc/` provides is the reusable piece every
such client should apply: `DialOptions()` bundles trace propagation and
error translation into one `[]grpc.DialOption`. The caller adds transport
credentials explicitly (a security-sensitive choice this package never makes
for you):

```go
conn, err := grpc.NewClient(target, append(
    []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
    modulexgrpc.DialOptions()...,
)...)
```

## Worked example: the notification port, bound locally and via gRPC

`examples/deployment/notification/ports/service.go` defines the domain port
this example reuses unchanged:

```go
type Sender interface {
    Send(ctx context.Context, message string) error
}
var ServiceKey = modulex.NewKey[Sender]("notification.Service")
```

This is the same port the existing HTTP example
(`examples/deployment/notification/adapters/http_client.go`/`http_server.go`)
already binds locally vs. remotely. The gRPC variant sits alongside it:

| File | Role |
|---|---|
| `notification/notificationpb/notification.proto` | Defines `Notification.Send`, matching `Sender.Send`'s shape (a message in, nothing but an error out). |
| `notification/notificationpb/*.pb.go` | Generated by `protoc` (see "Regenerating protobuf code" below) and committed to the repository. |
| `notification/adapters/grpc_server.go` | `GRPCServer` wraps a `ports.Sender` and exposes it as `notificationpb.NotificationServer`. Returns the domain error unchanged — mapping happens once, centrally, via `ServerOptions`. |
| `notification/adapters/grpc_client.go` | `GRPCClient` implements `ports.Sender` by calling a dialed `*grpc.ClientConn`, translating errors via `TranslateError`. |
| `notification/grpc_module.go` | `GRPCServerModule` (hosts the service, owns the `grpc.Server`'s lifecycle) and `GRPCRemoteModule` (dials the remote service, registers `ports.ServiceKey`) — the gRPC counterparts of `Module`/`RemoteModule`. |
| `remote/notification-grpc-server/main.go` | Standalone process: registers `notification.NewModule()` (the local implementation) and `notification.NewGRPCServerModule(addr)`, and lets the `Manager` own starting/stopping the gRPC listener. |
| `remote/grpc-consumer/main.go` | Standalone process: registers `notification.NewGRPCRemoteModule(target)` and `consumer.NewModule()` — the same `consumer.Module` the HTTP remote example uses, unchanged, because it only ever depends on `ports.ServiceKey`. |

`examples/deployment/integration_test.go`'s `TestRemoteGRPCComposition` runs
both processes' wiring in-test against a real TCP loopback connection,
proving the monolith and the remote-adapter example bind the same
`ports.Sender`/`ports.ServiceKey` — one locally, one over gRPC — without
either process's business logic (`consumer.Module`, `service.NotificationService`)
changing at all.

## Regenerating protobuf code

Generated `.pb.go`/`_grpc.pb.go` files are committed to the repository, like
any other Go source — `make build`/`make test`/CI never require `protoc` to
be installed, only a changed `.proto` file requires regenerating and
committing the result. If you have `protoc`, `protoc-gen-go`, and
`protoc-gen-go-grpc` installed locally:

```bash
make proto-gen
```

`proto-gen` is a convenience target only; it is intentionally not wired into
`build`, `test`, `lint`, or CI.

## Testing

`grpc/`'s tests use `google.golang.org/grpc/test/bufconn` for in-memory
client/server round trips (no network needed) and, for the health
integration, the real `grpc_health_v1` service already vendored by
`google.golang.org/grpc/health` — so the generic package's lifecycle,
propagation, and error-mapping tests all exercise a real generated gRPC
service without needing a bespoke test-only `.proto`. The notification
example's own tests (`examples/deployment/notification/adapters/grpc_test.go`)
separately validate that the generated `notificationpb` code round-trips a
real `Send` call end-to-end.

## Related work

- `docs/adr/adr-0031-modulex-value-and-specialization-roadmap.md`
- `docs/planning/lifecycle-guide.md`
- `docs/planning/eventbus-capabilities-guide.md`
- Jira MOD-55: Add gRPC and Connect topology adapters

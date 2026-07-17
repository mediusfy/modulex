# Modulex Library — Coding Standards

> **Reference implementation:** `~/programming/projects/pim-agl.monorepo/pim-agl-online-data-relay/`
> All new Go code in this repository should follow the coding style established in the pim-agl-online-data-relay
> project. When in doubt about style, naming, error handling, or observability conventions,
> consult that codebase as the canonical reference.

---

## 1. Package Layout & Architecture

### Hexagonal (Ports & Adapters)



## 2. Imports

Three groups, blank line between each, alphabetized within:

```go
import (
    "context"
    "log/slog"
    "net/http"

    "github.com/go-chi/chi/v5"
    "go.opentelemetry.io/otel/metric"

    "github.com/mediusfy/platform/modulex/internal/<name of package>"
)
```

Reference: `pim-agl-online-data-relay/internal/api/api.go` lines 3-18.

---

## 3. Formatting

- `gofmt -w .` before commit (tabs, 4-space display width).
- Line length: aim for 100-120 characters; break long lines logically.
- No empty line between the copyright/build-tag comment and `package` declaration.

Reference: `pim-agl-online-data-relay/AGENTS.md` §Formatting.

---

## 4. Naming Conventions

| Element | Convention | Example |
|---------|-----------|---------|
| Packages | Short, lowercase, matches directory | `metrics`, `config`, `apperrors` |
| Exported types/funcs | PascalCase | `ProcessRequest`, `NewService` |
| Unexported | camelCase | `startHTTPServer`, `finalLog` |
| Errors | `type Error string` + `Err` prefix | `ErrServerConfigNil`, `ErrMissingPubSubEnvelope` |
| Log keys | `logKey` prefix, camelCase | `logKeyError`, `logKeyPort`, `logKeyName` |
| Trace keys | `traceAttr` / `traceSpan` prefix | `traceAttrMessageID`, `traceSpanSolacePublish` |

Reference: `pim-agl-online-data-relay/internal/service/service.go` lines 22-63,
`pim-agl-online-data-relay/internal/apperrors/errors.go`.

---

## 5. Error Handling

### Sentinel Errors

Use Go 1.13+ sentinel error pattern with a typed `Error` string:

```go
type Error string

func (e Error) Error() string { return string(e) }

const (
    ErrValidationFailed Error = "message validation failed"
    ErrServerConfigNil  Error = "server config must not be nil"
)
```

Reference: `pim-agl-online-data-relay/internal/apperrors/errors.go`.

### Wrapping

Wrap errors with context using the `"%w: %v"` pattern:

```go
const errFmtWrapper = "%w: %v"

return fmt.Errorf(errFmtWrapper, ErrPublishFailed, err)
```

Reference: `pim-agl-online-data-relay/internal/service/service.go` line 62.

---

## 6. Constants

Define all string/magic constants as `const` blocks at the top of each file. Group by category
with a blank line between groups:

```go
const (
    // Error sentinels
    ErrMessageCreation  apperrors.Error = "message creation error"
    ErrMessageFiltered  apperrors.Error = "message filtered out"

    // Trace attributes
    traceAttrMessageID      = "message.id"
    traceAttrMessageCountry = "message.country"

    // Log keys
    logKeyResult        = "result"
    logKeyMessageID     = "message_id"
    logKeyError         = "error"
)
```

Reference: `pim-agl-online-data-relay/internal/service/service.go` lines 22-63.

---

## 7. Logging & Observability

### Logging

Use `log/slog` exclusively. No `log` or `fmt.Print`. Structured attributes always use named
log-key constants, never raw string literals:

```go
s.logger.LogAttrs(ctx, lvl, logMsgProcessMessageDone,
    slog.String(logKeyResult, string(res)),
    slog.String(logKeyMessageID, req.MessageID),
    slog.Any(logKeyError, err),
)
```

Reference: `pim-agl-online-data-relay/internal/service/service.go` lines 185-197.

### OpenTelemetry Tracing

Every public method creates a span, adds attributes, records errors, and defers `span.End()`:

```go
func (s *Service) ProcessMessage(ctx context.Context, req ProcessRequest) (err error) {
    ctx, span := s.gtrace.StartSpan(ctx, "service.ProcessMessage",
        attribute.String(traceAttrMessageID, req.MessageID),
    )
    defer func() {
        if err != nil {
            s.gtrace.RecordError(span, err)
        }
        span.End()
    }()
    // ...
}
```

Child spans via helper methods:

```go
func (s *Service) startChildSpan(ctx context.Context, name string, ...) (context.Context, trace.Span) {
    return s.gtrace.StartSpan(ctx, name, attrs...)
}
```

Reference: `pim-agl-online-data-relay/internal/service/service.go` lines 120-183, 250-260.

### Prometheus Metrics

Expose a `/metrics` endpoint via `httphelper.MetricsHandler()`. Track counters and histograms
for processing events. Reference: `pim-agl-online-data-relay/internal/metrics/metrics.go`.

---

## 8. Concurrency

Use `golang.org/x/sync/errgroup` for orchestrating multiple long-running goroutines
(API server, metrics server, background consumers/publishers):

```go
eg, ctx := errgroup.WithContext(ctx)

eg.Go(func() error {
    return startHTTPServer(ctx, cfg.Server.Port, app.Routes(), apiServer)
})

eg.Go(func() error {
    return startHTTPServer(ctx, cfg.Server.MetricsPort, httphelper.MetricsHandler(), metricsServer)
})

return eg.Wait()
```

Reference: `pim-agl-online-data-relay/cmd/main.go` lines 109-126.

---

## 9. Dependency Injection

- No global state. All dependencies passed through constructors.
- Components (loggers, tracers, publishers, metrics) are configurable struct fields.
- Constructor functions follow `NewXxx(deps...) (*Xxx, error)` pattern with validation.

Reference: `pim-agl-online-data-relay/internal/service/service.go` lines 106-118,
`pim-agl-online-data-relay/internal/api/api.go` lines 44-57.

---

## 10. HTTP Layer

### Router (Chi)

```go
func (a *API) Routes() http.Handler {
    r := chi.NewRouter()
    r.Use(middleware.CleanPath)
    r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
    r.Use(otelchi.Middleware(a.ServiceName, otelchi.WithChiRoutes(r)))
    // ...
    return r
}
```

### Handler Pattern

Handlers are thin — parse request, delegate to service, format response:

```go
func (a *API) dispatchPubSub(w http.ResponseWriter, r *http.Request) {
    name := chi.URLParam(r, urlParamHandler)
    h, ok := a.SubscriptionHandlers[name]
    if !ok {
        http.NotFound(w, r)
        return
    }
    a.execute(w, r, spanNameDispatch, func(ctx context.Context) error {
        env, ok := GetPubSubEnvelope(ctx)
        if !ok {
            return apperrors.ErrMissingPubSubEnvelope
        }
        return h.Handle(ctx, env.Message)
    })
}
```

Reference: `pim-agl-online-data-relay/internal/api/api.go` lines 59-102.

---

## 11. Graceful Shutdown

Intercept OS signals, cancel context, shut down servers with timeouts:

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()
    // ...
}

func startHTTPServer(ctx context.Context, port string, handler http.Handler, name string) error {
    srv := &http.Server{Addr: ":" + port, Handler: handler}
    // ... listen in goroutine, wait for ctx.Done(), call srv.Shutdown(...)
}
```

Reference: `pim-agl-online-data-relay/cmd/main.go` lines 42-153.

---

## 12. Testing

### Table-Driven Tests

```go
tests := []struct {
    name    string
    req     ProcessRequest
    wantErr bool
}{
    {name: "valid message", req: validReq, wantErr: false},
    {name: "empty payload", req: emptyReq, wantErr: true},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // ...
    })
}
```

### Mocks

Generate mocks with `mockery` from `.mockery.yaml` at service root. Interface segregation:
define one-method interfaces at the consumer site where possible.

Reference: `pim-agl-online-data-relay/.mockery.yaml`, `pim-agl-online-data-relay/mocks/`.

---

## 13. Before Committing

```bash
gofmt -w .
golangci-lint run ./...
go test ./... -count=1 -race
```

Reference: `pim-agl-online-data-relay/AGENTS.md` §Build, Lint, and Test Commands.


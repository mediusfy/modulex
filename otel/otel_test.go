package otel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mediusfy/modulex"
	modulexotel "github.com/mediusfy/modulex/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTestManager() *modulex.Manager {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	if err != nil {
		panic(err)
	}
	return mgr
}

func TestTracesNoGaps(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	manager := newTestManager()
	modulex.WithTracer(modulexotel.NewTracer(tp))(manager)

	modA := &testModule{name: "module-a"}
	require.NoError(t, manager.RegisterModule(modA))

	ctx := context.Background()

	err := manager.InitModules(ctx)
	require.NoError(t, err)

	spans := sr.Ended()
	require.Len(t, spans, 2)

	var parentSpan, childSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "InitModules" {
			parentSpan = s
		} else if s.Name() == "InitModule:module-a" {
			childSpan = s
		}
	}

	require.NotNil(t, parentSpan)
	require.NotNil(t, childSpan)

	assert.Equal(t, parentSpan.SpanContext().SpanID(), childSpan.Parent().SpanID())
	assert.Equal(t, parentSpan.SpanContext().TraceID(), childSpan.SpanContext().TraceID())
}

func TestBackgroundGoTracingPropagation(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	manager := newTestManager()
	modulex.WithTracer(modulexotel.NewTracer(tp))(manager)

	ctx, parentSpan := tp.Tracer("test").Start(context.Background(), "RootTask")

	handle, err := manager.Go(ctx, "BackgroundTask", func(bgCtx context.Context) error {
		_, activeSpan := tp.Tracer("test").Start(bgCtx, "NestedTask")
		activeSpan.End()
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, handle)

	require.NoError(t, handle.Wait())
	parentSpan.End()

	spans := sr.Ended()
	require.Len(t, spans, 3)

	var bgSpan, nestedSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "BackgroundTask" {
			bgSpan = s
		} else if s.Name() == "NestedTask" {
			nestedSpan = s
		}
	}

	require.NotNil(t, bgSpan)
	require.NotNil(t, nestedSpan)

	assert.Equal(t, parentSpan.SpanContext().TraceID(), bgSpan.SpanContext().TraceID())
	assert.Equal(t, parentSpan.SpanContext().TraceID(), nestedSpan.SpanContext().TraceID())

	assert.Equal(t, parentSpan.SpanContext().SpanID(), bgSpan.Parent().SpanID())
	assert.Equal(t, bgSpan.SpanContext().SpanID(), nestedSpan.Parent().SpanID())
}

func TestTracerRecordsErrorAndStatus(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	tracer := modulexotel.NewTracer(tp)
	ctx := context.Background()
	_, span := tracer.Start(ctx, "ErrorSpan", nil)

	sentinel := assert.AnError
	span.RecordError(sentinel)
	span.End()

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Contains(t, spans[0].Status().Description, sentinel.Error())
	require.Len(t, spans[0].Events(), 1)
	assert.Equal(t, "exception", spans[0].Events()[0].Name)
}

func TestSpanAttributesConversion(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	tracer := modulexotel.NewTracer(tp)
	ctx := context.Background()
	_, span := tracer.Start(ctx, "AttrSpan", map[string]any{
		"string_attr": "value",
		"int_attr":    42,
		"bool_attr":   true,
		"float_attr":  3.14,
		"uint_attr":   uint(7),
	})
	span.End()

	spans := sr.Ended()
	require.Len(t, spans, 1)
	attrs := spans[0].Attributes()
	require.Len(t, attrs, 5)

	got := make(map[string]any)
	for _, attr := range attrs {
		got[string(attr.Key)] = attr.Value.AsInterface()
	}
	assert.Equal(t, "value", got["string_attr"])
	assert.Equal(t, int64(42), got["int_attr"])
	assert.Equal(t, true, got["bool_attr"])
	assert.Equal(t, 3.14, got["float_attr"])
	// Unsupported types fall back to a formatted string representation.
	assert.Equal(t, "7", got["uint_attr"])
}

func TestNewTracerFallsBackToGlobalProvider(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(nooptrace.NewTracerProvider()) })

	tracer := modulexotel.NewTracer(nil)
	ctx := context.Background()
	_, span := tracer.Start(ctx, "GlobalFallback", nil)
	span.End()

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "GlobalFallback", spans[0].Name())
}

func TestContextWithSpanContextIgnoresForeignImplementation(t *testing.T) {
	tracer := modulexotel.NewTracer(sdktrace.NewTracerProvider())
	ctx := context.Background()

	// Pass a SpanContext implementation that is not *spanContext.
	foreign := foreignSpanContext{}
	got := tracer.ContextWithSpanContext(ctx, foreign)
	assert.Equal(t, ctx, got)
}

func TestHTTPMiddlewareSpanCreation(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	middleware := modulexotel.HTTPMiddleware(tp)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "GET", spans[0].Name())
	assert.Equal(t, codes.Unset, spans[0].Status().Code)
}

func TestHTTPMiddlewareRecords5xxError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	middleware := modulexotel.HTTPMiddleware(tp)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodPost, "/fail", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, int64(500), spans[0].Attributes()[2].Value.AsInt64())
}

func TestHTTPMiddlewareCustomSpanName(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	middleware := modulexotel.HTTPMiddleware(tp,
		modulexotel.WithHTTPSpanName(func(r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/custom", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "GET /custom", spans[0].Name())
}

func TestSubscriberMiddlewareSpanCreation(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	var gotPayload []byte
	handler := modulexotel.SubscriberMiddleware(tp)(
		"orders.created",
		func(ctx context.Context, payload []byte) error {
			gotPayload = payload
			return nil
		},
	)

	err := handler(context.Background(), []byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), gotPayload)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "process orders.created", spans[0].Name())
}

func TestSubscriberMiddlewareRecordsError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	sentinel := errors.New("handler failed")
	handler := modulexotel.SubscriberMiddleware(tp)(
		"orders.failed",
		func(ctx context.Context, payload []byte) error {
			return sentinel
		},
	)

	err := handler(context.Background(), []byte("data"))
	require.ErrorIs(t, err, sentinel)

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
}

func TestSubscriberMiddlewareCustomTopicAttr(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	handler := modulexotel.SubscriberMiddleware(tp,
		modulexotel.WithSubscriberTopicAttr("custom.topic"),
	)(
		"events.user",
		func(ctx context.Context, payload []byte) error {
			return nil
		},
	)

	_ = handler(context.Background(), []byte("data"))

	spans := sr.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "events.user", spans[0].Attributes()[0].Value.AsString())
}

type foreignSpanContext struct{}

func (foreignSpanContext) IsValid() bool   { return true }
func (foreignSpanContext) TraceID() string { return "foreign-trace" }
func (foreignSpanContext) SpanID() string  { return "foreign-span" }

type testModule struct {
	name string
}

func (m *testModule) Name() string                                 { return m.name }
func (m *testModule) DependsOn() []string                          { return nil }
func (m *testModule) Init(context.Context, modulex.Registry) error { return nil }

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

func TestHTTPMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		opts           []modulexotel.HTTPOption
		method         string
		path           string
		handlerStatus  int
		wantSpanName   string
		wantStatusCode codes.Code
		check          func(t *testing.T, span sdktrace.ReadOnlySpan)
	}{
		{
			name:           "creates span named after the HTTP method",
			method:         http.MethodGet,
			path:           "/test",
			handlerStatus:  http.StatusOK,
			wantSpanName:   "GET",
			wantStatusCode: codes.Unset,
		},
		{
			name:           "records the status code attribute on 5xx responses",
			method:         http.MethodPost,
			path:           "/fail",
			handlerStatus:  http.StatusInternalServerError,
			wantSpanName:   "POST",
			wantStatusCode: codes.Unset,
			check: func(t *testing.T, span sdktrace.ReadOnlySpan) {
				assert.Equal(t, int64(500), span.Attributes()[2].Value.AsInt64())
			},
		},
		{
			name: "custom span name function overrides the default",
			opts: []modulexotel.HTTPOption{
				modulexotel.WithHTTPSpanName(func(r *http.Request) string {
					return r.Method + " " + r.URL.Path
				}),
			},
			method:         http.MethodGet,
			path:           "/custom",
			handlerStatus:  http.StatusOK,
			wantSpanName:   "GET /custom",
			wantStatusCode: codes.Unset,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

			middleware := modulexotel.HTTPMiddleware(tp, tt.opts...)
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.handlerStatus)
				_, _ = w.Write([]byte("body"))
			}))

			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.handlerStatus, rec.Code)

			spans := sr.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, tt.wantSpanName, spans[0].Name())
			assert.Equal(t, tt.wantStatusCode, spans[0].Status().Code)
			if tt.check != nil {
				tt.check(t, spans[0])
			}
		})
	}
}

func TestSubscriberMiddleware(t *testing.T) {
	sentinel := errors.New("handler failed")

	tests := []struct {
		name           string
		opts           []modulexotel.SubscriberOption
		topic          string
		payload        []byte
		handlerErr     error
		wantErr        error
		wantSpanName   string
		wantStatusCode codes.Code
		check          func(t *testing.T, span sdktrace.ReadOnlySpan)
	}{
		{
			name:           "creates span named after the topic",
			topic:          "orders.created",
			payload:        []byte("hello"),
			wantSpanName:   "process orders.created",
			wantStatusCode: codes.Unset,
		},
		{
			name:           "records handler error and error status",
			topic:          "orders.failed",
			payload:        []byte("data"),
			handlerErr:     sentinel,
			wantErr:        sentinel,
			wantSpanName:   "process orders.failed",
			wantStatusCode: codes.Error,
		},
		{
			name: "custom topic attribute key",
			opts: []modulexotel.SubscriberOption{
				modulexotel.WithSubscriberTopicAttr("custom.topic"),
			},
			topic:          "events.user",
			payload:        []byte("data"),
			wantSpanName:   "process events.user",
			wantStatusCode: codes.Unset,
			check: func(t *testing.T, span sdktrace.ReadOnlySpan) {
				assert.Equal(t, "events.user", span.Attributes()[0].Value.AsString())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

			var gotPayload []byte
			handler := modulexotel.SubscriberMiddleware(tp, tt.opts...)(
				tt.topic,
				func(ctx context.Context, payload []byte) error {
					gotPayload = payload
					return tt.handlerErr
				},
			)

			err := handler(context.Background(), tt.payload)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.payload, gotPayload)

			spans := sr.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, tt.wantSpanName, spans[0].Name())
			assert.Equal(t, tt.wantStatusCode, spans[0].Status().Code)
			if tt.check != nil {
				tt.check(t, spans[0])
			}
		})
	}
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

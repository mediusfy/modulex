package otel

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/mediusfy/modulex"
)

func HTTPMiddleware(tp oteltrace.TracerProvider, opts ...HTTPOption) func(http.Handler) http.Handler {
	tracer, prop := resolveOTel(tp)
	var cfg httpConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.spanNameFn == nil {
		cfg.spanNameFn = func(r *http.Request) string { return r.Method }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			spanName := cfg.spanNameFn(r)
			ctx, span := tracer.Start(ctx, spanName,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			)
			defer span.End()

			span.SetAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPathKey.String(r.URL.Path),
			)

			sw := newStatusWriter(w)
			next.ServeHTTP(sw, r.WithContext(ctx))

			span.SetAttributes(semconv.HTTPResponseStatusCodeKey.Int(sw.status))
			if sw.status >= 500 {
				span.SetAttributes(attribute.Bool("error", true))
			}
		})
	}
}

// HTTPOption configures the HTTP middleware.
type HTTPOption func(*httpConfig)

type httpConfig struct {
	spanNameFn func(r *http.Request) string
}

// WithHTTPSpanName sets a custom span naming function. The default uses the
// HTTP method as the span name.
func WithHTTPSpanName(fn func(r *http.Request) string) HTTPOption {
	return func(c *httpConfig) {
		c.spanNameFn = fn
	}
}

// SubscriberMiddleware wraps an EventHandler with automatic span creation.
func SubscriberMiddleware(tp oteltrace.TracerProvider,
	opts ...SubscriberOption) func(topic string, handler modulex.EventHandler) modulex.EventHandler {
	tracer, _ := resolveOTel(tp)
	var cfg subscriberConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.topicAttr == "" {
		cfg.topicAttr = "messaging.destination"
	}

	return func(topic string, handler modulex.EventHandler) modulex.EventHandler {
		return func(ctx context.Context, payload []byte) error {
			ctx, span := tracer.Start(ctx, "process "+topic,
				oteltrace.WithSpanKind(oteltrace.SpanKindConsumer),
			)
			defer span.End()

			span.SetAttributes(attribute.String(cfg.topicAttr, topic))

			if err := handler(ctx, payload); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return err
			}
			return nil
		}
	}
}

// SubscriberOption configures the subscriber middleware.
type SubscriberOption func(*subscriberConfig)

type subscriberConfig struct {
	topicAttr string
}

// WithSubscriberTopicAttr sets the attribute key used to record the topic
// name on subscriber spans. Default is "messaging.destination".
func WithSubscriberTopicAttr(key string) SubscriberOption {
	return func(c *subscriberConfig) {
		c.topicAttr = key
	}
}

func resolveOTel(tp oteltrace.TracerProvider) (oteltrace.Tracer, propagation.TextMapPropagator) {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("github.com/mediusfy/modulex/otel"), otel.GetTextMapPropagator()
}

// statusWriter wraps http.ResponseWriter while preserving optional interfaces.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newStatusWriter(w http.ResponseWriter) *statusWriter {
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Support http.Flusher interface (Server-Sent Events)
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Support http.Hijacker interface (WebSockets)
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("http.Hijacker not supported by underlying ResponseWriter")
}

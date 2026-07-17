// Package otel provides an OpenTelemetry Tracer adapter for Modulex.
//
// It implements the modulex.Tracer interface using an OpenTelemetry
// TracerProvider, preserving span ancestry and recording errors with the
// appropriate span status.
package otel

import (
	"context"
	"fmt"

	"github.com/mediusfy/modulex"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Tracer adapts an OpenTelemetry TracerProvider to modulex.Tracer.
type Tracer struct {
	tracer oteltrace.Tracer
}

// NewTracer creates a Modulex Tracer backed by OpenTelemetry.
//
// If provider is nil, the global OpenTelemetry TracerProvider is used.
// The returned tracer is safe for concurrent use.
func NewTracer(provider oteltrace.TracerProvider) modulex.Tracer {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	return &Tracer{
		tracer: provider.Tracer("github.com/mediusfy/modulex"),
	}
}

// Start implements modulex.Tracer.
func (t *Tracer) Start(ctx context.Context, spanName string, attrs map[string]any) (context.Context, modulex.Span) {
	var opts []oteltrace.SpanStartOption
	if len(attrs) > 0 {
		opts = append(opts, oteltrace.WithAttributes(convertAttributes(attrs)...))
	}
	ctx, span := t.tracer.Start(ctx, spanName, opts...)
	return ctx, &Span{span: span}
}

// SpanContextFromContext implements modulex.Tracer.
func (t *Tracer) SpanContextFromContext(ctx context.Context) modulex.SpanContext {
	return &spanContext{sc: oteltrace.SpanContextFromContext(ctx)}
}

// ContextWithSpanContext implements modulex.Tracer.
func (t *Tracer) ContextWithSpanContext(ctx context.Context, sc modulex.SpanContext) context.Context {
	if s, ok := sc.(*spanContext); ok {
		return oteltrace.ContextWithSpanContext(ctx, s.sc)
	}
	return ctx
}

// Span adapts an OpenTelemetry span to modulex.Span.
type Span struct {
	span oteltrace.Span
}

// End implements modulex.Span.
func (s *Span) End() {
	s.span.End()
}

// RecordError implements modulex.Span.
func (s *Span) RecordError(err error) {
	if err == nil {
		return
	}
	s.span.RecordError(err)
	s.span.SetStatus(codes.Error, err.Error())
}

// SetAttributes implements modulex.Span.
func (s *Span) SetAttributes(attrs map[string]any) {
	if len(attrs) == 0 {
		return
	}
	s.span.SetAttributes(convertAttributes(attrs)...)
}

// spanContext adapts an OpenTelemetry SpanContext to modulex.SpanContext.
type spanContext struct {
	sc oteltrace.SpanContext
}

// IsValid implements modulex.SpanContext.
func (s *spanContext) IsValid() bool {
	return s.sc.IsValid()
}

func convertAttributes(attrs map[string]any) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		switch val := v.(type) {
		case string:
			out = append(out, attribute.String(k, val))
		case int:
			out = append(out, attribute.Int(k, val))
		case int64:
			out = append(out, attribute.Int64(k, val))
		case bool:
			out = append(out, attribute.Bool(k, val))
		case float64:
			out = append(out, attribute.Float64(k, val))
		default:
			out = append(out, attribute.String(k, fmtValue(v)))
		}
	}
	return out
}

func fmtValue(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return fmt.Sprintf("%v", v)
}

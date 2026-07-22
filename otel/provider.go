package otel

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Environment variables read by NewProviderFromEnv when the corresponding
// ProviderOption is not supplied. These mirror the standard OTLP exporter
// environment variables (see the OpenTelemetry spec), so a consumer's
// existing collector configuration works without code changes.
const (
	envExporterProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envExporterEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envSampleRatio      = "OTEL_TRACES_SAMPLER_ARG"

	defaultExporterProtocol = "grpc"
	defaultSampleRatio      = 1.0
)

// providerConfig holds the resolved configuration for NewProviderFromEnv,
// built up from environment-variable defaults and any ProviderOption
// overrides.
type providerConfig struct {
	protocol      string
	endpoint      string
	sampleRatio   float64
	resourceAttrs []attribute.KeyValue
	spanProcessor sdktrace.SpanProcessor
}

// ProviderOption configures NewProviderFromEnv.
type ProviderOption func(*providerConfig)

// WithExporterProtocol overrides the OTLP exporter protocol. Recognized
// values are "grpc" (the default), "http" (an alias for "http/protobuf"),
// and "none", which disables span export entirely (useful for local
// development). Overrides OTEL_EXPORTER_OTLP_PROTOCOL.
func WithExporterProtocol(protocol string) ProviderOption {
	return func(c *providerConfig) {
		c.protocol = protocol
	}
}

// WithExporterEndpoint overrides the OTLP collector endpoint. Overrides
// OTEL_EXPORTER_OTLP_ENDPOINT.
func WithExporterEndpoint(endpoint string) ProviderOption {
	return func(c *providerConfig) {
		c.endpoint = endpoint
	}
}

// WithSampleRatio overrides the trace sampling ratio, used as the ratio for
// a ParentBased(TraceIDRatioBased) sampler. Overrides
// OTEL_TRACES_SAMPLER_ARG.
func WithSampleRatio(ratio float64) ProviderOption {
	return func(c *providerConfig) {
		c.sampleRatio = ratio
	}
}

// WithResourceAttributes adds extra resource attributes alongside the
// service.name attribute derived from NewProviderFromEnv's serviceName
// argument.
func WithResourceAttributes(attrs ...attribute.KeyValue) ProviderOption {
	return func(c *providerConfig) {
		c.resourceAttrs = append(c.resourceAttrs, attrs...)
	}
}

// WithSpanProcessor attaches an additional sdktrace.SpanProcessor alongside
// the OTLP batch processor (or in place of it, when the exporter protocol is
// "none"). Use this to add a console/debug processor, or a test
// tracetest.SpanRecorder to observe the provider's output.
func WithSpanProcessor(sp sdktrace.SpanProcessor) ProviderOption {
	return func(c *providerConfig) {
		c.spanProcessor = sp
	}
}

// NewProviderFromEnv builds an OTLP-exporting *sdktrace.TracerProvider
// driven by standard OTEL_EXPORTER_OTLP_* environment variables, plus
// serviceName as the service.name resource attribute.
//
// This factors out the generic OTLP-provider-construction boilerplate
// (exporter protocol/endpoint resolution, resource attributes, sampling)
// that's otherwise hand-rolled per service. It deliberately does not cover
// app-specific concerns like PII redaction; wrap the returned provider's
// exporter/processor chain (via WithSpanProcessor) for that.
//
// The returned shutdown func flushes and closes the exporter; callers should
// defer it. Pass the returned provider to NewTracer to adapt it to
// modulex.Tracer:
//
//	tp, shutdown, err := otel.NewProviderFromEnv("my-service")
//	if err != nil {
//	    return err
//	}
//	defer shutdown(context.Background())
//	tracer := otel.NewTracer(tp)
func NewProviderFromEnv(serviceName string, opts ...ProviderOption) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	cfg := providerConfig{
		protocol:    envOrDefault(envExporterProtocol, defaultExporterProtocol),
		endpoint:    os.Getenv(envExporterEndpoint),
		sampleRatio: envFloatOrDefault(envSampleRatio, defaultSampleRatio),
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		append([]attribute.KeyValue{semconv.ServiceName(serviceName)}, cfg.resourceAttrs...)...,
	))
	if err != nil {
		return nil, nil, fmt.Errorf("otel: failed to build resource: %w", err)
	}

	exporter, err := newExporter(cfg)
	if err != nil {
		return nil, nil, err
	}

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.sampleRatio))),
	}
	if exporter != nil {
		tpOpts = append(tpOpts, sdktrace.WithBatcher(exporter))
	}
	if cfg.spanProcessor != nil {
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(cfg.spanProcessor))
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	return tp, tp.Shutdown, nil
}

// newExporter builds the configured OTLP exporter, or returns a nil
// exporter (and nil error) when the protocol is "none".
func newExporter(cfg providerConfig) (sdktrace.SpanExporter, error) {
	switch strings.ToLower(cfg.protocol) {
	case "none":
		return nil, nil
	case "http", "http/protobuf":
		httpOpts := []otlptracehttp.Option{}
		if cfg.endpoint != "" {
			httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(cfg.endpoint))
		}
		exporter, err := otlptracehttp.New(context.Background(), httpOpts...)
		if err != nil {
			return nil, fmt.Errorf("otel: failed to build OTLP HTTP exporter: %w", err)
		}
		return exporter, nil
	case "grpc", "":
		grpcOpts := []otlptracegrpc.Option{}
		if cfg.endpoint != "" {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(cfg.endpoint))
		}
		exporter, err := otlptracegrpc.New(context.Background(), grpcOpts...)
		if err != nil {
			return nil, fmt.Errorf("otel: failed to build OTLP gRPC exporter: %w", err)
		}
		return exporter, nil
	default:
		return nil, fmt.Errorf("otel: unsupported OTLP exporter protocol %q", cfg.protocol)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloatOrDefault(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

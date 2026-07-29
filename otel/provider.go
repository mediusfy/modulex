package otel

import (
	"context"
	"fmt"
	"net/url"
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

const (
	envExporterProtocol = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envExporterEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envInsecure         = "OTEL_EXPORTER_OTLP_INSECURE"
	envSampleRatio      = "OTEL_TRACES_SAMPLER_ARG"

	defaultExporterProtocol = "grpc"
	defaultSampleRatio      = 1.0
)

type providerConfig struct {
	protocol      string
	endpoint      string
	insecure      *bool // Use pointer to distinguish set vs unset
	sampleRatio   float64
	resourceAttrs []attribute.KeyValue
	spanProcessor sdktrace.SpanProcessor
}

// ProviderOption configures NewProviderFromEnv.
type ProviderOption func(*providerConfig)

func WithExporterProtocol(protocol string) ProviderOption {
	return func(c *providerConfig) {
		c.protocol = protocol
	}
}

func WithExporterEndpoint(endpoint string) ProviderOption {
	return func(c *providerConfig) {
		c.endpoint = endpoint
	}
}

// WithInsecure explicitly forces enabling or disabling TLS on the exporter.
func WithInsecure(insecure bool) ProviderOption {
	return func(c *providerConfig) {
		c.insecure = &insecure
	}
}

func WithSampleRatio(ratio float64) ProviderOption {
	return func(c *providerConfig) {
		c.sampleRatio = ratio
	}
}

func WithResourceAttributes(attrs ...attribute.KeyValue) ProviderOption {
	return func(c *providerConfig) {
		c.resourceAttrs = append(c.resourceAttrs, attrs...)
	}
}

func WithSpanProcessor(sp sdktrace.SpanProcessor) ProviderOption {
	return func(c *providerConfig) {
		c.spanProcessor = sp
	}
}

func NewProviderFromEnv(serviceName string, opts ...ProviderOption) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	sampleRatio, err := envFloatOrDefault(envSampleRatio, defaultSampleRatio)
	if err != nil {
		return nil, nil, err
	}

	cfg := providerConfig{
		protocol:    envOrDefault(envExporterProtocol, defaultExporterProtocol),
		endpoint:    os.Getenv(envExporterEndpoint),
		sampleRatio: sampleRatio,
	}

	if val := os.Getenv(envInsecure); val != "" {
		isInsecure := strings.EqualFold(val, "true") || val == "1"
		cfg.insecure = &isInsecure
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

func newExporter(cfg providerConfig) (sdktrace.SpanExporter, error) {
	ctx := context.Background()
	endpoint, isHTTP := sanitizeEndpoint(cfg.endpoint)

	// Determine insecure status:
	// 1. Explicit config choice (WithInsecure option or OTEL_EXPORTER_OTLP_INSECURE)
	// 2. OR scheme was explicitly "http://"
	// 3. Default to insecure for standard localhost/127.0.0.1 loopback testing endpoints if unspecified
	isInsecure := false
	if cfg.insecure != nil {
		isInsecure = *cfg.insecure
	} else if isHTTP || isLoopbackHost(endpoint) {
		isInsecure = true
	}

	switch strings.ToLower(cfg.protocol) {
	case "none":
		return nil, nil

	case "http", "http/protobuf":
		httpOpts := []otlptracehttp.Option{}
		if endpoint != "" {
			httpOpts = append(httpOpts, otlptracehttp.WithEndpoint(endpoint))
		}
		if isInsecure {
			httpOpts = append(httpOpts, otlptracehttp.WithInsecure())
		}
		exporter, err := otlptracehttp.New(ctx, httpOpts...)
		if err != nil {
			return nil, fmt.Errorf("otel: failed to build OTLP HTTP exporter: %w", err)
		}
		return exporter, nil

	case "grpc", "":
		grpcOpts := []otlptracegrpc.Option{}
		if endpoint != "" {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithEndpoint(endpoint))
		}
		if isInsecure {
			grpcOpts = append(grpcOpts, otlptracegrpc.WithInsecure())
		}
		exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
		if err != nil {
			return nil, fmt.Errorf("otel: failed to build OTLP gRPC exporter: %w", err)
		}
		return exporter, nil

	default:
		return nil, fmt.Errorf("otel: unsupported OTLP exporter protocol %q", cfg.protocol)
	}
}

func sanitizeEndpoint(endpoint string) (hostPort string, isHTTP bool) {
	if endpoint == "" {
		return "", false
	}
	if after, ok := strings.CutPrefix(endpoint, "http://"); ok {
		return after, true
	}
	if after, ok := strings.CutPrefix(endpoint, "https://"); ok {
		return after, false
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host, u.Scheme == "http"
	}
	return endpoint, false
}

func isLoopbackHost(endpoint string) bool {
	host := endpoint
	if h, _, err := netSplitHost(endpoint); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func netSplitHost(endpoint string) (string, string, error) {
	idx := strings.LastIndex(endpoint, ":")
	if idx < 0 {
		return endpoint, "", nil
	}
	return endpoint[:idx], endpoint[idx+1:], nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloatOrDefault(key string, fallback float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("otel: invalid value %q for %s: %w", v, key, err)
	}
	return f, nil
}

package otel_test

import (
	"context"
	"testing"

	modulexotel "github.com/mediusfy/modulex/otel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewProviderFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		opts    []modulexotel.ProviderOption
		wantErr bool
	}{
		{
			name: "none protocol builds a provider with no exporter",
			opts: []modulexotel.ProviderOption{
				modulexotel.WithExporterProtocol("none"),
			},
		},
		{
			name: "grpc protocol constructs without a live collector",
			opts: []modulexotel.ProviderOption{
				modulexotel.WithExporterProtocol("grpc"),
				modulexotel.WithExporterEndpoint("127.0.0.1:4317"),
			},
		},
		{
			name: "http protocol constructs without a live collector",
			opts: []modulexotel.ProviderOption{
				modulexotel.WithExporterProtocol("http"),
				modulexotel.WithExporterEndpoint("127.0.0.1:4318"),
			},
		},
		{
			name: "unsupported protocol returns an error",
			opts: []modulexotel.ProviderOption{
				modulexotel.WithExporterProtocol("carrier-pigeon"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tp, shutdown, err := modulexotel.NewProviderFromEnv("test-service", tt.opts...)
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, tp)
				assert.Nil(t, shutdown)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, tp)
			require.NotNil(t, shutdown)
			t.Cleanup(func() { _ = shutdown(context.Background()) })
		})
	}
}

func TestNewProviderFromEnv_ResourceAndSampling(t *testing.T) {
	sr := tracetest.NewSpanRecorder()

	tp, shutdown, err := modulexotel.NewProviderFromEnv("resource-test-service",
		modulexotel.WithExporterProtocol("none"),
		modulexotel.WithSpanProcessor(sr),
		modulexotel.WithResourceAttributes(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "resource-check")
	span.End()

	spans := sr.Ended()
	require.Len(t, spans, 1)

	found := false
	for _, attr := range spans[0].Resource().Attributes() {
		if string(attr.Key) == "service.name" {
			assert.Equal(t, "resource-test-service", attr.Value.AsString())
			found = true
		}
	}
	assert.True(t, found, "expected service.name resource attribute")
}

func TestNewProviderFromEnv_SampleRatio(t *testing.T) {
	tests := []struct {
		name          string
		ratio         float64
		wantRecording bool
	}{
		{name: "ratio 1.0 samples every span", ratio: 1.0, wantRecording: true},
		{name: "ratio 0.0 samples no spans", ratio: 0.0, wantRecording: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tp, shutdown, err := modulexotel.NewProviderFromEnv("sampling-test-service",
				modulexotel.WithExporterProtocol("none"),
				modulexotel.WithSampleRatio(tt.ratio),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = shutdown(context.Background()) })

			_, span := tp.Tracer("test").Start(context.Background(), "sample-check")
			defer span.End()

			assert.Equal(t, tt.wantRecording, span.IsRecording())
		})
	}
}

func TestNewProviderFromEnv_UsesEnvironmentDefaults(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "none")

	tp, shutdown, err := modulexotel.NewProviderFromEnv("env-default-service")
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, shutdown)
	_ = shutdown(context.Background())
}

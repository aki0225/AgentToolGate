package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

func InitTracerProvider(ctx context.Context, endpoint string) (*sdktrace.TracerProvider, error) {
	endpoint = NormalizeEndpoint(endpoint)
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("agenttoolgate-backend"),
		)),
	}

	if endpoint != "" {
		exporter, err := otlptracegrpc.New(
			ctx,
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("init OTLP trace exporter: %w", err)
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)))
	}

	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	return provider, nil
}

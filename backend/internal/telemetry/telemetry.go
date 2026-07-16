package telemetry

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "agenttoolgate/backend"

func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

func RecordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

func TraceID(ctx context.Context) string {
	traceID := trace.SpanContextFromContext(ctx).TraceID()
	if !traceID.IsValid() {
		return ""
	}
	return traceID.String()
}

func NormalizeEndpoint(endpoint string) string {
	return strings.TrimSpace(endpoint)
}

var ErrNoEndpoint = errors.New("otel exporter endpoint is not configured")

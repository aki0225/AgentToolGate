package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestToolCallWritesOpenTelemetryTraceIDAndSpans(t *testing.T) {
	exporter := installInMemoryTracer(t)

	srv, st, workspace := newGovernanceTestApp(t)
	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
	if resp.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.TraceID == "" {
		t.Fatalf("expected trace id in response: %+v", response)
	}
	if strings.HasPrefix(response.TraceID, "trace_") || len(response.TraceID) != 32 {
		t.Fatalf("trace id must be the OpenTelemetry trace id, got %q", response.TraceID)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 || calls[0].TraceID != response.TraceID {
		t.Fatalf("audit trace id must match response trace id, call=%+v response=%+v", calls, response)
	}

	spans := exporter.GetSpans()
	seen := map[string]bool{}
	for _, span := range spans {
		seen[span.Name] = true
	}
	for _, name := range []string{
		"agenttoolgate.request",
		"agenttoolgate.auth",
		"agenttoolgate.policy.evaluate",
		"agenttoolgate.approval.check",
		"agenttoolgate.connector.execute",
		"agenttoolgate.audit.write",
	} {
		if !seen[name] {
			t.Fatalf("expected span %q, got spans=%v", name, seen)
		}
	}
}

func TestHTTPRequestEmitsConnectorSpanAttributes(t *testing.T) {
	exporter := installInMemoryTracer(t)

	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(mockHTTP.Close)

	srv, _, _ := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})
	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+mockHTTP.URL+`/status?token=secret-value&view=summary"}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	span, ok := findSpanByName(exporter.GetSpans(), "connector.http.request")
	if !ok {
		t.Fatalf("expected connector.http.request span, got %+v", exporter.GetSpans())
	}
	if got := stringAttribute(span, "http.method"); got != http.MethodGet {
		t.Fatalf("expected http.method=GET, got %q", got)
	}
	if got := stringAttribute(span, "http.url"); !strings.Contains(got, "view=summary") || strings.Contains(got, "secret-value") {
		t.Fatalf("expected redacted http.url attribute, got %q", got)
	}
	if got := intAttribute(span, "http.status_code"); got != http.StatusOK {
		t.Fatalf("expected http.status_code=200, got %d", got)
	}
}

func TestMetricsEndpointReportsToolCalls(t *testing.T) {
	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "metricscheck", "Mock Metrics Check", "mock", "low", false)

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.metricscheck","arguments":{"message":"hello"}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from metrics, got %d body=%s", metricsRec.Code, metricsRec.Body.String())
	}

	if !matchesToolCallMetric(metricsRec.Body.String(), "mock.metricscheck", "success") {
		t.Fatalf("expected metrics output to include tool_call_total for mock.metricscheck, got %s", metricsRec.Body.String())
	}
}

func installInMemoryTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	provider := trace.NewTracerProvider(trace.WithSyncer(exporter))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})
	return exporter
}

func findSpanByName(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, span := range spans {
		if span.Name == name {
			return span, true
		}
	}
	return tracetest.SpanStub{}, false
}

func stringAttribute(span tracetest.SpanStub, key string) string {
	for _, item := range span.Attributes {
		if item.Key == attribute.Key(key) {
			return item.Value.AsString()
		}
	}
	return ""
}

func intAttribute(span tracetest.SpanStub, key string) int {
	for _, item := range span.Attributes {
		if item.Key == attribute.Key(key) {
			return int(item.Value.AsInt64())
		}
	}
	return 0
}

func matchesToolCallMetric(body, toolKey, status string) bool {
	quotedToolKey := regexp.QuoteMeta(toolKey)
	quotedStatus := regexp.QuoteMeta(status)
	patterns := []string{
		fmt.Sprintf(`(?m)^tool_call_total\{[^}]*tool_key="%s"[^}]*status="%s"[^}]*\} [0-9.]+$`, quotedToolKey, quotedStatus),
		fmt.Sprintf(`(?m)^tool_call_total\{[^}]*status="%s"[^}]*tool_key="%s"[^}]*\} [0-9.]+$`, quotedStatus, quotedToolKey),
	}
	for _, pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(body) {
			return true
		}
	}
	return false
}

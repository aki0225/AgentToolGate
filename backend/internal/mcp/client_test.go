package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameMCPOrigin(t *testing.T) {
	t.Parallel()

	base := mustMCPTestURL(t, "https://example.test/mcp")
	for _, tc := range []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "same path", candidate: "https://example.test/next", want: true},
		{name: "explicit default port", candidate: "https://example.test:443/next", want: true},
		{name: "different scheme", candidate: "http://example.test/mcp", want: false},
		{name: "different host", candidate: "https://other.test/mcp", want: false},
		{name: "different port", candidate: "https://example.test:8443/mcp", want: false},
		{name: "userinfo", candidate: "https://user@example.test/mcp", want: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameMCPOrigin(base, mustMCPTestURL(t, tc.candidate)); got != tc.want {
				t.Fatalf("sameMCPOrigin() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOutboundClientRejectsCrossOriginSSERedirect(t *testing.T) {
	t.Parallel()

	var targetRequests int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&targetRequests, 1)
	}))
	t.Cleanup(target.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusFound)
	}))
	t.Cleanup(source.Close)

	client := newMCPTestOutboundClient(t, source.URL+"/mcp", map[string]string{
		"Authorization": "Bearer synthetic-mcp-secret",
		"X-Api-Key":     "synthetic-mcp-key",
	}, time.Second)
	if _, err := client.SyncTools(context.Background()); err == nil || !strings.Contains(err.Error(), "original origin") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	if got := atomic.LoadInt32(&targetRequests); got != 0 {
		t.Fatalf("redirect target must receive zero requests, got %d", got)
	}
}

func TestOutboundClientRejectsCrossOriginAfterSameOriginRedirect(t *testing.T) {
	t.Parallel()

	var targetRequests int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&targetRequests, 1)
	}))
	t.Cleanup(target.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, target.URL+"/capture", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(source.Close)

	client := newMCPTestOutboundClient(t, source.URL+"/start", map[string]string{
		"Authorization": "Bearer synthetic-mcp-secret",
	}, time.Second)
	if _, err := client.SyncTools(context.Background()); err == nil || !strings.Contains(err.Error(), "original origin") {
		t.Fatalf("expected multi-hop cross-origin redirect rejection, got %v", err)
	}
	if got := atomic.LoadInt32(&targetRequests); got != 0 {
		t.Fatalf("cross-origin redirect target must receive zero requests, got %d", got)
	}
}

func TestOutboundClientRejectsCrossOriginPostRedirect(t *testing.T) {
	t.Parallel()

	var targetRequests int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetRequests, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)

	source := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request, messages chan string) {
		switch r.URL.Path {
		case "/mcp":
			serveMCPTestStream(w, r, "/rpc", messages)
		case "/rpc":
			var request JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode JSON-RPC request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Method == "initialize" {
				messages <- mcpTestResponse(request.ID, map[string]any{"protocolVersion": "2024-11-05"})
				w.WriteHeader(http.StatusAccepted)
				return
			}
			http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	})

	client := newMCPTestOutboundClient(t, source.URL+"/mcp", map[string]string{
		"Authorization": "Bearer synthetic-mcp-secret",
		"X-Api-Key":     "synthetic-mcp-key",
	}, 2*time.Second)
	if _, err := client.CallTool(context.Background(), "write", json.RawMessage(`{"value":"synthetic-body-secret"}`)); err == nil ||
		!strings.Contains(err.Error(), "original origin") {
		t.Fatalf("expected cross-origin POST redirect rejection, got %v", err)
	}
	if got := atomic.LoadInt32(&targetRequests); got != 0 {
		t.Fatalf("redirect target must receive zero requests, got %d", got)
	}
}

func TestOutboundClientAllowsSameOriginRedirects(t *testing.T) {
	t.Parallel()

	const (
		authorization = "Bearer synthetic-mcp-secret"
		apiKey        = "synthetic-mcp-key"
	)
	source := newMCPTestServer(t, func(w http.ResponseWriter, r *http.Request, messages chan string) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/nested/sse", http.StatusFound)
		case "/nested/sse":
			assertMCPTestHeaders(t, r, authorization, apiKey)
			serveMCPTestStream(w, r, "rpc-start", messages)
		case "/nested/rpc-start":
			http.Redirect(w, r, "/nested/rpc-final", http.StatusTemporaryRedirect)
		case "/nested/rpc-final":
			assertMCPTestHeaders(t, r, authorization, apiKey)
			var request JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode JSON-RPC request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch request.Method {
			case "initialize":
				messages <- mcpTestResponse(request.ID, map[string]any{"protocolVersion": "2024-11-05"})
			case "tools/list":
				messages <- mcpTestResponse(request.ID, map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "safe",
						"inputSchema": map[string]any{"type": "object"},
					}},
				})
			default:
				t.Errorf("unexpected JSON-RPC method %q", request.Method)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	})

	client := newMCPTestOutboundClient(t, source.URL+"/start", map[string]string{
		"Authorization": authorization,
		"X-Api-Key":     apiKey,
	}, 2*time.Second)
	tools, err := client.SyncTools(context.Background())
	if err != nil {
		t.Fatalf("sync tools through same-origin redirects: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

func TestMCPHTTPClientStopsAfterTenRedirects(t *testing.T) {
	t.Parallel()

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", atomic.LoadInt32(&requests)), http.StatusFound)
	}))
	t.Cleanup(server.Close)

	resp, err := newMCPTestHTTPClient(t, server.URL, time.Second).Get(server.URL + "/hop/0")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "redirect limit exceeded") {
		t.Fatalf("expected redirect limit error, got %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 10 {
		t.Fatalf("expected exactly 10 requests before rejection, got %d", got)
	}
}

func TestValidateMCPEndpointUsesEffectiveDefaultPort(t *testing.T) {
	t.Parallel()

	if err := validateMCPEndpoint(
		mustMCPTestURL(t, "https://example.test/mcp"),
		mustMCPTestURL(t, "https://example.test:443/rpc"),
	); err != nil {
		t.Fatalf("default HTTPS port must stay same-origin: %v", err)
	}
	if err := validateMCPEndpoint(
		mustMCPTestURL(t, "http://example.test:80/mcp"),
		mustMCPTestURL(t, "http://example.test/rpc"),
	); err != nil {
		t.Fatalf("default HTTP port must stay same-origin: %v", err)
	}
}

func TestReadSSEEventEnforcesLineLimit(t *testing.T) {
	t.Parallel()

	maxLine := "data: " + strings.Repeat("a", outboundMaxSSELineBytes-len("data: "))
	event, data, err := readSSEEvent(bufio.NewReader(strings.NewReader(maxLine + "\n\n")))
	if err != nil {
		t.Fatalf("read maximum-size SSE line: %v", err)
	}
	if event != "" || data != strings.TrimSpace(strings.TrimPrefix(maxLine, "data:")) {
		t.Fatalf("unexpected maximum-size SSE event: event=%q data length=%d", event, len(data))
	}

	oversizedLine := "data: " + strings.Repeat("a", outboundMaxSSELineBytes-len("data: ")+1)
	if _, _, err := readSSEEvent(bufio.NewReader(strings.NewReader(oversizedLine + "\n\n"))); err == nil ||
		!strings.Contains(err.Error(), "SSE line exceeds") {
		t.Fatalf("expected SSE line limit error, got %v", err)
	}
}

func TestReadSSEEventEnforcesEventLimit(t *testing.T) {
	t.Parallel()

	var maximumEvent strings.Builder
	remaining := outboundMaxSSEEventBytes - len("data: ok\n\n")
	for remaining > 0 {
		lineBytes := min(outboundMaxSSELineBytes+1, remaining)
		maximumEvent.WriteByte(':')
		maximumEvent.WriteString(strings.Repeat("a", lineBytes-2))
		maximumEvent.WriteByte('\n')
		remaining -= lineBytes
	}
	maximumEvent.WriteString("data: ok\n\n")
	if _, data, err := readSSEEvent(bufio.NewReader(strings.NewReader(maximumEvent.String()))); err != nil {
		t.Fatalf("read maximum-size SSE event: %v", err)
	} else if data != "ok" {
		t.Fatalf("unexpected maximum-size SSE event data %q", data)
	}

	var event strings.Builder
	line := ":" + strings.Repeat("a", 99) + "\n"
	for event.Len() <= outboundMaxSSEEventBytes {
		event.WriteString(line)
	}
	event.WriteByte('\n')

	if _, _, err := readSSEEvent(bufio.NewReader(strings.NewReader(event.String()))); err == nil ||
		!strings.Contains(err.Error(), "SSE event exceeds") {
		t.Fatalf("expected SSE event limit error, got %v", err)
	}
}

func TestDecodeJSONRPCResponseEnforcesResultLimit(t *testing.T) {
	t.Parallel()

	maxResult := `"` + strings.Repeat("a", outboundMaxJSONRPCResultBytes-2) + `"`
	if _, err := decodeOutboundJSONRPCResponse(`{"jsonrpc":"2.0","id":1,"result":`+maxResult+`}`, true); err != nil {
		t.Fatalf("decode maximum-size JSON-RPC result: %v", err)
	}

	oversizedResult := `"` + strings.Repeat("a", outboundMaxJSONRPCResultBytes-1) + `"`
	if _, err := decodeOutboundJSONRPCResponse(`{"jsonrpc":"2.0","id":1,"result":`+oversizedResult+`}`, true); err == nil ||
		!strings.Contains(err.Error(), "JSON-RPC result exceeds") {
		t.Fatalf("expected JSON-RPC result limit error, got %v", err)
	}
}

func TestBuildOutboundToolCallRequestEnforcesExactPOSTBodyLimit(t *testing.T) {
	t.Parallel()

	_, basePayload, err := buildOutboundToolCallRequest("bounded", json.RawMessage(`{"value":""}`))
	if err != nil {
		t.Fatalf("build base tools/call request: %v", err)
	}
	valueBytes := outboundMaxPOSTBodyBytes - len(basePayload)
	if valueBytes <= 0 {
		t.Fatalf("unexpected tools/call wrapper size %d", len(basePayload))
	}
	exactArguments := json.RawMessage(`{"value":"` + strings.Repeat("a", valueBytes) + `"}`)
	_, exactPayload, err := buildOutboundToolCallRequest("bounded", exactArguments)
	if err != nil {
		t.Fatalf("build exact-limit tools/call request: %v", err)
	}
	if len(exactPayload) != outboundMaxPOSTBodyBytes {
		t.Fatalf("exact tools/call payload size = %d, want %d", len(exactPayload), outboundMaxPOSTBodyBytes)
	}

	oversizedArguments := json.RawMessage(`{"value":"` + strings.Repeat("a", valueBytes+1) + `"}`)
	if _, _, err := buildOutboundToolCallRequest("bounded", oversizedArguments); err == nil ||
		!strings.Contains(err.Error(), "POST JSON body exceeds") {
		t.Fatalf("expected exact + 1 POST body rejection, got %v", err)
	}
}

func TestDecodeOutboundToolsEnforcesToolCountLimit(t *testing.T) {
	t.Parallel()

	tools := make([]map[string]any, outboundMaxTools)
	for index := range tools {
		tools[index] = map[string]any{
			"name":        fmt.Sprintf("tool-%d", index),
			"inputSchema": map[string]any{"type": "object"},
		}
	}
	decoded, err := decodeOutboundTools(map[string]any{"tools": tools})
	if err != nil {
		t.Fatalf("decode maximum tool count: %v", err)
	}
	if len(decoded) != outboundMaxTools {
		t.Fatalf("decoded %d tools, want %d", len(decoded), outboundMaxTools)
	}

	tools = append(tools, map[string]any{
		"name":        "oversized",
		"inputSchema": map[string]any{"type": "object"},
	})
	if _, err := decodeOutboundTools(map[string]any{"tools": tools}); err == nil ||
		!strings.Contains(err.Error(), "more than 256 tools") {
		t.Fatalf("expected tools/list count limit error, got %v", err)
	}
}

func TestDecodeOutboundToolsEnforcesInputSchemaLimit(t *testing.T) {
	t.Parallel()

	maxSchema := sizedMCPTestSchema(t, outboundMaxInputSchemaBytes)
	tools, err := decodeOutboundTools(map[string]any{
		"tools": []any{map[string]any{
			"name":        "maximum-schema",
			"inputSchema": json.RawMessage(maxSchema),
		}},
	})
	if err != nil {
		t.Fatalf("decode maximum-size inputSchema: %v", err)
	}
	if len(tools) != 1 || len(tools[0].InputSchema) != outboundMaxInputSchemaBytes {
		t.Fatalf("unexpected maximum-size inputSchema result: %+v", tools)
	}

	oversizedSchema := sizedMCPTestSchema(t, outboundMaxInputSchemaBytes+1)
	if _, err := decodeOutboundTools(map[string]any{
		"tools": []any{map[string]any{
			"name":        "oversized-schema",
			"inputSchema": json.RawMessage(oversizedSchema),
		}},
	}); err == nil || !strings.Contains(err.Error(), "inputSchema exceeds") {
		t.Fatalf("expected inputSchema limit error, got %v", err)
	}
}

func TestOutboundClientClosesSSEConnectionOnOversizedLine(t *testing.T) {
	t.Parallel()

	connectionClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.Repeat("a", outboundMaxSSELineBytes))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(connectionClosed)
	}))
	t.Cleanup(server.Close)

	client := newMCPTestOutboundClient(t, server.URL, nil, 2*time.Second)
	if _, err := client.SyncTools(context.Background()); err == nil || !strings.Contains(err.Error(), "SSE line exceeds") {
		t.Fatalf("expected SSE line limit error, got %v", err)
	}
	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		t.Fatal("oversized SSE line did not close the connection")
	}
}

func TestOutboundClientRejectsOversizedPostBodyBeforeOpeningSession(t *testing.T) {
	t.Parallel()

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, "oversized request must not open a session", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := newMCPTestOutboundClient(t, server.URL, nil, 2*time.Second)
	arguments := json.RawMessage(`{"value":"` + strings.Repeat("a", outboundMaxPOSTBodyBytes) + `"}`)
	if _, err := client.CallTool(context.Background(), "oversized", arguments); err == nil ||
		!strings.Contains(err.Error(), "POST JSON body exceeds") {
		t.Fatalf("expected POST body limit error, got %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("oversized POST body must fail before opening an SSE session, got %d requests", got)
	}
}

func TestOutboundClientRejectsOversizedPostResponseAndClosesSSEConnection(t *testing.T) {
	t.Parallel()

	connectionClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /rpc\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(connectionClosed)
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, strings.Repeat("a", outboundMaxPOSTResponseBytes+1))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	client := newMCPTestOutboundClient(t, server.URL, nil, 2*time.Second)
	if _, err := client.SyncTools(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "POST response body exceeds") {
		t.Fatalf("expected POST response body limit error, got %v", err)
	}
	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		t.Fatal("oversized POST response did not close the SSE connection")
	}
}

type mcpTestHandler func(http.ResponseWriter, *http.Request, chan string)

func newMCPTestServer(t *testing.T, handler mcpTestHandler) *httptest.Server {
	t.Helper()

	messages := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, messages)
	}))
	t.Cleanup(server.Close)
	return server
}

func serveMCPTestStream(w http.ResponseWriter, r *http.Request, endpoint string, messages <-chan string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
	flusher.Flush()

	for {
		select {
		case message := <-messages:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", message)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func mcpTestResponse(id any, result any) string {
	raw, _ := json.Marshal(JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
	return string(raw)
}

func assertMCPTestHeaders(t *testing.T, request *http.Request, authorization, apiKey string) {
	t.Helper()

	if request.Header.Get("Authorization") != authorization || request.Header.Get("X-Api-Key") != apiKey {
		t.Errorf("same-origin redirect lost MCP headers: authorization=%q apiKey=%q", request.Header.Get("Authorization"), request.Header.Get("X-Api-Key"))
	}
}

func mustMCPTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return parsed
}

func sizedMCPTestSchema(t *testing.T, size int) []byte {
	t.Helper()

	const prefix = `{"type":"object","description":"`
	const suffix = `"}`
	if size < len(prefix)+len(suffix) {
		t.Fatalf("schema size %d is too small", size)
	}
	return []byte(prefix + strings.Repeat("a", size-len(prefix)-len(suffix)) + suffix)
}

func newMCPTestOutboundClient(t *testing.T, baseURL string, headers map[string]string, timeout time.Duration) *OutboundClient {
	t.Helper()

	return NewOutboundClient(baseURL, headers, timeout, OutboundClientOptions{
		AllowedAuthorities: []string{mustMCPTestURL(t, baseURL).Host},
	})
}

func newMCPTestHTTPClient(t *testing.T, baseURL string, timeout time.Duration) *http.Client {
	t.Helper()

	return newMCPHTTPClient(timeout, OutboundClientOptions{
		AllowedAuthorities: []string{mustMCPTestURL(t, baseURL).Host},
	})
}

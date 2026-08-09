package mcp

import (
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

	client := NewOutboundClient(source.URL+"/mcp", map[string]string{
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

	client := NewOutboundClient(source.URL+"/start", map[string]string{
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

	client := NewOutboundClient(source.URL+"/mcp", map[string]string{
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

	client := NewOutboundClient(source.URL+"/start", map[string]string{
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

	resp, err := newMCPHTTPClient(time.Second).Get(server.URL + "/hop/0")
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

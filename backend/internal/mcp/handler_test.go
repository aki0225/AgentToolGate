package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestHandleInitializeReturnsMCPServerInfo(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	response := decodeMCPEvent(t, rec)

	if response.JSONRPC != "2.0" || response.ID != float64(1) {
		t.Fatalf("unexpected jsonrpc envelope: %+v", response)
	}
	result := response.Result.(map[string]any)
	if result["protocolVersion"] == "" {
		t.Fatalf("missing protocol version: %+v", result)
	}
	serverInfo := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "AgentToolGate" {
		t.Fatalf("unexpected server info: %+v", serverInfo)
	}
}

func TestStreamableHTTPHandleInitializeReturnsJSON(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}).StreamableHTTPHandler()

	rec := postMCPStreamableHTTPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	response := decodeMCPJSON(t, rec)

	if response.JSONRPC != "2.0" || response.ID != float64(1) {
		t.Fatalf("unexpected jsonrpc envelope: %+v", response)
	}
	result := response.Result.(map[string]any)
	serverInfo := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "AgentToolGate" {
		t.Fatalf("unexpected server info: %+v", serverInfo)
	}
}

func TestStreamableHTTPRejectsNonPostWithoutSSEFallback(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}).StreamableHTTPHandler()

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/mcp", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("expected Allow POST, got %q", rec.Header().Get("Allow"))
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("expected JSON content type, got %q", rec.Header().Get("Content-Type"))
			}
			response := decodeMCPJSON(t, rec)
			if response.Error == nil || response.Error.Code != ErrInvalidRequest {
				t.Fatalf("expected JSON-RPC method error, got %+v", response)
			}
		})
	}
}

func TestStreamableHTTPAcceptsInitializedNotificationWithoutToolExecution(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}).StreamableHTTPHandler()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for initialized notification, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("JSON-RPC notification must not return a response body, got %s", rec.Body.String())
	}
}

func TestStreamableHTTPIgnoresToolCallNotification(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}).StreamableHTTPHandler()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"must not execute"}}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for ignored notification, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStreamableHTTPReturnsStableJSONRPCErrors(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}).StreamableHTTPHandler()

	cases := []struct {
		name string
		body string
		code int
	}{
		{name: "invalid json", body: `{"jsonrpc":"2.0","id":`, code: ErrParse},
		{name: "unknown method", body: `{"jsonrpc":"2.0","id":3,"method":"unknown"}`, code: ErrMethodNotFound},
		{name: "invalid envelope", body: `{"jsonrpc":"1.0","id":4,"method":"initialize"}`, code: ErrInvalidRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := postMCPStreamableHTTPJSONRPC(t, handler, tc.body)
			response := decodeMCPJSON(t, rec)
			if response.Error == nil || response.Error.Code != tc.code {
				t.Fatalf("expected JSON-RPC error %d, got %+v", tc.code, response)
			}
		})
	}
}

func TestHandleToolsListReturnsOnlyEnabledTools(t *testing.T) {
	t.Parallel()

	st := newMCPTestStore(t)
	workspaceID := firstMCPTestWorkspaceID(t, st)
	disabled, err := st.CreateTool(context.Background(), model.CreateToolInput{
		WorkspaceID:      workspaceID,
		Namespace:        "mock",
		Name:             "disabled",
		DisplayName:      "Disabled",
		Description:      "Hidden from MCP clients",
		OperationType:    "mock",
		RiskLevel:        "low",
		InputSchemaJSON:  json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          false,
	})
	if err != nil {
		t.Fatalf("create disabled tool: %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("test setup failed: disabled tool is enabled")
	}

	handler := NewHandler(Config{
		Store: st,
		ResolveContext: func(context.Context) (RequestContext, error) {
			return RequestContext{WorkspaceID: workspaceID}, nil
		},
		CallTool: neverCallMCPTool(t),
		Logger:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":"tools","method":"tools/list"}`)
	response := decodeMCPEvent(t, rec)
	result := response.Result.(map[string]any)
	tools := result["tools"].([]any)

	if len(tools) == 0 {
		t.Fatalf("expected enabled tools")
	}
	for _, item := range tools {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		if name == "mock.disabled" {
			t.Fatalf("disabled tool leaked into tools/list: %+v", tools)
		}
		if name == "mock.echo" {
			schema := tool["inputSchema"].(map[string]any)
			if schema["type"] != "object" {
				t.Fatalf("expected input schema from registry, got %+v", schema)
			}
		}
	}
}

func TestHandleToolsCallMapsApprovalRequiredToJSONRPCError(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool: func(context.Context, ToolCallRequest) (ToolCallResult, error) {
			return ToolCallResult{
				Status:         "approval_required",
				CallID:         "call-1",
				ApprovalID:     "approval-1",
				ApprovalStatus: "pending",
				Message:        "This tool call requires approval.",
			}, nil
		},
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"http.request","arguments":{"method":"POST","url":"http://example.test/items"}}}`)
	response := decodeMCPEvent(t, rec)

	if response.Error == nil {
		t.Fatalf("expected approval_required MCP error, got %+v", response)
	}
	if response.Error.Code != ErrApprovalRequired || !strings.Contains(response.Error.Message, "审批") {
		t.Fatalf("unexpected approval error: %+v", response.Error)
	}
	data := response.Error.Data.(map[string]any)
	if data["approvalId"] != "approval-1" || data["callId"] != "call-1" {
		t.Fatalf("missing approval data: %+v", data)
	}
}

func TestHandleToolsCallReturnsToolResultAsMCPContent(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool: func(_ context.Context, req ToolCallRequest) (ToolCallResult, error) {
			if req.Tool != "mock.echo" {
				t.Fatalf("unexpected tool name: %s", req.Tool)
			}
			if string(req.Arguments) != `{"message":"hello"}` {
				t.Fatalf("unexpected arguments: %s", req.Arguments)
			}
			return ToolCallResult{
				Status: "success",
				Result: map[string]any{"ok": true},
				CallID: "call-1",
			}, nil
		},
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"hello"}}}`)
	response := decodeMCPEvent(t, rec)
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	result := response.Result.(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected single content item, got %+v", content)
	}
	item := content[0].(map[string]any)
	if item["type"] != "text" || !strings.Contains(item["text"].(string), `"ok":true`) {
		t.Fatalf("unexpected MCP content: %+v", item)
	}
}

func TestHandleToolsCallRedactsInternalErrors(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool: func(context.Context, ToolCallRequest) (ToolCallResult, error) {
			return ToolCallResult{}, errors.New("upstream http://10.0.0.5:9999/private failed with Authorization header")
		},
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"http.request","arguments":{"method":"GET","url":"http://internal.test/private"}}}`)
	response := decodeMCPEvent(t, rec)

	if response.Error == nil {
		t.Fatalf("expected JSON-RPC error, got %+v", response)
	}
	if response.Error.Code != ErrInternal || response.Error.Message != "MCP tool call failed" {
		t.Fatalf("expected generic internal error, got %+v", response.Error)
	}
	for _, leaked := range []string{"10.0.0.5", "9999", "private", "Authorization"} {
		if strings.Contains(response.Error.Message, leaked) {
			t.Fatalf("JSON-RPC error leaked %q: %+v", leaked, response.Error)
		}
	}
}

func TestHandleToolsCallKeepsBadRequestErrorsActionable(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool: func(context.Context, ToolCallRequest) (ToolCallResult, error) {
			return ToolCallResult{}, errors.New("bad request: url is required")
		},
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	rec := postMCPJSONRPC(t, handler, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"http.request","arguments":{}}}`)
	response := decodeMCPEvent(t, rec)

	if response.Error == nil || response.Error.Code != ErrInvalidParams || response.Error.Message != "bad request: url is required" {
		t.Fatalf("expected actionable bad-request error, got %+v", response.Error)
	}
}

func TestHandleSSEGetReturnsEndpointEvent(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:          newMCPTestStore(t),
		ResolveContext: fixedMCPContext,
		CallTool:       neverCallMCPTool(t),
		Logger:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "event: endpoint") || !strings.Contains(rec.Body.String(), "/mcp/sse?sessionId=") {
		t.Fatalf("expected endpoint event, got %q", rec.Body.String())
	}
}

func TestHandleSSEGetRejectsUnauthorizedSessionBeforeOpeningStream(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Store:                 newMCPTestStore(t),
		ResolveContext:        fixedMCPContext,
		ResolveSessionContext: func(context.Context) (RequestContext, error) { return RequestContext{}, errors.New("forbidden") },
		CallTool:              neverCallMCPTool(t),
		Logger:                slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unauthorized SSE session must not open stream, content-type=%q", rec.Header().Get("Content-Type"))
	}
}

func postMCPJSONRPC(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp/sse", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", rec.Header().Get("Content-Type"))
	}
	return rec
}

func postMCPStreamableHTTPJSONRPC(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON content type, got %q", rec.Header().Get("Content-Type"))
	}
	return rec
}

func decodeMCPEvent(t *testing.T, rec *httptest.ResponseRecorder) JSONRPCResponse {
	t.Helper()

	var dataLine string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("missing SSE data line: %q", rec.Body.String())
	}

	var response JSONRPCResponse
	if err := json.Unmarshal([]byte(dataLine), &response); err != nil {
		t.Fatalf("decode json-rpc response: %v data=%s", err, dataLine)
	}
	return response
}

func decodeMCPJSON(t *testing.T, rec *httptest.ResponseRecorder) JSONRPCResponse {
	t.Helper()

	var response JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode json-rpc response: %v body=%s", err, rec.Body.String())
	}
	return response
}

func newMCPTestStore(t *testing.T) *store.MemoryStore {
	t.Helper()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	return st
}

func fixedMCPContext(context.Context) (RequestContext, error) {
	return RequestContext{WorkspaceID: "workspace-1"}, nil
}

func firstMCPTestWorkspaceID(t *testing.T, st store.Store) string {
	t.Helper()

	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) == 0 {
		t.Fatalf("expected bootstrap workspace")
	}
	return workspaces[0].ID
}

func neverCallMCPTool(t *testing.T) ToolCaller {
	t.Helper()
	return func(context.Context, ToolCallRequest) (ToolCallResult, error) {
		t.Fatalf("tool caller should not be invoked")
		return ToolCallResult{}, nil
	}
}

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestMCPToolsCallUsesGovernedAuditFlow(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newMCPAppTestServer(t, config.Config{})
	router := srv.Router()

	resp := postMCPAppJSONRPC(t, router, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"hello"}}}`)
	response := decodeMCPAppEvent(t, resp.Body.String())
	if response.Error != nil {
		t.Fatalf("unexpected MCP error: %+v", response.Error)
	}
	result := response.Result.(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), `"message":"hello"`) {
		t.Fatalf("unexpected MCP tool result: %+v", result)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(calls))
	}
	if calls[0].ToolKey != "mock.echo" || calls[0].Status != "success" || calls[0].PolicyDecision != "allow" {
		t.Fatalf("unexpected governed audit call: %+v", calls[0])
	}
}

func TestMCPToolsCallApprovalRequiredReturnsErrorAndCreatesApproval(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	cfg := config.Config{
		HTTPAllowedHosts:     []string{mustMCPAppURLHost(t, mockHTTP.URL)},
		HTTPAllowedMethods:   []string{http.MethodGet, http.MethodPost},
		HTTPTimeoutMs:        3000,
		HTTPMaxResponseBytes: 65536,
	}
	srv, st, workspace := newMCPAppTestServer(t, cfg)
	router := srv.Router()

	resp := postMCPAppJSONRPC(t, router, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello"}}}}`)
	response := decodeMCPAppEvent(t, resp.Body.String())
	if response.Error == nil {
		t.Fatalf("expected MCP approval error, got %+v", response)
	}
	if !strings.Contains(response.Error.Message, "审批") {
		t.Fatalf("approval error should guide user to console approval, got %+v", response.Error)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("approval_required MCP call must not execute target")
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(calls))
	}
	call := calls[0]
	if call.ToolKey != "http.request" || call.Status != "approval_required" || call.ApprovalStatus != "pending" {
		t.Fatalf("unexpected approval audit: %+v", call)
	}
	if call.ApprovalID == "" {
		t.Fatalf("approval id missing from audit call: %+v", call)
	}
}

func TestMCPStreamableHTTPInitializeAndToolsList(t *testing.T) {
	t.Parallel()

	srv, _, _ := newMCPAppTestServer(t, config.Config{})
	router := srv.Router()

	initialize := decodeMCPAppJSON(t, postMCPAppStreamableHTTPJSONRPC(t, router, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	if initialize.Error != nil {
		t.Fatalf("initialize returned MCP error: %+v", initialize.Error)
	}
	serverInfo := initialize.Result.(map[string]any)["serverInfo"].(map[string]any)
	if serverInfo["name"] != "AgentToolGate" {
		t.Fatalf("unexpected server info: %+v", serverInfo)
	}

	list := decodeMCPAppJSON(t, postMCPAppStreamableHTTPJSONRPC(t, router, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if list.Error != nil {
		t.Fatalf("tools/list returned MCP error: %+v", list.Error)
	}
	if !mcpAppToolsListContains(list.Result, "mock.echo") {
		t.Fatalf("tools/list did not expose mock.echo: %+v", list.Result)
	}
}

func TestMCPStreamableHTTPUsesDefaultWorkspaceWithoutHeaderInLocalMode(t *testing.T) {
	t.Parallel()

	srv, _, _ := newMCPAppTestServer(t, config.Config{})
	router := srv.Router()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	response := decodeMCPAppJSON(t, rec)
	if response.Error != nil {
		t.Fatalf("local default workspace should allow tools/list without header, got %+v", response.Error)
	}
	if !mcpAppToolsListContains(response.Result, "mock.echo") {
		t.Fatalf("tools/list did not expose mock.echo without workspace header: %+v", response.Result)
	}
}

func TestMCPStreamableHTTPToolsCallUsesGovernedAuditFlow(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newMCPAppTestServer(t, config.Config{})
	router := srv.Router()

	response := decodeMCPAppJSON(t, postMCPAppStreamableHTTPJSONRPC(t, router, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"hello from streamable"}}}`))
	if response.Error != nil {
		t.Fatalf("unexpected MCP error: %+v", response.Error)
	}
	result := response.Result.(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), `"message":"hello from streamable"`) {
		t.Fatalf("unexpected MCP tool result: %+v", result)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(calls))
	}
	if calls[0].ToolKey != "mock.echo" || calls[0].Status != "success" || calls[0].PolicyDecision != "allow" {
		t.Fatalf("unexpected governed audit call: %+v", calls[0])
	}
}

func TestMCPStreamableHTTPWriteToolRequiresApprovalWithoutTouchingUpstream(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	cfg := config.Config{
		HTTPAllowedHosts:     []string{mustMCPAppURLHost(t, mockHTTP.URL)},
		HTTPAllowedMethods:   []string{http.MethodGet, http.MethodPost},
		HTTPTimeoutMs:        3000,
		HTTPMaxResponseBytes: 65536,
	}
	srv, st, workspace := newMCPAppTestServer(t, cfg)
	router := srv.Router()

	response := decodeMCPAppJSON(t, postMCPAppStreamableHTTPJSONRPC(t, router, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello"}}}}`))
	if response.Error == nil {
		t.Fatalf("expected MCP approval error, got %+v", response)
	}
	if !strings.Contains(response.Error.Message, "审批") {
		t.Fatalf("approval error should guide user to console approval, got %+v", response.Error)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("approval_required MCP call must not execute target")
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(calls))
	}
	call := calls[0]
	if call.ToolKey != "http.request" || call.Status != "approval_required" || call.ApprovalStatus != "pending" {
		t.Fatalf("unexpected approval audit: %+v", call)
	}
	if call.ApprovalID == "" {
		t.Fatalf("approval id missing from audit call: %+v", call)
	}
}

func TestMCPStreamableHTTPReservedMethodsDoNotFallBackToSPA(t *testing.T) {
	t.Parallel()

	srv, _, _ := newMCPAppTestServer(t, config.Config{})
	router := srv.Router()

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/mcp", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405, got %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("expected Allow POST, got %q", rec.Header().Get("Allow"))
			}
			if strings.Contains(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "<!doctype html") {
				t.Fatalf("/mcp was swallowed by SPA fallback: content-type=%q body=%s", rec.Header().Get("Content-Type"), rec.Body.String())
			}
		})
	}
}

func TestMCPStreamableHTTPDoesNotUseOIDCQueryWorkspaceFallback(t *testing.T) {
	t.Parallel()

	oidcApp := &App{cfg: config.Config{AuthMode: "oidc"}}
	req := httptest.NewRequest(http.MethodPost, "/mcp?workspaceOrgId=query-org", nil)
	if got := oidcApp.requestedWorkspaceOrgID(req); got != "" {
		t.Fatalf("OIDC /mcp 不能使用 query workspace fallback，got %q", got)
	}
}

func TestMCPInboundSSESessionSmokeCoversListCallAndAudit(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newMCPAppTestServer(t, config.Config{})
	httpServer := httptest.NewServer(srv.Router())
	t.Cleanup(httpServer.Close)

	client := httpServer.Client()
	client.Timeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/mcp/sse", nil)
	if err != nil {
		t.Fatalf("new sse request: %v", err)
	}
	sseReq.Header.Set("X-Workspace-Org-Id", "local-org")
	sseResp, err := client.Do(sseReq)
	if err != nil {
		t.Fatalf("open mcp sse session: %v", err)
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected SSE 200, got %d", sseResp.StatusCode)
	}
	if !strings.HasPrefix(sseResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", sseResp.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(sseResp.Body)
	endpoint := readMCPAppSSEEventData(t, reader, "endpoint")
	if !strings.HasPrefix(endpoint, "/mcp/sse?sessionId=") {
		t.Fatalf("unexpected MCP session endpoint: %q", endpoint)
	}
	endpointURL := httpServer.URL + endpoint

	postMCPAppSessionJSONRPC(t, client, endpointURL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	initialize := decodeMCPAppEventData(t, readMCPAppSSEEventData(t, reader, "message"))
	if initialize.Error != nil {
		t.Fatalf("initialize returned MCP error: %+v", initialize.Error)
	}
	serverInfo := initialize.Result.(map[string]any)["serverInfo"].(map[string]any)
	if serverInfo["name"] != "AgentToolGate" {
		t.Fatalf("unexpected server info: %+v", serverInfo)
	}

	postMCPAppSessionJSONRPC(t, client, endpointURL, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := decodeMCPAppEventData(t, readMCPAppSSEEventData(t, reader, "message"))
	if list.Error != nil {
		t.Fatalf("tools/list returned MCP error: %+v", list.Error)
	}
	if !mcpAppToolsListContains(list.Result, "mock.echo") {
		t.Fatalf("tools/list did not expose mock.echo: %+v", list.Result)
	}

	postMCPAppSessionJSONRPC(t, client, endpointURL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mock.echo","arguments":{"message":"hello from mcp smoke"}}}`)
	call := decodeMCPAppEventData(t, readMCPAppSSEEventData(t, reader, "message"))
	if call.Error != nil {
		t.Fatalf("tools/call returned MCP error: %+v", call.Error)
	}
	callResult := call.Result.(map[string]any)
	if metadata := callResult["metadata"].(map[string]any); metadata["status"] != "success" {
		t.Fatalf("expected success metadata, got %+v", metadata)
	}
	content := callResult["content"].([]any)
	if len(content) != 1 || !strings.Contains(content[0].(map[string]any)["text"].(string), "hello from mcp smoke") {
		t.Fatalf("unexpected tools/call content: %+v", content)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one audit row, got %d", len(calls))
	}
	if calls[0].ToolKey != "mock.echo" || calls[0].Status != "success" || calls[0].PolicyDecision != "allow" {
		t.Fatalf("unexpected audit row after MCP smoke: %+v", calls[0])
	}
}

type mcpAppJSONRPCResponse struct {
	JSONRPC string              `json:"jsonrpc"`
	ID      any                 `json:"id,omitempty"`
	Result  any                 `json:"result,omitempty"`
	Error   *mcpAppJSONRPCError `json:"error,omitempty"`
}

type mcpAppJSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func newMCPAppTestServer(t *testing.T, overrides config.Config) (*App, store.Store, model.Workspace) {
	t.Helper()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("expected one workspace, got %d", len(workspaces))
	}

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
		HTTPAllowedMethods:    []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete},
		HTTPTimeoutMs:         3000,
		HTTPMaxResponseBytes:  65536,
	}
	if len(overrides.HTTPAllowedHosts) > 0 {
		cfg.HTTPAllowedHosts = overrides.HTTPAllowedHosts
	}
	if len(overrides.HTTPAllowedMethods) > 0 {
		cfg.HTTPAllowedMethods = overrides.HTTPAllowedMethods
	}
	if overrides.HTTPTimeoutMs > 0 {
		cfg.HTTPTimeoutMs = overrides.HTTPTimeoutMs
	}
	if overrides.HTTPMaxResponseBytes > 0 {
		cfg.HTTPMaxResponseBytes = overrides.HTTPMaxResponseBytes
	}

	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	appStore := approvalReviewableTestStore(st)
	return New(cfg, appStore, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), appStore, workspaces[0]
}

func postMCPAppJSONRPC(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
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

func postMCPAppStreamableHTTPJSONRPC(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Org-Id", "local-org")
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

func decodeMCPAppJSON(t *testing.T, rec *httptest.ResponseRecorder) mcpAppJSONRPCResponse {
	t.Helper()

	var response mcpAppJSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP JSON response: %v body=%s", err, rec.Body.String())
	}
	return response
}

func decodeMCPAppEvent(t *testing.T, body string) mcpAppJSONRPCResponse {
	t.Helper()

	var dataLine string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("missing SSE data line: %q", body)
	}
	var response mcpAppJSONRPCResponse
	if err := json.Unmarshal([]byte(dataLine), &response); err != nil {
		t.Fatalf("decode MCP response: %v data=%s", err, dataLine)
	}
	return response
}

func decodeMCPAppEventData(t *testing.T, data string) mcpAppJSONRPCResponse {
	t.Helper()

	var response mcpAppJSONRPCResponse
	if err := json.Unmarshal([]byte(data), &response); err != nil {
		t.Fatalf("decode MCP event data: %v data=%s", err, data)
	}
	return response
}

func postMCPAppSessionJSONRPC(t *testing.T, client *http.Client, endpointURL, body string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, endpointURL, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new mcp session request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Org-Id", "local-org")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post MCP JSON-RPC to session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202 accepted from MCP session endpoint, got %d body=%s", resp.StatusCode, raw)
	}
}

func readMCPAppSSEEventData(t *testing.T, reader *bufio.Reader, wantEvent string) string {
	t.Helper()

	event := ""
	dataLines := make([]string, 0, 1)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read MCP SSE event %q: %v", wantEvent, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event == wantEvent {
				return strings.Join(dataLines, "\n")
			}
			event = ""
			dataLines = dataLines[:0]
			continue
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			event = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			dataLines = append(dataLines, value)
		}
	}
}

func mcpAppToolsListContains(result any, toolName string) bool {
	resultMap, ok := result.(map[string]any)
	if !ok {
		return false
	}
	tools, ok := resultMap["tools"].([]any)
	if !ok {
		return false
	}
	for _, item := range tools {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if tool["name"] == toolName {
			return true
		}
	}
	return false
}

func mustMCPAppURLHost(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed.Host
}

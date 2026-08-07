package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
)

func TestEvaluateMCPInboundAtURLPerformsHandshakeAndToolsList(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	var workspaceHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		methods = append(methods, request.Method)
		workspaceHeaders = append(workspaceHeaders, r.Header.Get("X-Workspace-Org-Id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "mock.echo", "description": "synthetic"},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}))
	defer server.Close()

	result, err := evaluateMCPInboundAtURL(
		context.Background(),
		server.URL,
		"local-org",
		2*time.Second,
		operations.GuardInput{ToolName: "mcp.tools/list"},
		time.Now(),
	)
	if err != nil {
		t.Fatalf("evaluateMCPInboundAtURL() error = %v", err)
	}
	if result.Decision != model.DecisionAllow ||
		!result.Silent ||
		result.RiskLevel != "low" ||
		result.Category != "mcp_readonly" ||
		result.Duration <= 0 {
		t.Fatalf("MCP Inbound 结果异常：%+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(methods, ",") != "initialize,tools/list" {
		t.Fatalf("MCP 请求顺序异常：%v", methods)
	}
	for _, header := range workspaceHeaders {
		if header != "local-org" {
			t.Fatalf("workspace header=%q", header)
		}
	}
}

func TestEvaluateMCPInboundAtURLFailsClosed(t *testing.T) {
	t.Run("unsupported tool", func(t *testing.T) {
		if _, err := evaluateMCPInboundAtURL(
			context.Background(),
			"http://127.0.0.1:1",
			"local-org",
			time.Second,
			operations.GuardInput{ToolName: "mcp.tools/call"},
			time.Now(),
		); err == nil {
			t.Fatal("不支持的 MCP operation 必须失败")
		}
	})

	t.Run("non loopback", func(t *testing.T) {
		if _, err := evaluateMCPInboundAtURL(
			context.Background(),
			"https://example.com",
			"local-org",
			time.Second,
			operations.GuardInput{ToolName: "mcp.tools/list"},
			time.Now(),
		); err == nil || !strings.Contains(err.Error(), "不安全") {
			t.Fatalf("非 loopback MCP endpoint 必须被拒绝，err=%v", err)
		}
	})

	t.Run("missing mock echo", func(t *testing.T) {
		server := newMCPFailureServer(t, false)
		if _, err := evaluateMCPInboundAtURL(
			context.Background(),
			server.URL,
			"local-org",
			2*time.Second,
			operations.GuardInput{ToolName: "mcp.tools/list"},
			time.Now(),
		); err == nil || !strings.Contains(err.Error(), "mock.echo") {
			t.Fatalf("缺少 mock.echo 必须失败，err=%v", err)
		}
	})

	t.Run("unsupported protocol version", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"result": map[string]any{
					"protocolVersion": "2099-01-01",
				},
			})
		}))
		defer server.Close()
		if _, err := evaluateMCPInboundAtURL(
			context.Background(),
			server.URL,
			"local-org",
			2*time.Second,
			operations.GuardInput{ToolName: "mcp.tools/list"},
			time.Now(),
		); err == nil || !strings.Contains(err.Error(), "协议版本") {
			t.Fatalf("不受支持的 MCP 协议版本必须失败，err=%v", err)
		}
	})

	t.Run("rpc error does not echo message", func(t *testing.T) {
		const sensitiveMessage = "token=should-not-be-returned"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      request["id"],
				"error": map[string]any{
					"code":    -32000,
					"message": sensitiveMessage,
				},
			})
		}))
		defer server.Close()
		_, err := evaluateMCPInboundAtURL(
			context.Background(),
			server.URL,
			"local-org",
			2*time.Second,
			operations.GuardInput{ToolName: "mcp.tools/list"},
			time.Now(),
		)
		if err == nil || strings.Contains(err.Error(), sensitiveMessage) {
			t.Fatalf("JSON-RPC 错误必须脱敏，err=%v", err)
		}
	})

	t.Run("mismatched response id", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "unexpected",
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
				},
			})
		}))
		defer server.Close()
		if _, err := evaluateMCPInboundAtURL(
			context.Background(),
			server.URL,
			"local-org",
			2*time.Second,
			operations.GuardInput{ToolName: "mcp.tools/list"},
			time.Now(),
		); err == nil || !strings.Contains(err.Error(), "响应 ID") {
			t.Fatalf("JSON-RPC 响应 ID 不匹配必须失败，err=%v", err)
		}
	})

	t.Run("http error status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		if err := evaluateMCPError(t, server.URL); err == nil || !strings.Contains(err.Error(), "状态码 503") {
			t.Fatalf("MCP HTTP 错误状态必须失败，err=%v", err)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "{")
		}))
		defer server.Close()
		if err := evaluateMCPError(t, server.URL); err == nil || !strings.Contains(err.Error(), "JSON-RPC 响应失败") {
			t.Fatalf("畸形 MCP JSON 必须失败，err=%v", err)
		}
	})

	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxMCPResponseBytes+1))
		}))
		defer server.Close()
		if err := evaluateMCPError(t, server.URL); err == nil || !strings.Contains(err.Error(), "超过限制") {
			t.Fatalf("超大 MCP 响应必须失败，err=%v", err)
		}
	})

	t.Run("invalid jsonrpc version", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "1.0",
				"id":      "initialize",
				"result":  map[string]any{"protocolVersion": "2024-11-05"},
			})
		}))
		defer server.Close()
		if err := evaluateMCPError(t, server.URL); err == nil || !strings.Contains(err.Error(), "JSON-RPC 版本") {
			t.Fatalf("无效 JSON-RPC 版本必须失败，err=%v", err)
		}
	})

	t.Run("missing result", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      "initialize",
			})
		}))
		defer server.Close()
		if err := evaluateMCPError(t, server.URL); err == nil || !strings.Contains(err.Error(), "缺少 result") {
			t.Fatalf("缺少 MCP result 必须失败，err=%v", err)
		}
	})
}

func evaluateMCPError(t *testing.T, baseURL string) error {
	t.Helper()
	_, err := evaluateMCPInboundAtURL(
		context.Background(),
		baseURL,
		"local-org",
		2*time.Second,
		operations.GuardInput{ToolName: "mcp.tools/list"},
		time.Now(),
	)
	return err
}

func newMCPFailureServer(t *testing.T, includeMockEcho bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		method := fmt.Sprint(request["method"])
		result := map[string]any{}
		switch method {
		case "initialize":
			result["protocolVersion"] = "2024-11-05"
		case "tools/list":
			tools := []map[string]any{{"name": "other.tool"}}
			if includeMockEcho {
				tools = append(tools, map[string]any{"name": "mock.echo"})
			}
			result["tools"] = tools
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      request["id"],
			"result":  result,
		})
	}))
}

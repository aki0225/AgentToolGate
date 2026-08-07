package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
)

const maxMCPResponseBytes = 1 << 20

type mcpRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (g *GuardCLI) evaluateMCPInbound(
	ctx context.Context,
	input operations.GuardInput,
	startedAt time.Time,
) (Evaluation, error) {
	if g.mcpRuntime == nil {
		return Evaluation{}, fmt.Errorf("MCP Inbound runtime 未启用")
	}
	return evaluateMCPInboundAtURL(
		ctx,
		g.mcpRuntime.BaseURL(),
		g.mcpWorkspaceOrgID,
		g.timeout,
		input,
		startedAt,
	)
}

func evaluateMCPInboundAtURL(
	ctx context.Context,
	baseURL,
	workspaceOrgID string,
	timeout time.Duration,
	input operations.GuardInput,
	startedAt time.Time,
) (Evaluation, error) {
	if strings.TrimSpace(input.ToolName) != "mcp.tools/list" {
		return Evaluation{}, fmt.Errorf("MCP Inbound 当前只支持 mcp.tools/list")
	}
	if err := mockserver.ValidateLoopbackURL(strings.TrimRight(baseURL, "/") + "/mcp"); err != nil {
		return Evaluation{}, fmt.Errorf("MCP Inbound endpoint 不安全：%w", err)
	}
	initializeResponse, err := postMCP(ctx, baseURL, workspaceOrgID, timeout, map[string]any{
		"jsonrpc": "2.0",
		"id":      "initialize",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "atg-eval",
				"version": "v1",
			},
		},
	}, "initialize")
	if err != nil {
		return Evaluation{}, fmt.Errorf("MCP initialize 失败：%w", err)
	}
	var initializeResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(initializeResponse.Result, &initializeResult); err != nil ||
		initializeResult.ProtocolVersion != "2024-11-05" {
		return Evaluation{}, fmt.Errorf("MCP initialize 返回不受支持的协议版本")
	}
	listResponse, err := postMCP(ctx, baseURL, workspaceOrgID, timeout, map[string]any{
		"jsonrpc": "2.0",
		"id":      "tools-list",
		"method":  "tools/list",
	}, "tools-list")
	if err != nil {
		return Evaluation{}, fmt.Errorf("MCP tools/list 失败：%w", err)
	}
	var listResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listResponse.Result, &listResult); err != nil {
		return Evaluation{}, fmt.Errorf("解析 MCP tools/list 结果失败")
	}
	foundMockEcho := false
	for _, tool := range listResult.Tools {
		if strings.TrimSpace(tool.Name) == "mock.echo" {
			foundMockEcho = true
			break
		}
	}
	if !foundMockEcho {
		return Evaluation{}, fmt.Errorf("MCP tools/list 未返回 mock.echo")
	}
	return Evaluation{
		Decision:  model.DecisionAllow,
		RiskLevel: "low",
		Silent:    true,
		Reason:    "MCP Inbound 只读握手与工具发现成功",
		Signals: []string{
			"mcp_initialize_success",
			"mcp_tools_list_success",
		},
		Category: "mcp_readonly",
		Duration: time.Since(startedAt),
	}, nil
}

func postMCP(
	ctx context.Context,
	baseURL,
	workspaceOrgID string,
	timeout time.Duration,
	payload any,
	expectedID string,
) (mcpRPCResponse, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return mcpRPCResponse{}, fmt.Errorf("编码 MCP 请求失败")
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/mcp",
		bytes.NewReader(raw),
	)
	if err != nil {
		return mcpRPCResponse{}, fmt.Errorf("创建 MCP 请求失败")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if workspaceOrgID != "" {
		request.Header.Set("X-Workspace-Org-Id", workspaceOrgID)
	}
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("MCP Inbound 请求不允许重定向")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		if requestContext.Err() != nil {
			return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 请求超时或被取消")
		}
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 请求失败")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMCPResponseBytes+1))
	if err != nil {
		return mcpRPCResponse{}, fmt.Errorf("读取 MCP Inbound 响应失败")
	}
	if len(body) > maxMCPResponseBytes {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 响应超过限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 返回状态码 %d", response.StatusCode)
	}
	var rpcResponse mcpRPCResponse
	if err := decodeStrictJSON(body, &rpcResponse); err != nil {
		return mcpRPCResponse{}, fmt.Errorf("解析 MCP Inbound JSON-RPC 响应失败")
	}
	if rpcResponse.JSONRPC != "2.0" {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound JSON-RPC 版本无效")
	}
	var responseID string
	if err := json.Unmarshal(rpcResponse.ID, &responseID); err != nil || responseID != expectedID {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound JSON-RPC 响应 ID 不匹配")
	}
	if rpcResponse.Error != nil {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 返回 JSON-RPC 错误 code=%d", rpcResponse.Error.Code)
	}
	if len(bytes.TrimSpace(rpcResponse.Result)) == 0 {
		return mcpRPCResponse{}, fmt.Errorf("MCP Inbound 响应缺少 result")
	}
	return rpcResponse, nil
}

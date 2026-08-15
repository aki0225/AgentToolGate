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
)

type governanceAPI struct {
	baseURL           string
	workspaceOrgID    string
	sensitiveValue    string
	sensitiveDetected bool
	client            *http.Client
}

type governanceErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type governanceToolCallResponse struct {
	Status         string `json:"status"`
	CallID         string `json:"callId"`
	ApprovalID     string `json:"approvalId"`
	ApprovalStatus string `json:"approvalStatus"`
}

type governanceAgentGuardResponse struct {
	Decision       string `json:"decision"`
	ApprovalID     string `json:"approvalId"`
	ApprovalStatus string `json:"approvalStatus"`
	CallID         string `json:"callId"`
	Fingerprint    string `json:"fingerprint"`
}

type governanceApproval struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RequestedBy string `json:"requestedBy"`
	ReviewedBy  string `json:"reviewedBy"`
}

type governanceApprovalList struct {
	Items []governanceApproval `json:"items"`
}

type governanceToolCall struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	ApprovalID     string `json:"approvalId"`
	ApprovalStatus string `json:"approvalStatus"`
}

type governanceSecret struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ValueRef string `json:"valueRef"`
}

func newGovernanceAPI(baseURL, workspaceOrgID string, timeout time.Duration, sensitiveValue string) *governanceAPI {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &governanceAPI{
		baseURL:        strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		workspaceOrgID: strings.TrimSpace(workspaceOrgID),
		sensitiveValue: sensitiveValue,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return fmt.Errorf("governance API 不允许重定向")
			},
		},
	}
}

func (c *governanceAPI) SensitiveValueDetected() bool {
	return c != nil && c.sensitiveDetected
}

func (c *governanceAPI) requestJSON(
	ctx context.Context,
	method,
	path string,
	payload any,
	destination any,
) (int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, fmt.Errorf("编码 governance API 请求失败：%w", err)
		}
		c.detectSensitive(raw)
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, fmt.Errorf("创建 governance API 请求失败：%w", err)
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.workspaceOrgID != "" {
		request.Header.Set("X-Workspace-Org-Id", c.workspaceOrgID)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("调用 governance API 失败：%w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, governanceRequestLimit+1))
	if err != nil {
		return 0, fmt.Errorf("读取 governance API 响应失败：%w", err)
	}
	if len(raw) > governanceRequestLimit {
		return 0, fmt.Errorf("governance API 响应超过大小限制")
	}
	c.detectSensitive(raw)
	if destination != nil && len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(destination); err != nil {
			return 0, fmt.Errorf("解析 governance API 响应失败")
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return 0, fmt.Errorf("governance API 响应包含无效尾部")
		}
	}
	return response.StatusCode, nil
}

func (c *governanceAPI) createHTTPPost(
	ctx context.Context,
	targetURL,
	message string,
) (governanceToolCallResponse, error) {
	var response governanceToolCallResponse
	status, err := c.requestJSON(ctx, "POST", "/api/tool-calls", map[string]any{
		"tool": "http.request",
		"arguments": map[string]any{
			"method": "POST",
			"url":    targetURL,
			"body": map[string]any{
				"message": message,
			},
		},
	}, &response)
	if err != nil {
		return governanceToolCallResponse{}, err
	}
	if status != http.StatusOK {
		return governanceToolCallResponse{}, fmt.Errorf("创建 HTTP tool call 返回状态码 %d", status)
	}
	return response, nil
}

func (c *governanceAPI) createAgentGuardTicket(
	ctx context.Context,
	target,
	ticketID string,
) (governanceAgentGuardResponse, error) {
	payload := map[string]any{
		"adapter":         "codex",
		"tool":            "Write",
		"actionType":      "write",
		"target":          target,
		"isScript":        true,
		"contentEncoding": "plain",
		"content":         "Write-Host 'synthetic governance action'",
	}
	if strings.TrimSpace(ticketID) != "" {
		payload["ticketId"] = ticketID
	}
	var response governanceAgentGuardResponse
	status, err := c.requestJSON(ctx, "POST", "/api/agent-guard/evaluate", payload, &response)
	if err != nil {
		return governanceAgentGuardResponse{}, err
	}
	if status != http.StatusOK {
		return governanceAgentGuardResponse{}, fmt.Errorf("Agent Guard evaluate 返回状态码 %d", status)
	}
	return response, nil
}

func (c *governanceAPI) getApproval(ctx context.Context, approvalID string) (governanceApproval, error) {
	var response governanceApprovalList
	status, err := c.requestJSON(ctx, "GET", "/api/approvals", nil, &response)
	if err != nil {
		return governanceApproval{}, err
	}
	if status != http.StatusOK {
		return governanceApproval{}, fmt.Errorf("读取 approval 列表返回状态码 %d", status)
	}
	for _, approval := range response.Items {
		if approval.ID == approvalID {
			return approval, nil
		}
	}
	return governanceApproval{}, fmt.Errorf("approval 记录不存在")
}

func (c *governanceAPI) getToolCall(ctx context.Context, callID string) (governanceToolCall, error) {
	var response governanceToolCall
	status, err := c.requestJSON(ctx, "GET", "/api/tool-calls/"+callID, nil, &response)
	if err != nil {
		return governanceToolCall{}, err
	}
	if status != http.StatusOK {
		return governanceToolCall{}, fmt.Errorf("读取 tool call 返回状态码 %d", status)
	}
	return response, nil
}

func (c *governanceAPI) detectSensitive(raw []byte) {
	if c == nil || c.sensitiveValue == "" {
		return
	}
	if bytes.Contains(raw, []byte(c.sensitiveValue)) {
		c.sensitiveDetected = true
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/google/uuid"
)

const (
	ErrParse            = -32700
	ErrInvalidRequest   = -32600
	ErrMethodNotFound   = -32601
	ErrInvalidParams    = -32602
	ErrInternal         = -32603
	ErrApprovalRequired = -32050
)

type Config struct {
	Store                 store.Store
	ResolveContext        func(context.Context) (RequestContext, error)
	ResolveSessionContext func(context.Context) (RequestContext, error)
	CallTool              ToolCaller
	Logger                *slog.Logger
}

type RequestContext struct {
	WorkspaceID string
}

type ToolCallRequest struct {
	Tool      string
	Arguments json.RawMessage
}

type ToolCallResult struct {
	Status         string
	Result         any
	CallID         string
	TraceID        string
	Message        string
	Reason         string
	ApprovalID     string
	ApprovalStatus string
}

type ToolCaller func(context.Context, ToolCallRequest) (ToolCallResult, error)

type Handler struct {
	store                 store.Store
	resolveContext        func(context.Context) (RequestContext, error)
	resolveSessionContext func(context.Context) (RequestContext, error)
	callTool              ToolCaller
	logger                *slog.Logger
	sessionsMu            sync.RWMutex
	sessions              map[string]chan JSONRPCResponse
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	IDSet   bool            `json:"-"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func NewHandler(cfg Config) *Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		store:                 cfg.Store,
		resolveContext:        cfg.ResolveContext,
		resolveSessionContext: cfg.ResolveSessionContext,
		callTool:              cfg.CallTool,
		logger:                logger,
		sessions:              map[string]chan JSONRPCResponse{},
	}
}

func (h *Handler) StreamableHTTPHandler() http.Handler {
	return http.HandlerFunc(h.ServeStreamableHTTP)
}

func (h *Handler) ServeStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		h.writeJSONResponse(w, http.StatusMethodNotAllowed, jsonRPCError(nil, ErrInvalidRequest, "该 MCP endpoint 仅支持 POST JSON-RPC 请求", nil))
		return
	}

	req, err := decodeJSONRPCRequest(r)
	if err != nil {
		h.writeJSONResponse(w, http.StatusOK, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: ErrParse, Message: "invalid JSON-RPC request"},
		})
		return
	}
	if req.isNotification() {
		h.handleNotification(r.Context(), req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	h.writeJSONResponse(w, http.StatusOK, h.handleJSONRPC(r.Context(), req))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.serveSSESession(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	req, err := decodeJSONRPCRequest(r)
	if err != nil {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		h.writeEvent(w, JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: ErrParse, Message: "invalid JSON-RPC request"},
		})
		return
	}
	if req.isNotification() {
		h.handleNotification(r.Context(), req)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := h.handleJSONRPC(r.Context(), req)
	if sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId")); sessionID != "" {
		if !h.sendSessionResponse(sessionID, resp) {
			http.Error(w, "MCP SSE session not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	h.writeEvent(w, resp)
}

func decodeJSONRPCRequest(r *http.Request) (JSONRPCRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return JSONRPCRequest{}, err
	}
	_, idSet := raw["id"]
	encoded, err := json.Marshal(raw)
	if err != nil {
		return JSONRPCRequest{}, err
	}
	var req JSONRPCRequest
	if err := json.Unmarshal(encoded, &req); err != nil {
		return JSONRPCRequest{}, err
	}
	req.IDSet = idSet
	return req, nil
}

func (h *Handler) serveSSESession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.resolveSession(r.Context()); err != nil {
		h.logger.Warn("MCP SSE session authorization failed", "error", err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sessionID := uuid.NewString()
	responses := make(chan JSONRPCResponse, 16)
	h.sessionsMu.Lock()
	h.sessions[sessionID] = responses
	h.sessionsMu.Unlock()
	defer func() {
		h.sessionsMu.Lock()
		delete(h.sessions, sessionID)
		h.sessionsMu.Unlock()
		close(responses)
	}()

	endpoint := "/mcp/sse?sessionId=" + url.QueryEscape(sessionID)
	h.writeNamedEvent(w, "endpoint", []byte(endpoint))

	for {
		select {
		case response := <-responses:
			h.writeEvent(w, response)
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handler) handleJSONRPC(ctx context.Context, req JSONRPCRequest) JSONRPCResponse {
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		return jsonRPCError(req.ID, ErrInvalidRequest, "invalid JSON-RPC request", nil)
	}

	switch req.Method {
	case "initialize":
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "AgentToolGate",
					"version": "0.1.0",
				},
			},
		}
	case "tools/list":
		return h.handleToolsList(ctx, req)
	case "tools/call":
		return h.handleToolsCall(ctx, req)
	default:
		return jsonRPCError(req.ID, ErrMethodNotFound, "method not found", nil)
	}
}

func (req JSONRPCRequest) isNotification() bool {
	return req.JSONRPC == "2.0" && strings.TrimSpace(req.Method) != "" && !req.IDSet
}

func (h *Handler) handleNotification(ctx context.Context, req JSONRPCRequest) {
	// MCP 客户端在 initialize 后会发送 notifications/initialized。
	// 通知没有 id，按 JSON-RPC 语义不能返回结果；这里仅接受握手通知，
	// 其它通知也不触发工具执行，避免无响应调用绕过治理审计。
	if req.Method == "notifications/initialized" {
		return
	}
	h.logger.Debug("ignored MCP JSON-RPC notification", "method", req.Method)
}

func (h *Handler) handleToolsList(ctx context.Context, req JSONRPCRequest) JSONRPCResponse {
	reqCtx, err := h.resolve(ctx)
	if err != nil {
		h.logger.Warn("MCP tools/list context resolution failed", "error", err)
		return jsonRPCError(req.ID, ErrInternal, "MCP request is not authorized", nil)
	}
	tools, err := h.store.ListTools(ctx, reqCtx.WorkspaceID)
	if err != nil {
		h.logger.Warn("MCP tools/list failed", "error", err)
		return jsonRPCError(req.ID, ErrInternal, "MCP tools list failed", nil)
	}

	items := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if !tool.Enabled {
			continue
		}
		items = append(items, mapMCPTool(tool))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i]["name"].(string) < items[j]["name"].(string)
	})

	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": items},
	}
}

func (h *Handler) handleToolsCall(ctx context.Context, req JSONRPCRequest) JSONRPCResponse {
	if h.callTool == nil {
		return jsonRPCError(req.ID, ErrInternal, "MCP tool caller is not configured", nil)
	}

	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) == 0 {
		return jsonRPCError(req.ID, ErrInvalidParams, "tools/call params are required", nil)
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonRPCError(req.ID, ErrInvalidParams, "invalid tools/call params", nil)
	}
	if strings.TrimSpace(params.Name) == "" {
		return jsonRPCError(req.ID, ErrInvalidParams, "tool name is required", nil)
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}

	if _, err := h.resolve(ctx); err != nil {
		h.logger.Warn("MCP tools/call context resolution failed", "tool", strings.TrimSpace(params.Name), "error", err)
		return jsonRPCError(req.ID, ErrInternal, "MCP request is not authorized", nil)
	}

	result, err := h.callTool(ctx, ToolCallRequest{
		Tool:      strings.TrimSpace(params.Name),
		Arguments: params.Arguments,
	})
	if err != nil {
		h.logger.Warn("MCP tools/call failed", "tool", strings.TrimSpace(params.Name), "error", err)
		code, message := mcpToolCallPublicError(err)
		return jsonRPCError(req.ID, code, message, nil)
	}
	if strings.EqualFold(result.Status, "approval_required") {
		return jsonRPCError(req.ID, ErrApprovalRequired, "该工具调用需要审批。请前往 AgentToolGate 控制台审批后重试。", map[string]any{
			"status":         result.Status,
			"callId":         result.CallID,
			"approvalId":     result.ApprovalID,
			"approvalStatus": result.ApprovalStatus,
			"message":        result.Message,
			"reason":         result.Reason,
		})
	}

	text, err := json.Marshal(result.Result)
	if err != nil {
		return jsonRPCError(req.ID, ErrInternal, "failed to encode tool result", nil)
	}
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(text),
				},
			},
			"isError": strings.EqualFold(result.Status, "failed") || strings.EqualFold(result.Status, "denied"),
			"metadata": map[string]any{
				"status":  result.Status,
				"callId":  result.CallID,
				"traceId": result.TraceID,
			},
		},
	}
}

func (h *Handler) resolve(ctx context.Context) (RequestContext, error) {
	if h.resolveContext == nil {
		return RequestContext{}, errors.New("MCP request context resolver is not configured")
	}
	reqCtx, err := h.resolveContext(ctx)
	if err != nil {
		return RequestContext{}, err
	}
	if strings.TrimSpace(reqCtx.WorkspaceID) == "" {
		return RequestContext{}, errors.New("MCP request context is missing workspace")
	}
	return reqCtx, nil
}

func (h *Handler) resolveSession(ctx context.Context) (RequestContext, error) {
	resolver := h.resolveSessionContext
	if resolver == nil {
		resolver = h.resolveContext
	}
	if resolver == nil {
		return RequestContext{}, errors.New("MCP session context resolver is not configured")
	}
	reqCtx, err := resolver(ctx)
	if err != nil {
		return RequestContext{}, err
	}
	if strings.TrimSpace(reqCtx.WorkspaceID) == "" {
		return RequestContext{}, errors.New("MCP session context is missing workspace")
	}
	return reqCtx, nil
}

func mcpToolCallPublicError(err error) (int, string) {
	if err == nil {
		return ErrInternal, "MCP tool call failed"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "bad request") ||
		strings.Contains(lower, "invalid argument") ||
		strings.Contains(lower, "invalid params") ||
		strings.Contains(lower, "validation") {
		return ErrInvalidParams, err.Error()
	}
	return ErrInternal, "MCP tool call failed"
}

func mapMCPTool(tool model.Tool) map[string]any {
	inputSchema := map[string]any{"type": "object"}
	if len(tool.InputSchemaJSON) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(tool.InputSchemaJSON, &decoded); err == nil && len(decoded) > 0 {
			inputSchema = decoded
		}
	}

	item := map[string]any{
		"name":        tool.Key(),
		"description": strings.TrimSpace(tool.Description),
		"inputSchema": inputSchema,
	}
	if strings.TrimSpace(tool.DisplayName) != "" {
		item["title"] = strings.TrimSpace(tool.DisplayName)
	}
	return item
}

func jsonRPCError(id any, code int, message string, data any) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func (h *Handler) writeEvent(w http.ResponseWriter, response JSONRPCResponse) {
	payload, err := json.Marshal(response)
	if err != nil {
		h.logger.Warn("encode MCP response failed", "error", err)
		payload = []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"}}`)
	}
	h.writeNamedEvent(w, "message", payload)
}

func (h *Handler) writeNamedEvent(w http.ResponseWriter, event string, payload []byte) {
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) writeJSONResponse(w http.ResponseWriter, status int, response JSONRPCResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Warn("encode MCP streamable HTTP response failed", "error", err)
	}
}

func (h *Handler) sendSessionResponse(sessionID string, response JSONRPCResponse) bool {
	h.sessionsMu.RLock()
	defer h.sessionsMu.RUnlock()
	responses, ok := h.sessions[sessionID]
	if !ok {
		return false
	}

	select {
	case responses <- response:
		return true
	default:
		return false
	}
}

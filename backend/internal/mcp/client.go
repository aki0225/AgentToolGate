package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const outboundDefaultTimeout = 3 * time.Second

type OutboundClient struct {
	baseURL    string
	headers    map[string]string
	timeout    time.Duration
	httpClient *http.Client
}

type OutboundTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

func NewOutboundClient(baseURL string, headers map[string]string, timeout time.Duration) *OutboundClient {
	if timeout <= 0 {
		timeout = outboundDefaultTimeout
	}
	return &OutboundClient{
		baseURL: baseURL,
		headers: cloneStringMap(headers),
		timeout: timeout,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *OutboundClient) SyncTools(ctx context.Context) ([]OutboundTool, error) {
	session, err := c.newSession(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	initializeResponse, err := session.callJSONRPC(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "initialize",
		Method:  "initialize",
		Params: mustJSONRawMessage(map[string]any{
			"protocolVersion": "2024-11-05",
		}),
	})
	if err != nil {
		return nil, err
	}
	if initializeResponse.Error != nil {
		return nil, fmt.Errorf("mcp initialize failed: %s", initializeResponse.Error.Message)
	}

	response, err := session.callJSONRPC(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "tools/list",
		Method:  "tools/list",
	})
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, fmt.Errorf("mcp tools/list failed: %s", response.Error.Message)
	}

	return decodeOutboundTools(response.Result)
}

func (c *OutboundClient) CallTool(ctx context.Context, toolName string, arguments json.RawMessage) (any, error) {
	session, err := c.newSession(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	initializeResponse, err := session.callJSONRPC(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "initialize",
		Method:  "initialize",
		Params: mustJSONRawMessage(map[string]any{
			"protocolVersion": "2024-11-05",
		}),
	})
	if err != nil {
		return nil, err
	}
	if initializeResponse.Error != nil {
		return nil, fmt.Errorf("mcp initialize failed: %s", initializeResponse.Error.Message)
	}

	response, err := session.callJSONRPC(ctx, JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "tools/call",
		Method:  "tools/call",
		Params: mustJSONRawMessage(map[string]any{
			"name":      strings.TrimSpace(toolName),
			"arguments": jsonValueOrObject(arguments),
		}),
	})
	if err != nil {
		return nil, err
	}
	if response.Error != nil {
		return nil, fmt.Errorf("mcp tools/call failed: %s", response.Error.Message)
	}
	return response.Result, nil
}

func (c *OutboundClient) newSession(ctx context.Context) (*outboundSession, error) {
	base, err := url.Parse(strings.TrimSpace(c.baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("mcp url is invalid")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("mcp url is invalid")
	}
	if base.User != nil {
		return nil, fmt.Errorf("mcp url userinfo is not allowed")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mcp session failed with status %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	event, data, err := readSSEEvent(reader)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if event != "endpoint" {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mcp session did not return endpoint event")
	}

	endpoint, err := resolveMCPEndpoint(base, strings.TrimSpace(data))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if err := validateMCPEndpoint(base, endpoint); err != nil {
		_ = resp.Body.Close()
		return nil, err
	}

	return &outboundSession{
		client:   c.httpClient,
		baseURL:  base,
		endpoint: endpoint,
		body:     resp.Body,
		reader:   reader,
		headers:  cloneStringMap(c.headers),
	}, nil
}

type outboundSession struct {
	client   *http.Client
	baseURL  *url.URL
	endpoint *url.URL
	body     io.ReadCloser
	reader   *bufio.Reader
	headers  map[string]string
}

func (s *outboundSession) Close() {
	if s.body != nil {
		_ = s.body.Close()
	}
}

func (s *outboundSession) callJSONRPC(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return JSONRPCResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return JSONRPCResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	for key, value := range s.headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return JSONRPCResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return JSONRPCResponse{}, fmt.Errorf("mcp call failed with status %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	event, data, err := readSSEEvent(s.reader)
	if err != nil {
		return JSONRPCResponse{}, err
	}
	if event != "message" {
		return JSONRPCResponse{}, fmt.Errorf("mcp session returned unexpected event %q", event)
	}

	var decoded JSONRPCResponse
	if err := json.Unmarshal([]byte(data), &decoded); err != nil {
		return JSONRPCResponse{}, err
	}
	if decoded.Error != nil {
		return decoded, nil
	}
	return decoded, nil
}

func readSSEEvent(reader *bufio.Reader) (string, string, error) {
	var event string
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event == "" && len(dataLines) == 0 {
				continue
			}
			return event, strings.Join(dataLines, "\n"), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func resolveMCPEndpoint(base *url.URL, raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("mcp endpoint is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("mcp endpoint is invalid")
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return base.ResolveReference(parsed), nil
	}
	if parsed.Scheme == "" {
		return base.ResolveReference(parsed), nil
	}
	return parsed, nil
}

func validateMCPEndpoint(base, endpoint *url.URL) error {
	if endpoint == nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return fmt.Errorf("mcp endpoint is invalid")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("mcp endpoint is invalid")
	}
	if endpoint.User != nil {
		return fmt.Errorf("mcp endpoint userinfo is not allowed")
	}
	if !strings.EqualFold(endpoint.Scheme, base.Scheme) || !strings.EqualFold(endpoint.Host, base.Host) {
		return fmt.Errorf("mcp endpoint must stay on the original origin")
	}
	return nil
}

func decodeOutboundTools(result any) ([]OutboundTool, error) {
	if result == nil {
		return nil, fmt.Errorf("mcp tools/list returned empty result")
	}
	obj, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mcp tools/list returned invalid result")
	}
	rawTools, ok := obj["tools"]
	if !ok {
		return nil, fmt.Errorf("mcp tools/list response missing tools")
	}
	data, err := json.Marshal(rawTools)
	if err != nil {
		return nil, err
	}
	var items []struct {
		Name        string          `json:"name"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
		Annotations map[string]any  `json:"annotations"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	tools := make([]OutboundTool, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		inputSchema := item.InputSchema
		if len(inputSchema) == 0 {
			inputSchema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, OutboundTool{
			Name:        name,
			Title:       strings.TrimSpace(item.Title),
			Description: strings.TrimSpace(item.Description),
			InputSchema: append(json.RawMessage(nil), inputSchema...),
			Annotations: cloneAnyMap(item.Annotations),
		})
	}
	return tools, nil
}

func jsonValueOrObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{}
	}
	if decoded == nil {
		return map[string]any{}
	}
	return decoded
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(value))
	for key, val := range value {
		cloned[key] = val
	}
	return cloned
}

func cloneAnyMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(value))
	for key, val := range value {
		cloned[key] = val
	}
	return cloned
}

func mustJSONRawMessage(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

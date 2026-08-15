package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agenttoolgate/backend/internal/netguard"
)

const (
	outboundDefaultTimeout        = 3 * time.Second
	outboundMaxSSELineBytes       = 64 << 10
	outboundMaxSSEEventBytes      = 1 << 20
	outboundMaxPOSTBodyBytes      = 1 << 20
	outboundMaxPOSTResponseBytes  = 1 << 20
	outboundMaxJSONRPCResultBytes = 1 << 20
	outboundMaxTools              = 256
	outboundMaxInputSchemaBytes   = 64 << 10
)

type OutboundClient struct {
	baseURL    string
	headers    map[string]string
	timeout    time.Duration
	httpClient *http.Client
}

type OutboundClientOptions struct {
	AllowedAuthorities []string
	Resolver           netguard.Resolver
}

type OutboundTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

func NewOutboundClient(baseURL string, headers map[string]string, timeout time.Duration, options OutboundClientOptions) *OutboundClient {
	if timeout <= 0 {
		timeout = outboundDefaultTimeout
	}
	return &OutboundClient{
		baseURL:    baseURL,
		headers:    cloneStringMap(headers),
		timeout:    timeout,
		httpClient: newMCPHTTPClient(timeout, options),
	}
}

func newMCPHTTPClient(timeout time.Duration, options OutboundClientOptions) *http.Client {
	client := netguard.NewClient(netguard.ClientOptions{
		Timeout:            timeout,
		AllowedAuthorities: options.AllowedAuthorities,
		Resolver:           options.Resolver,
	})
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("mcp redirect limit exceeded")
		}
		if len(via) == 0 || !sameMCPOrigin(via[0].URL, req.URL) {
			return errors.New("mcp redirect must stay on the original origin")
		}
		return nil
	}
	return client
}

func sameMCPOrigin(base, candidate *url.URL) bool {
	if base == nil || candidate == nil || base.User != nil || candidate.User != nil {
		return false
	}
	if !strings.EqualFold(base.Scheme, candidate.Scheme) ||
		!strings.EqualFold(base.Hostname(), candidate.Hostname()) {
		return false
	}
	return effectiveMCPPort(base) == effectiveMCPPort(candidate)
}

func effectiveMCPPort(value *url.URL) string {
	if value == nil {
		return ""
	}
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
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

	return decodeOutboundToolsJSON(response.rawResult)
}

func (c *OutboundClient) CallTool(ctx context.Context, toolName string, arguments json.RawMessage) (any, error) {
	toolCallRequest, toolCallPayload, err := buildOutboundToolCallRequest(toolName, arguments)
	if err != nil {
		return nil, err
	}

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

	response, err := session.callJSONRPCPayload(ctx, toolCallRequest, toolCallPayload)
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

	responseBase := resp.Request.URL
	endpoint, err := resolveMCPEndpoint(responseBase, strings.TrimSpace(data))
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
		baseURL:  responseBase,
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

type outboundJSONRPCResponse struct {
	JSONRPCResponse
	rawResult json.RawMessage
}

func (s *outboundSession) Close() {
	if s.body != nil {
		_ = s.body.Close()
	}
}

func (s *outboundSession) callJSONRPC(ctx context.Context, req JSONRPCRequest) (outboundJSONRPCResponse, error) {
	payload, err := marshalOutboundJSONRPCRequest(req)
	if err != nil {
		return outboundJSONRPCResponse{}, err
	}
	return s.callJSONRPCPayload(ctx, req, payload)
}

func (s *outboundSession) callJSONRPCPayload(ctx context.Context, req JSONRPCRequest, payload []byte) (outboundJSONRPCResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return outboundJSONRPCResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	for key, value := range s.headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return outboundJSONRPCResponse{}, err
	}
	if err := discardMCPPOSTResponse(resp.Body); err != nil {
		s.Close()
		return outboundJSONRPCResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return outboundJSONRPCResponse{}, fmt.Errorf("mcp call failed with status %d", resp.StatusCode)
	}

	event, data, err := readSSEEvent(s.reader)
	if err != nil {
		s.Close()
		return outboundJSONRPCResponse{}, err
	}
	if event != "message" {
		s.Close()
		return outboundJSONRPCResponse{}, fmt.Errorf("mcp session returned unexpected event %q", event)
	}

	decoded, err := decodeOutboundJSONRPCResponse(data, req.Method != "tools/list")
	if err != nil {
		s.Close()
		return outboundJSONRPCResponse{}, err
	}
	if decoded.Error != nil {
		return decoded, nil
	}
	return decoded, nil
}

func marshalOutboundJSONRPCRequest(req JSONRPCRequest) ([]byte, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(payload) > outboundMaxPOSTBodyBytes {
		return nil, fmt.Errorf("mcp POST JSON body exceeds %d bytes", outboundMaxPOSTBodyBytes)
	}
	return payload, nil
}

func buildOutboundToolCallRequest(toolName string, arguments json.RawMessage) (JSONRPCRequest, []byte, error) {
	normalizedArguments, err := normalizeOutboundToolArguments(arguments)
	if err != nil {
		return JSONRPCRequest{}, nil, err
	}
	params, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{
		Name:      strings.TrimSpace(toolName),
		Arguments: normalizedArguments,
	})
	if err != nil {
		return JSONRPCRequest{}, nil, err
	}
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      "tools/call",
		Method:  "tools/call",
		Params:  params,
	}
	payload, err := marshalOutboundJSONRPCRequest(req)
	return req, payload, err
}

func normalizeOutboundToolArguments(arguments json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(trimmed) > outboundMaxPOSTBodyBytes {
		return nil, fmt.Errorf("mcp POST JSON body exceeds %d bytes", outboundMaxPOSTBodyBytes)
	}
	if !json.Valid(trimmed) || trimmed[0] != '{' {
		return json.RawMessage(`{}`), nil
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func discardMCPPOSTResponse(body io.ReadCloser) error {
	if body == nil {
		return nil
	}
	reader := &io.LimitedReader{R: body, N: outboundMaxPOSTResponseBytes + 1}
	n, readErr := io.Copy(io.Discard, reader)
	closeErr := body.Close()
	if n > outboundMaxPOSTResponseBytes {
		return fmt.Errorf("mcp POST response body exceeds %d bytes", outboundMaxPOSTResponseBytes)
	}
	if readErr != nil {
		return readErr
	}
	return closeErr
}

func readSSEEvent(reader *bufio.Reader) (string, string, error) {
	var event string
	var dataLines []string
	eventBytes := 0
	for {
		line, lineBytes, err := readSSELine(reader)
		if err != nil {
			return "", "", err
		}
		if lineBytes > outboundMaxSSEEventBytes-eventBytes {
			return "", "", fmt.Errorf("mcp SSE event exceeds %d bytes", outboundMaxSSEEventBytes)
		}
		eventBytes += lineBytes
		if line == "" {
			if event == "" && len(dataLines) == 0 {
				eventBytes = 0
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

func readSSELine(reader *bufio.Reader) (string, int, error) {
	maxBufferedBytes := outboundMaxSSELineBytes + len("\r\n")
	line := make([]byte, 0, min(reader.Size(), maxBufferedBytes))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > maxBufferedBytes-len(line) {
			return "", len(line) + len(fragment), fmt.Errorf("mcp SSE line exceeds %d bytes", outboundMaxSSELineBytes)
		}
		line = append(line, fragment...)
		if err == nil {
			content := line[:len(line)-1]
			if len(content) > 0 && content[len(content)-1] == '\r' {
				content = content[:len(content)-1]
			}
			if len(content) > outboundMaxSSELineBytes {
				return "", len(line), fmt.Errorf("mcp SSE line exceeds %d bytes", outboundMaxSSELineBytes)
			}
			return string(content), len(line), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", len(line), err
		}
	}
}

func decodeOutboundJSONRPCResponse(data string, decodeResult bool) (outboundJSONRPCResponse, error) {
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *JSONRPCError   `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return outboundJSONRPCResponse{}, err
	}
	if len(envelope.Result) > outboundMaxJSONRPCResultBytes {
		return outboundJSONRPCResponse{}, fmt.Errorf("mcp JSON-RPC result exceeds %d bytes", outboundMaxJSONRPCResultBytes)
	}

	var result any
	if decodeResult && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			return outboundJSONRPCResponse{}, err
		}
	}
	return outboundJSONRPCResponse{
		JSONRPCResponse: JSONRPCResponse{
			JSONRPC: envelope.JSONRPC,
			ID:      envelope.ID,
			Result:  result,
			Error:   envelope.Error,
		},
		rawResult: append(json.RawMessage(nil), envelope.Result...),
	}, nil
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
	if !sameMCPOrigin(base, endpoint) {
		return fmt.Errorf("mcp endpoint must stay on the original origin")
	}
	return nil
}

func decodeOutboundTools(result any) ([]OutboundTool, error) {
	if result == nil {
		return nil, fmt.Errorf("mcp tools/list returned empty result")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return decodeOutboundToolsJSON(data)
}

func decodeOutboundToolsJSON(result json.RawMessage) ([]OutboundTool, error) {
	if len(result) == 0 || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return nil, fmt.Errorf("mcp tools/list returned empty result")
	}
	var obj struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &obj); err != nil {
		return nil, fmt.Errorf("mcp tools/list returned invalid result")
	}
	if len(obj.Tools) == 0 {
		return nil, fmt.Errorf("mcp tools/list response missing tools")
	}

	decoder := json.NewDecoder(bytes.NewReader(obj.Tools))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, fmt.Errorf("mcp tools/list tools must be an array")
	}

	tools := make([]OutboundTool, 0, min(outboundMaxTools, 16))
	itemCount := 0
	for decoder.More() {
		itemCount++
		if itemCount > outboundMaxTools {
			return nil, fmt.Errorf("mcp tools/list returned more than %d tools", outboundMaxTools)
		}
		var item struct {
			Name        string          `json:"name"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations json.RawMessage `json:"annotations"`
		}
		if err := decoder.Decode(&item); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		inputSchema := item.InputSchema
		if len(inputSchema) == 0 {
			inputSchema = json.RawMessage(`{"type":"object"}`)
		}
		if len(inputSchema) > outboundMaxInputSchemaBytes {
			return nil, fmt.Errorf("mcp tool %q inputSchema exceeds %d bytes", name, outboundMaxInputSchemaBytes)
		}
		var annotations map[string]any
		if len(item.Annotations) > 0 && !bytes.Equal(bytes.TrimSpace(item.Annotations), []byte("null")) {
			if err := json.Unmarshal(item.Annotations, &annotations); err != nil {
				return nil, err
			}
		}
		tools = append(tools, OutboundTool{
			Name:        name,
			Title:       strings.TrimSpace(item.Title),
			Description: strings.TrimSpace(item.Description),
			InputSchema: append(json.RawMessage(nil), inputSchema...),
			Annotations: cloneAnyMap(annotations),
		})
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != ']' {
		return nil, fmt.Errorf("mcp tools/list returned invalid tools array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("mcp tools/list returned trailing data")
	}
	return tools, nil
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

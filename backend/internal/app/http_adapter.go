package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"agenttoolgate/backend/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultHTTPTimeoutMs        = 3000
	hardHTTPTimeoutMs           = 30000
	defaultHTTPMaxResponseBytes = 65536
	hardHTTPMaxResponseBytes    = 1048576
)

var defaultHTTPAllowedMethods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

var (
	errHTTPRedirectTargetNotAllowed = errors.New("http redirect target is not allowed")
	errHTTPRedirectLimitExceeded    = errors.New("http redirect limit exceeded")
)

type httpRequestArgs struct {
	Method           string
	URL              *url.URL
	Headers          map[string]string
	HeaderSecretRefs map[string]string
	Body             any
	HasBody          bool
}

type parsedHTTPAllowedHosts struct {
	values map[string]struct{}
}

func (a *App) validateHTTPRequestBeforePolicy(ctx context.Context, workspaceID string, decodedArgs any) error {
	args, err := a.parseHTTPRequestArgs(decodedArgs)
	if err != nil {
		return err
	}
	_, err = a.resolveHTTPRequestHeaders(ctx, workspaceID, args)
	return err
}

func (a *App) deriveHTTPPolicyDecision(decodedArgs any, currentDecision, currentReason string) (string, string) {
	args, err := a.parseHTTPRequestArgs(decodedArgs)
	if err != nil {
		return currentDecision, currentReason
	}
	if isHTTPWriteMethod(args.Method) {
		return policyRequireApproval, "http write method requires approval"
	}
	return currentDecision, currentReason
}

func (a *App) executeHTTPRequest(ctx context.Context, workspaceID string, decodedArgs any) (resultPayload map[string]any, resultJSON json.RawMessage, err error) {
	ctx, span := telemetry.StartSpan(ctx, "connector.http.request", attribute.String("tool.key", "http.request"))
	defer func() {
		if err != nil {
			telemetry.RecordError(span, err)
		}
		span.End()
	}()

	args, err := a.parseHTTPRequestArgs(decodedArgs)
	if err != nil {
		return nil, nil, err
	}
	span.SetAttributes(
		attribute.String("http.method", args.Method),
		attribute.String("http.url", redactHTTPURLForTelemetry(args.URL)),
	)

	timeout := effectiveHTTPTimeout(time.Duration(a.cfg.HTTPTimeoutMs) * time.Millisecond)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resolvedHeaders, err := a.resolveHTTPRequestHeaders(requestCtx, workspaceID, args)
	if err != nil {
		return nil, nil, err
	}

	var bodyReader io.Reader
	if args.HasBody {
		bodyBytes, contentType, err := encodeHTTPRequestBody(args.Body)
		if err != nil {
			return nil, nil, err
		}
		bodyReader = bytes.NewReader(bodyBytes)
		if contentType != "" && !hasHTTPHeader(resolvedHeaders, "Content-Type") {
			resolvedHeaders["Content-Type"] = contentType
		}
	}

	req, err := http.NewRequestWithContext(requestCtx, args.Method, args.URL.String(), bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("create http request: %w", err)
	}
	for key, value := range resolvedHeaders {
		req.Header.Set(key, value)
	}

	timeoutClient := newGuardedHTTPClient(timeout, a.cfg.HTTPAllowedHosts)
	resp, err := timeoutClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errors.Is(err, errHTTPRedirectTargetNotAllowed) {
			return nil, nil, badRequest("http redirect target is not allowed")
		}
		if errors.Is(err, errHTTPRedirectLimitExceeded) {
			return nil, nil, badRequest("http redirect limit exceeded")
		}
		return nil, nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 非 2xx 不透传响应体，避免把内部 API 错误详情或敏感内容写入审计。
		return nil, nil, fmt.Errorf("http request failed with status %d", resp.StatusCode)
	}

	bodyValue, truncated, err := readHTTPResponseBody(resp.Body, effectiveHTTPMaxResponseBytes(a.cfg.HTTPMaxResponseBytes))
	if err != nil {
		return nil, nil, err
	}

	resultPayload = map[string]any{
		"method":        args.Method,
		"url":           args.URL.String(),
		"statusCode":    resp.StatusCode,
		"headers":       redactHTTPHeaders(resp.Header),
		"body":          bodyValue,
		"bodyTruncated": truncated,
	}
	resultJSON, err = json.Marshal(resultPayload)
	if err != nil {
		return nil, nil, err
	}
	return resultPayload, resultJSON, nil
}

func (a *App) parseHTTPRequestArgs(decodedArgs any) (httpRequestArgs, error) {
	obj, ok := decodedArgs.(map[string]any)
	if !ok {
		return httpRequestArgs{}, badRequest("http.request arguments must be a JSON object")
	}

	method, err := requiredHTTPStringArg(obj, "method")
	if err != nil {
		return httpRequestArgs{}, err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if !isConfiguredHTTPMethodAllowed(method, a.cfg.HTTPAllowedMethods) {
		return httpRequestArgs{}, badRequest(fmt.Sprintf("http method %s is not allowed", method))
	}
	if !isHTTPSafeMethod(method) && !isHTTPWriteMethod(method) {
		return httpRequestArgs{}, badRequest(fmt.Sprintf("http method %s is not supported", method))
	}

	rawURL, err := requiredHTTPStringArg(obj, "url")
	if err != nil {
		return httpRequestArgs{}, err
	}
	parsedURL, err := parseAndValidateHTTPURL(rawURL, a.cfg.HTTPAllowedHosts)
	if err != nil {
		return httpRequestArgs{}, err
	}

	headers, err := parseHTTPRequestHeaders(obj["headers"])
	if err != nil {
		return httpRequestArgs{}, err
	}
	secretRefs, err := parseHTTPRequestHeaderSecretRefs(obj["headerSecretRefs"], headers)
	if err != nil {
		return httpRequestArgs{}, err
	}

	body, hasBody := obj["body"]
	return httpRequestArgs{
		Method:           method,
		URL:              parsedURL,
		Headers:          headers,
		HeaderSecretRefs: secretRefs,
		Body:             body,
		HasBody:          hasBody,
	}, nil
}

func requiredHTTPStringArg(obj map[string]any, key string) (string, error) {
	value, exists := obj[key]
	if !exists || value == nil {
		return "", badRequest(fmt.Sprintf("http %s is required", key))
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", badRequest(fmt.Sprintf("http %s must be a non-empty string", key))
	}
	return strings.TrimSpace(text), nil
}

func parseHTTPRequestHeaders(value any) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, badRequest("http headers must be a JSON object")
	}
	headers := make(map[string]string, len(obj))
	for key, rawValue := range obj {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return nil, badRequest("http header name is required")
		}
		if !isValidHTTPHeaderName(trimmedKey) {
			return nil, badRequest("http header name is invalid")
		}
		if isHTTPForbiddenRequestHeader(trimmedKey) {
			return nil, badRequest(fmt.Sprintf("http header %s is not allowed", trimmedKey))
		}
		text, ok := rawValue.(string)
		if !ok {
			return nil, badRequest(fmt.Sprintf("http header %s must be a string", trimmedKey))
		}
		if !isValidHTTPHeaderValue(text) {
			return nil, badRequest(fmt.Sprintf("http header %s value is invalid", trimmedKey))
		}
		headers[trimmedKey] = text
	}
	return headers, nil
}

func parseHTTPRequestHeaderSecretRefs(value any, headers map[string]string) (map[string]string, error) {
	if value == nil {
		return map[string]string{}, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, badRequest("http headerSecretRefs must be a JSON object")
	}
	secretRefs := make(map[string]string, len(obj))
	for key, rawValue := range obj {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return nil, badRequest("http header secret ref name is required")
		}
		if !isValidHTTPHeaderName(trimmedKey) {
			return nil, badRequest("http header secret ref name is invalid")
		}
		if isHTTPForbiddenHTTPSecretRefHeader(trimmedKey) {
			return nil, badRequest(fmt.Sprintf("http header %s is not allowed", trimmedKey))
		}
		text, ok := rawValue.(string)
		if !ok {
			return nil, badRequest(fmt.Sprintf("http header secret ref %s must be a string", trimmedKey))
		}
		trimmedRef := normalizeSecretReferenceValue(text)
		if !isValidSecretReferenceValue(trimmedRef) {
			return nil, badRequest(fmt.Sprintf("http header secret ref %s is invalid", trimmedKey))
		}
		for header := range headers {
			if strings.EqualFold(header, trimmedKey) {
				return nil, badRequest(fmt.Sprintf("http header %s cannot be defined in both headers and headerSecretRefs", trimmedKey))
			}
		}
		secretRefs[trimmedKey] = trimmedRef
	}
	return secretRefs, nil
}

func (a *App) resolveHTTPRequestHeaders(ctx context.Context, workspaceID string, args httpRequestArgs) (map[string]string, error) {
	resolved := make(map[string]string, len(args.Headers)+len(args.HeaderSecretRefs))
	for key, value := range args.Headers {
		resolved[strings.TrimSpace(key)] = value
	}
	secretValues, err := a.resolveSecretRefs(ctx, workspaceID, args.HeaderSecretRefs)
	if err != nil {
		return nil, err
	}
	for key, value := range secretValues {
		for existing := range resolved {
			if strings.EqualFold(existing, key) {
				return nil, badRequest(fmt.Sprintf("http header %s cannot be defined in both headers and headerSecretRefs", key))
			}
		}
		resolved[key] = value
	}
	return resolved, nil
}

func newGuardedHTTPClient(timeout time.Duration, rawAllowedHosts []string) *http.Client {
	transport := cloneHTTPTransportWithoutProxy()
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errHTTPRedirectLimitExceeded
			}
			if _, err := parseAndValidateHTTPURL(req.URL.String(), rawAllowedHosts); err != nil {
				// 不返回原始校验错误，避免把上游 Location 中的内部地址透给前端或审计错误详情。
				return errHTTPRedirectTargetNotAllowed
			}
			return nil
		},
	}
}

func cloneHTTPTransportWithoutProxy() *http.Transport {
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := defaultTransport.Clone()
		// HTTP Adapter 不支持代理；显式禁用环境代理，避免 allowlist 之外的转发路径。
		cloned.Proxy = nil
		return cloned
	}
	return &http.Transport{Proxy: nil}
}

func parseAndValidateHTTPURL(rawURL string, rawAllowedHosts []string) (*url.URL, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, badRequest("http url is invalid")
	}
	if parsedURL.User != nil {
		return nil, badRequest("http url userinfo is not allowed")
	}
	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, badRequest("only http and https urls are allowed")
	}

	allowedHosts, err := parseHTTPAllowedHosts(rawAllowedHosts)
	if err != nil {
		return nil, err
	}
	hostKey := normalizeHTTPHostKey(parsedURL.Host)
	hostAllowed := allowedHosts.Contains(hostKey)
	if err := guardHTTPHost(parsedURL.Hostname(), hostAllowed); err != nil {
		return nil, err
	}
	if !hostAllowed {
		return nil, badRequest(fmt.Sprintf("http host %s is not allowed", hostKey))
	}
	return parsedURL, nil
}

func parseHTTPAllowedHosts(rawHosts []string) (parsedHTTPAllowedHosts, error) {
	result := parsedHTTPAllowedHosts{values: map[string]struct{}{}}
	for _, raw := range rawHosts {
		trimmed := normalizeHTTPHostKey(raw)
		if trimmed == "" {
			continue
		}
		result.values[trimmed] = struct{}{}
	}
	if len(result.values) == 0 {
		return parsedHTTPAllowedHosts{}, badRequest("http allowed host whitelist is not configured")
	}
	return result, nil
}

func (h parsedHTTPAllowedHosts) Contains(host string) bool {
	_, ok := h.values[normalizeHTTPHostKey(host)]
	return ok
}

func normalizeHTTPHostKey(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

func guardHTTPHost(hostname string, hostAllowed bool) error {
	normalizedHost := strings.ToLower(strings.Trim(hostname, "[]"))
	if normalizedHost == "localhost" && !hostAllowed {
		return badRequest("localhost is not allowed unless explicitly allowlisted")
	}
	addr, err := netip.ParseAddr(normalizedHost)
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return badRequest("unspecified IP addresses are not allowed")
	}
	linkLocalMetadata := netip.MustParsePrefix("169.254.0.0/16")
	if linkLocalMetadata.Contains(addr) {
		return badRequest("metadata and link-local IP addresses are not allowed")
	}
	if addr.IsLoopback() && !hostAllowed {
		return badRequest("loopback IP addresses are not allowed unless explicitly allowlisted")
	}
	return nil
}

func isConfiguredHTTPMethodAllowed(method string, rawAllowedMethods []string) bool {
	allowed := effectiveHTTPAllowedMethods(rawAllowedMethods)
	_, ok := allowed[method]
	return ok
}

func effectiveHTTPAllowedMethods(rawAllowedMethods []string) map[string]struct{} {
	if len(rawAllowedMethods) == 0 {
		rawAllowedMethods = defaultHTTPAllowedMethods
	}
	allowed := map[string]struct{}{}
	for _, method := range rawAllowedMethods {
		normalized := strings.ToUpper(strings.TrimSpace(method))
		if normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return allowed
}

func isHTTPSafeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func isHTTPWriteMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isHTTPForbiddenRequestHeader(header string) bool {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "authorization", "cookie", "set-cookie", "host", "proxy-authorization":
		return true
	default:
		return false
	}
}

func isHTTPForbiddenHTTPSecretRefHeader(header string) bool {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "cookie", "set-cookie", "host", "proxy-authorization":
		return true
	default:
		return false
	}
}

func isValidHTTPHeaderName(header string) bool {
	if header == "" {
		return false
	}
	for i := 0; i < len(header); i++ {
		if !isHTTPTokenByte(header[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenByte(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return true
	case ch >= 'A' && ch <= 'Z':
		return true
	case ch >= '0' && ch <= '9':
		return true
	}
	switch ch {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func isValidHTTPHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\r', '\n', 0:
			return false
		}
	}
	return true
}

func encodeHTTPRequestBody(value any) ([]byte, string, error) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), "text/plain; charset=utf-8", nil
	case nil:
		return []byte("null"), "application/json", nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, "", fmt.Errorf("encode http request body: %w", err)
		}
		return raw, "application/json", nil
	}
}

func hasHTTPHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func readHTTPResponseBody(body io.Reader, maxBytes int) (any, bool, error) {
	limited := io.LimitReader(body, int64(maxBytes)+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, fmt.Errorf("read http response body: %w", err)
	}
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}
	var decoded any
	if len(raw) > 0 && json.Unmarshal(raw, &decoded) == nil {
		return redactJSONValueByKey(decoded), truncated, nil
	}
	return string(raw), truncated, nil
}

func redactHTTPHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isSensitiveJSONKey(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = strings.Join(headers.Values(key), ",")
	}
	return result
}

func redactHTTPURLForTelemetry(value *url.URL) string {
	if value == nil {
		return ""
	}
	redacted := *value
	redacted.User = nil
	redacted.Fragment = ""
	query := redacted.Query()
	for key := range query {
		if isSensitiveJSONKey(key) {
			query.Set(key, "[REDACTED]")
		}
	}
	redacted.RawQuery = query.Encode()
	return redacted.String()
}

func effectiveHTTPTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return time.Duration(defaultHTTPTimeoutMs) * time.Millisecond
	}
	max := time.Duration(hardHTTPTimeoutMs) * time.Millisecond
	if value > max {
		return max
	}
	return value
}

func effectiveHTTPMaxResponseBytes(value int) int {
	if value <= 0 {
		return defaultHTTPMaxResponseBytes
	}
	if value > hardHTTPMaxResponseBytes {
		return hardHTTPMaxResponseBytes
	}
	return value
}

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"agenttoolgate/backend/internal/mcp"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultMCPOutboundTimeoutMs = 3000
	hardMCPOutboundTimeoutMs    = 30000
)

var (
	mcpRemoteToolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

type mcpConnectorConfig struct {
	Transport        string            `json:"transport"`
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers"`
	HeaderSecretRefs map[string]string `json:"headerSecretRefs"`
	TimeoutMs        int               `json:"timeoutMs,omitempty"`
}

type syncConnectorResponse struct {
	Connector    model.Connector `json:"connector"`
	CreatedTools []string        `json:"createdTools"`
	UpdatedTools []string        `json:"updatedTools"`
	SkippedTools []string        `json:"skippedTools"`
	StaleTools   []string        `json:"staleTools"`
}

func (a *App) handleSyncConnector(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireManageConnectors(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	connectorID := chi.URLParam(r, "id")
	if strings.TrimSpace(connectorID) == "" {
		a.respondError(w, badRequest("connector id is required"))
		return
	}

	connector, err := a.store.GetConnectorByID(r.Context(), reqCtx.Workspace.ID, connectorID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	if strings.ToLower(strings.TrimSpace(connector.Type)) != "mcp" {
		a.respondError(w, badRequest("connector must be type mcp"))
		return
	}
	if !connector.Enabled {
		a.respondError(w, badRequest("connector is disabled"))
		return
	}

	synced, err := a.syncMCPConnector(r.Context(), reqCtx.Workspace.ID, connector)
	if err != nil {
		a.respondError(w, err)
		return
	}
	synced.Connector = redactConnectorForResponse(synced.Connector)

	writeJSON(w, http.StatusOK, synced)
}

func (a *App) syncMCPConnector(ctx context.Context, workspaceID string, connector model.Connector) (syncConnectorResponse, error) {
	cfg, err := parseMCPConnectorConfig(connector.ConfigJSON)
	if err != nil {
		return syncConnectorResponse{}, err
	}
	if strings.TrimSpace(cfg.Transport) == "" {
		cfg.Transport = "sse"
	}
	if strings.ToLower(strings.TrimSpace(cfg.Transport)) != "sse" {
		return syncConnectorResponse{}, badRequest("mcp connector transport must be sse")
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return syncConnectorResponse{}, badRequest("mcp connector url is required")
	}
	if _, err := parseAndValidateMCPConnectorURL(cfg.URL); err != nil {
		return syncConnectorResponse{}, err
	}
	if err := validateMCPConnectorHeaders(cfg.Headers); err != nil {
		return syncConnectorResponse{}, err
	}
	if err := validateMCPConnectorHeaderSecretRefs(cfg.HeaderSecretRefs); err != nil {
		return syncConnectorResponse{}, err
	}
	resolvedHeaders, err := a.resolveMCPConnectorHeadersForWorkspace(ctx, workspaceID, cfg)
	if err != nil {
		return syncConnectorResponse{}, err
	}

	client := mcp.NewOutboundClient(cfg.URL, resolvedHeaders, effectiveMCPTimeout(cfg.TimeoutMs))
	remoteTools, err := client.SyncTools(ctx)
	if err != nil {
		return syncConnectorResponse{}, fmt.Errorf("mcp outbound sync failed")
	}

	namespace := mcpConnectorNamespace(connector.Name)
	created := make([]string, 0, len(remoteTools))
	updated := make([]string, 0)
	skipped := make([]string, 0)
	remoteKeys := map[string]struct{}{}
	for _, remoteTool := range remoteTools {
		toolName := normalizeMCPRemoteToolName(remoteTool.Name)
		if toolName == "" {
			continue
		}
		key := namespace + "." + toolName
		remoteKeys[key] = struct{}{}
		operationType, riskLevel, requiresApproval := inferMCPToolGovernance(remoteTool)
		input := mcpCreateToolInput(workspaceID, namespace, toolName, connector, remoteTool, operationType, riskLevel, requiresApproval)
		if existing, err := a.store.GetToolByKey(ctx, workspaceID, key); err == nil {
			if _, updateErr := a.store.UpdateTool(ctx, workspaceID, existing.ID, model.UpdateToolInput{
				DisplayName:      input.DisplayName,
				Description:      input.Description,
				OperationType:    input.OperationType,
				RiskLevel:        input.RiskLevel,
				RequiresApproval: &input.RequiresApproval,
				InputSchemaJSON:  input.InputSchemaJSON,
				OutputSchemaJSON: input.OutputSchemaJSON,
				// 不传 Enabled，确保 sync 不覆盖人工停用状态。
			}); updateErr != nil {
				return syncConnectorResponse{}, updateErr
			}
			updated = append(updated, key)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return syncConnectorResponse{}, err
		}

		tool, createErr := a.store.CreateTool(ctx, input)
		if createErr != nil {
			if errors.Is(createErr, store.ErrConflict) {
				skipped = append(skipped, key)
				continue
			}
			return syncConnectorResponse{}, createErr
		}
		created = append(created, tool.Key())
	}
	stale, err := a.mcpStaleToolKeys(ctx, workspaceID, namespace, remoteKeys)
	if err != nil {
		return syncConnectorResponse{}, err
	}

	return syncConnectorResponse{
		Connector:    connector,
		CreatedTools: created,
		UpdatedTools: updated,
		SkippedTools: skipped,
		StaleTools:   stale,
	}, nil
}

func (a *App) validateMCPToolCallBeforePolicy(ctx context.Context, workspaceID string, tool model.Tool, decodedArgs any) error {
	connectorName := mcpConnectorNameFromNamespace(tool.Namespace)
	if connectorName == "" {
		return badRequest("mcp tool namespace is invalid")
	}
	connector, err := a.lookupMCPConnector(ctx, tool.WorkspaceID, connectorName)
	if err != nil {
		return err
	}
	if !connector.Enabled {
		return badRequest("connector is disabled")
	}
	cfg, err := parseMCPConnectorConfig(connector.ConfigJSON)
	if err != nil {
		return err
	}
	if effectiveMCPTransport(cfg.Transport) != "sse" {
		return badRequest("mcp connector transport must be sse")
	}
	if _, err := parseAndValidateMCPConnectorURL(cfg.URL); err != nil {
		return err
	}
	if err := validateMCPConnectorHeaders(cfg.Headers); err != nil {
		return err
	}
	if err := validateMCPConnectorHeaderSecretRefs(cfg.HeaderSecretRefs); err != nil {
		return err
	}
	if _, err := a.resolveMCPConnectorHeadersForWorkspace(ctx, workspaceID, cfg); err != nil {
		return err
	}
	return validateMCPToolArguments(decodedArgs)
}

func (a *App) executeMCPTool(ctx context.Context, tool model.Tool, decodedArgs any) (resultPayload map[string]any, resultJSON json.RawMessage, err error) {
	connectorName := mcpConnectorNameFromNamespace(tool.Namespace)
	if connectorName == "" {
		return nil, nil, badRequest("mcp tool namespace is invalid")
	}
	ctx, span := telemetry.StartSpan(ctx, "connector.mcp."+tool.Name,
		attribute.String("tool.key", tool.Key()),
		attribute.String("mcp.connector", connectorName),
		attribute.String("mcp.tool", tool.Name),
	)
	defer func() {
		if err != nil {
			telemetry.RecordError(span, err)
		}
		span.End()
	}()

	connector, err := a.lookupMCPConnector(ctx, tool.WorkspaceID, connectorName)
	if err != nil {
		return nil, nil, err
	}
	if !connector.Enabled {
		return nil, nil, badRequest("connector is disabled")
	}
	cfg, err := parseMCPConnectorConfig(connector.ConfigJSON)
	if err != nil {
		return nil, nil, err
	}
	if effectiveMCPTransport(cfg.Transport) != "sse" {
		return nil, nil, badRequest("mcp connector transport must be sse")
	}
	if _, err := parseAndValidateMCPConnectorURL(cfg.URL); err != nil {
		return nil, nil, err
	}
	if err := validateMCPConnectorHeaders(cfg.Headers); err != nil {
		return nil, nil, err
	}
	if err := validateMCPConnectorHeaderSecretRefs(cfg.HeaderSecretRefs); err != nil {
		return nil, nil, err
	}
	resolvedHeaders, err := a.resolveMCPConnectorHeadersForWorkspace(ctx, tool.WorkspaceID, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := validateMCPToolArguments(decodedArgs); err != nil {
		return nil, nil, err
	}

	client := mcp.NewOutboundClient(cfg.URL, resolvedHeaders, effectiveMCPTimeout(cfg.TimeoutMs))
	result, err := client.CallTool(ctx, tool.Name, defaultJSON(marshalDecodedValue(decodedArgs)))
	if err != nil {
		return nil, nil, fmt.Errorf("mcp outbound tool call failed")
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("mcp tool result must be an object")
	}
	if isMCPToolError(resultMap) {
		return nil, nil, fmt.Errorf("mcp tool %s returned an error", tool.Key())
	}
	resultJSON, err = json.Marshal(resultMap)
	if err != nil {
		return nil, nil, err
	}
	redactedJSON := redactMCPToolOutputForAudit(resultJSON)
	redactedResult, err := decodeJSONValue(redactedJSON)
	if err != nil {
		return nil, nil, err
	}
	redactedMap, ok := redactedResult.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("mcp tool result must be an object")
	}
	return redactedMap, redactedJSON, nil
}

func (a *App) lookupMCPConnector(ctx context.Context, workspaceID, connectorName string) (model.Connector, error) {
	connectors, err := a.store.ListConnectors(ctx, workspaceID)
	if err != nil {
		return model.Connector{}, err
	}
	for _, connector := range connectors {
		if strings.EqualFold(strings.TrimSpace(connector.Type), "mcp") && strings.EqualFold(strings.TrimSpace(connector.Name), connectorName) {
			return connector, nil
		}
	}
	return model.Connector{}, store.ErrNotFound
}

func parseMCPConnectorConfig(raw json.RawMessage) (mcpConnectorConfig, error) {
	var cfg mcpConnectorConfig
	if len(raw) == 0 {
		return cfg, badRequest("mcp connector config is required")
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return mcpConnectorConfig{}, badRequest("mcp connector config is invalid")
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if cfg.HeaderSecretRefs == nil {
		cfg.HeaderSecretRefs = map[string]string{}
	}
	cfg.Transport = strings.ToLower(strings.TrimSpace(cfg.Transport))
	cfg.URL = strings.TrimSpace(cfg.URL)
	return cfg, nil
}

func mcpCreateToolInput(workspaceID, namespace, toolName string, connector model.Connector, remoteTool mcp.OutboundTool, operationType, riskLevel string, requiresApproval bool) model.CreateToolInput {
	displayName := strings.TrimSpace(remoteTool.Title)
	if displayName == "" {
		displayName = strings.TrimSpace(connector.DisplayName)
		if displayName == "" {
			displayName = connector.Name
		}
		displayName = displayName + " / " + toolName
	}
	description := strings.TrimSpace(remoteTool.Description)
	if description == "" {
		description = "Synced from MCP connector " + connector.Name
	}
	inputSchema := remoteTool.InputSchema
	if len(inputSchema) == 0 {
		inputSchema = json.RawMessage(`{"type":"object"}`)
	}
	return model.CreateToolInput{
		WorkspaceID:      workspaceID,
		Namespace:        namespace,
		Name:             toolName,
		DisplayName:      displayName,
		Description:      description,
		OperationType:    operationType,
		RiskLevel:        riskLevel,
		RequiresApproval: requiresApproval,
		InputSchemaJSON:  inputSchema,
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	}
}

func (a *App) mcpStaleToolKeys(ctx context.Context, workspaceID, namespace string, remoteKeys map[string]struct{}) ([]string, error) {
	tools, err := a.store.ListTools(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	stale := make([]string, 0)
	for _, tool := range tools {
		if !strings.EqualFold(strings.TrimSpace(tool.Namespace), namespace) {
			continue
		}
		if _, ok := remoteKeys[tool.Key()]; !ok {
			stale = append(stale, tool.Key())
		}
	}
	sort.Strings(stale)
	return stale, nil
}

func mcpConnectorNamespace(connectorName string) string {
	return "mcp_" + strings.ToLower(strings.TrimSpace(connectorName))
}

func mcpConnectorNameFromNamespace(namespace string) string {
	trimmed := strings.ToLower(strings.TrimSpace(namespace))
	if !strings.HasPrefix(trimmed, "mcp_") {
		return ""
	}
	return strings.TrimPrefix(trimmed, "mcp_")
}

func effectiveMCPTransport(transport string) string {
	normalized := strings.ToLower(strings.TrimSpace(transport))
	if normalized == "" {
		return "sse"
	}
	return normalized
}

func normalizeMCPRemoteToolName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if !mcpRemoteToolNamePattern.MatchString(trimmed) {
		return ""
	}
	return trimmed
}

func inferMCPToolGovernance(tool mcp.OutboundTool) (string, string, bool) {
	if tool.Annotations != nil {
		if destructive, ok := tool.Annotations["destructiveHint"].(bool); ok && destructive {
			return "delete", "high", true
		}
		if openWorld, ok := tool.Annotations["openWorldHint"].(bool); ok && openWorld {
			return "write", "medium", true
		}
		if readOnly, ok := tool.Annotations["readOnlyHint"].(bool); ok && readOnly {
			return "read", "low", false
		}
	}

	lower := strings.ToLower(strings.TrimSpace(tool.Name))
	switch {
	case strings.HasPrefix(lower, "get"), strings.HasPrefix(lower, "list"), strings.HasPrefix(lower, "fetch"), strings.HasPrefix(lower, "search"):
		return "read", "low", false
	case strings.HasPrefix(lower, "delete"), strings.HasPrefix(lower, "remove"), strings.HasPrefix(lower, "destroy"):
		return "delete", "high", true
	case strings.HasPrefix(lower, "create"), strings.HasPrefix(lower, "update"), strings.HasPrefix(lower, "write"), strings.HasPrefix(lower, "post"), strings.HasPrefix(lower, "send"), strings.HasPrefix(lower, "call"):
		return "create", "medium", true
	default:
		return "write", "medium", true
	}
}

func isMCPToolError(result map[string]any) bool {
	value, ok := result["isError"]
	if !ok {
		return false
	}
	typed, ok := value.(bool)
	return ok && typed
}

func effectiveMCPTimeout(timeoutMs int) time.Duration {
	if timeoutMs <= 0 {
		return time.Duration(defaultMCPOutboundTimeoutMs) * time.Millisecond
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	maxTimeout := time.Duration(hardMCPOutboundTimeoutMs) * time.Millisecond
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func marshalDecodedValue(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func parseAndValidateMCPConnectorURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, badRequest("mcp connector url is invalid")
	}
	if parsed.User != nil {
		return nil, badRequest("mcp connector url userinfo is not allowed")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, badRequest("mcp connector url must use http or https")
	}
	return parsed, nil
}

func validateMCPConnectorHeaders(headers map[string]string) error {
	for key, value := range headers {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return badRequest("mcp connector header name is required")
		}
		if !isValidHTTPHeaderName(trimmedKey) {
			return badRequest("mcp connector header name is invalid")
		}
		if isMCPForbiddenConnectorHeader(trimmedKey) {
			return badRequest(fmt.Sprintf("mcp connector header %s is not allowed", trimmedKey))
		}
		if !isValidHTTPHeaderValue(value) {
			return badRequest(fmt.Sprintf("mcp connector header %s value is invalid", trimmedKey))
		}
	}
	return nil
}

func validateMCPConnectorHeaderSecretRefs(refs map[string]string) error {
	for key, ref := range refs {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return badRequest("mcp connector header secret ref header name is required")
		}
		if !isValidHTTPHeaderName(trimmedKey) {
			return badRequest("mcp connector header secret ref header name is invalid")
		}
		if isMCPForbiddenConnectorHeader(trimmedKey) {
			return badRequest(fmt.Sprintf("mcp connector header %s is not allowed", trimmedKey))
		}
		trimmedRef := strings.TrimSpace(ref)
		if !isValidSecretReferenceValue(trimmedRef) {
			return badRequest("mcp connector header secret ref is invalid")
		}
	}
	return nil
}

func resolveMCPConnectorHeaders(cfg mcpConnectorConfig) (map[string]string, error) {
	resolved := make(map[string]string, len(cfg.Headers)+len(cfg.HeaderSecretRefs))
	for key, value := range cfg.Headers {
		resolved[strings.TrimSpace(key)] = value
	}
	for key, ref := range cfg.HeaderSecretRefs {
		trimmedKey := strings.TrimSpace(key)
		for existing := range resolved {
			if strings.EqualFold(existing, trimmedKey) {
				return nil, badRequest(fmt.Sprintf("mcp connector header %s cannot be defined in both headers and headerSecretRefs", trimmedKey))
			}
		}
		trimmedRef := strings.TrimSpace(ref)
		secretValue := strings.TrimSpace(os.Getenv(trimmedRef))
		if secretValue == "" {
			return nil, badRequest(fmt.Sprintf("mcp connector header secret ref %s is not configured", trimmedRef))
		}
		if !isValidHTTPHeaderValue(secretValue) {
			return nil, badRequest(fmt.Sprintf("mcp connector header secret ref %s value is invalid", trimmedRef))
		}
		resolved[trimmedKey] = secretValue
	}
	return resolved, nil
}

func (a *App) resolveMCPConnectorHeadersForWorkspace(ctx context.Context, workspaceID string, cfg mcpConnectorConfig) (map[string]string, error) {
	resolved := make(map[string]string, len(cfg.Headers)+len(cfg.HeaderSecretRefs))
	for key, value := range cfg.Headers {
		resolved[strings.TrimSpace(key)] = value
	}
	for key, ref := range cfg.HeaderSecretRefs {
		trimmedKey := strings.TrimSpace(key)
		for existing := range resolved {
			if strings.EqualFold(existing, trimmedKey) {
				return nil, badRequest(fmt.Sprintf("mcp connector header %s cannot be defined in both headers and headerSecretRefs", trimmedKey))
			}
		}
		trimmedRef := strings.TrimSpace(ref)
		secretValue, err := a.resolveLegacyEnvSecretRefValue(ctx, workspaceID, trimmedRef)
		if err != nil {
			return nil, err
		}
		if !isValidHTTPHeaderValue(secretValue) {
			return nil, badRequest(fmt.Sprintf("mcp connector header secret ref %s value is invalid", trimmedRef))
		}
		resolved[trimmedKey] = secretValue
	}
	return resolved, nil
}

func isMCPForbiddenConnectorHeader(header string) bool {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "host", "set-cookie", "proxy-authorization":
		return true
	default:
		return false
	}
}

func validateMCPToolArguments(decodedArgs any) error {
	if _, ok := decodedArgs.(map[string]any); !ok {
		return badRequest("mcp tool arguments must be a JSON object")
	}
	return nil
}

func redactMCPToolInputForAudit(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return defaultJSON(raw)
	}
	return mustBootstrapConnectorJSON(redactMCPAuditValue(decoded, true))
}

func redactMCPToolOutputForAudit(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return defaultJSON(raw)
	}
	redacted := redactJSONValueByValue(redactMCPAuditValue(decoded, true))
	return mustBootstrapConnectorJSON(redacted)
}

func redactMCPAuditValue(value any, redactBodyKey bool) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if isSecretReferenceJSONKey(key) {
				redacted[key] = item
				continue
			}
			if isSensitiveJSONKey(key) || (redactBodyKey && normalizedKey == "body") {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = redactMCPAuditValue(item, redactBodyKey)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactMCPAuditValue(item, redactBodyKey)
		}
		return redacted
	default:
		return typed
	}
}

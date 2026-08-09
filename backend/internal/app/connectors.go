package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/go-chi/chi/v5"
)

// DefaultBootstrapConnectors 返回启动时自动创建的 connector。
// 记录以环境变量为初始值；HTTP、GitHub 和 MCP 会在运行时读取可治理字段。
func DefaultBootstrapConnectors(cfg config.Config) []model.BootstrapConnectorInput {
	return []model.BootstrapConnectorInput{
		{
			Type:        "database",
			Name:        "local_postgres",
			DisplayName: "Local PostgreSQL",
			ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
				"datasource": cfg.DatabaseQueryDatasource,
				"mode":       "reference",
			}),
			Enabled: true,
		},
		{
			Type:        "github",
			Name:        "default",
			DisplayName: "GitHub Default",
			ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
				"apiBaseURL":   cfg.GitHubAPIBaseURL,
				"allowedRepos": cfg.GitHubAllowedRepos,
			}),
			Enabled: true,
		},
		{
			Type:        "http",
			Name:        "default",
			DisplayName: "HTTP Default",
			ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
				"allowedHosts":   cfg.HTTPAllowedHosts,
				"allowedMethods": cfg.HTTPAllowedMethods,
			}),
			Enabled: true,
		},
	}
}

func (a *App) ensureBuiltinConnectors(ctx context.Context, workspaceID string) error {
	for _, input := range DefaultBootstrapConnectors(a.cfg) {
		_, err := a.store.CreateConnector(ctx, model.CreateConnectorInput{
			WorkspaceID: workspaceID,
			Type:        input.Type,
			Name:        input.Name,
			DisplayName: input.DisplayName,
			ConfigJSON:  input.ConfigJSON,
			Enabled:     input.Enabled,
		})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func mustBootstrapConnectorJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

type createConnectorRequest struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	ConfigJSON  json.RawMessage `json:"configJson"`
	Enabled     *bool           `json:"enabled"`
}

type updateConnectorRequest struct {
	DisplayName *string         `json:"displayName,omitempty"`
	ConfigJSON  json.RawMessage `json:"configJson,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
}

func (a *App) handleListConnectors(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireManageConnectors(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	connectors, err := a.store.ListConnectors(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": redactConnectorsForResponse(connectors)})
}

func (a *App) handleGetConnector(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, redactConnectorForResponse(connector))
}

func (a *App) handleCreateConnector(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, errors.New("missing request context"))
		return
	}
	if err := requireManageConnectors(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req createConnectorRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}

	connectorType, err := normalizeConnectorType(req.Type)
	if err != nil {
		a.respondError(w, err)
		return
	}
	name, err := normalizeConnectorName(req.Name)
	if err != nil {
		a.respondError(w, err)
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = connectorType + "." + name
	}

	configJSON, err := normalizeConnectorConfigJSONForType(connectorType, req.ConfigJSON)
	if err != nil {
		a.respondError(w, err)
		return
	}

	connector, err := a.store.CreateConnector(r.Context(), model.CreateConnectorInput{
		WorkspaceID: reqCtx.Workspace.ID,
		Type:        connectorType,
		Name:        name,
		DisplayName: displayName,
		ConfigJSON:  configJSON,
		Enabled:     boolValue(req.Enabled, true),
	})
	if err != nil {
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, redactConnectorForResponse(connector))
}

func (a *App) handlePatchConnector(w http.ResponseWriter, r *http.Request) {
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

	var req updateConnectorRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}

	var displayName string
	if req.DisplayName != nil {
		displayName = strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			a.respondError(w, badRequest("displayName cannot be empty"))
			return
		}
	}
	existing, err := a.store.GetConnectorByID(r.Context(), reqCtx.Workspace.ID, connectorID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	configJSON := json.RawMessage{}
	if len(req.ConfigJSON) > 0 {
		normalized, err := normalizeConnectorConfigJSONForType(existing.Type, req.ConfigJSON)
		if err != nil {
			a.respondError(w, err)
			return
		}
		configJSON = normalized
	}

	connector, err := a.store.UpdateConnector(r.Context(), reqCtx.Workspace.ID, connectorID, model.UpdateConnectorInput{
		DisplayName: displayName,
		ConfigJSON:  configJSON,
		Enabled:     req.Enabled,
	})
	if err != nil {
		a.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, redactConnectorForResponse(connector))
}

func normalizeConnectorType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "database", "github", "http", "mcp":
		return strings.ToLower(strings.TrimSpace(raw)), nil
	default:
		return "", badRequest("connector type must be database, github, http, or mcp")
	}
}

func normalizeConnectorName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", badRequest("connector name is required")
	}
	for index := 0; index < len(name); index++ {
		ch := name[index]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '_' || ch == '-':
		default:
			return "", badRequest("connector name may only contain lowercase letters, numbers, hyphens, and underscores")
		}
	}
	return name, nil
}

func normalizeConnectorConfigJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, badRequest("configJson must be valid JSON")
	}
	if decoded == nil {
		return json.RawMessage(`{}`), nil
	}
	redacted := redactConnectorConfigValue(decoded)
	normalized, err := json.Marshal(redacted)
	if err != nil {
		return nil, fmt.Errorf("encode connector config: %w", err)
	}
	return normalized, nil
}

func normalizeConnectorConfigJSONForType(connectorType string, raw json.RawMessage) (json.RawMessage, error) {
	normalized, err := normalizeConnectorConfigJSON(raw)
	if err != nil || !strings.EqualFold(strings.TrimSpace(connectorType), "mcp") {
		return normalized, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		return nil, badRequest("configJson must be a JSON object")
	}
	decoded["secretRefMode"] = mcpSecretRefModeWorkspace
	return json.Marshal(decoded)
}

func redactConnectorsForResponse(connectors []model.Connector) []model.Connector {
	items := make([]model.Connector, 0, len(connectors))
	for _, connector := range connectors {
		items = append(items, redactConnectorForResponse(connector))
	}
	return items
}

func redactConnectorForResponse(connector model.Connector) model.Connector {
	connector.ConfigJSON = redactConnectorConfigJSONForResponse(connector.ConfigJSON)
	return connector
}

func redactConnectorConfigJSONForResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage(`{}`)
	}
	redacted, err := json.Marshal(redactJSONValueByValue(redactConnectorConfigValue(decoded)))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return redacted
}

func redactConnectorConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "allowedHosts") {
				// allowlist 是运行时治理配置，不包含凭据，必须原样保存。
				redacted[key] = redactConnectorConfigValue(item)
				continue
			}
			if strings.EqualFold(strings.TrimSpace(key), "headerSecretRefs") {
				// headerSecretRefs 保存 workspace Secret 名称，不保存解析后的密钥值。
				redacted[key] = cloneConnectorSecretRefs(item)
				continue
			}
			if isSecretReferenceJSONKey(key) {
				redacted[key] = item
				continue
			}
			if isSensitiveJSONKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = redactConnectorConfigValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactConnectorConfigValue(item)
		}
		return redacted
	default:
		return typed
	}
}

func cloneConnectorSecretRefs(value any) any {
	refs, ok := value.(map[string]any)
	if !ok {
		return value
	}
	cloned := make(map[string]any, len(refs))
	for key, ref := range refs {
		cloned[key] = ref
	}
	return cloned
}

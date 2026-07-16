package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/go-chi/chi/v5"
)

type createSecretRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Enabled     *bool           `json:"enabled"`
	SecretType  string          `json:"secretType"`
	ValueSource string          `json:"valueSource"`
	ValueRef    string          `json:"valueRef"`
	Metadata    json.RawMessage `json:"metadata"`
}

type updateSecretRequest = createSecretRequest

func (a *App) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	secrets, err := a.store.ListSecrets(r.Context(), reqCtx.Workspace.ID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	secretItems, err := a.decorateSecretBindings(r.Context(), reqCtx.Workspace.ID, secrets)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": secretItems})
}

func (a *App) handleGetSecret(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	secretID := strings.TrimSpace(chi.URLParam(r, "id"))
	if secretID == "" {
		a.respondError(w, badRequest("secret id is required"))
		return
	}

	secret, err := a.store.GetSecretByID(r.Context(), reqCtx.Workspace.ID, secretID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	decorated, err := a.decorateSecretBindings(r.Context(), reqCtx.Workspace.ID, []model.Secret{secret})
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decorated[0])
}

func (a *App) handleGetSecretUsage(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	secretID := strings.TrimSpace(chi.URLParam(r, "id"))
	if secretID == "" {
		a.respondError(w, badRequest("secret id is required"))
		return
	}

	usage, err := a.buildSecretUsageResponse(r.Context(), reqCtx.Workspace.ID, secretID)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (a *App) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req createSecretRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	input, err := normalizeSecretCreateRequest(reqCtx.Workspace, req)
	if err != nil {
		a.respondError(w, err)
		return
	}

	secret, err := a.store.CreateSecret(r.Context(), input)
	if err != nil {
		a.respondError(w, err)
		return
	}
	decorated, err := a.decorateSecretBindings(r.Context(), reqCtx.Workspace.ID, []model.Secret{secret})
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, decorated[0])
}

func (a *App) handleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	secretID := strings.TrimSpace(chi.URLParam(r, "id"))
	if secretID == "" {
		a.respondError(w, badRequest("secret id is required"))
		return
	}

	var req updateSecretRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	input, err := normalizeSecretUpdateRequest(req)
	if err != nil {
		a.respondError(w, err)
		return
	}

	secret, err := a.store.UpdateSecret(r.Context(), reqCtx.Workspace.ID, secretID, input)
	if err != nil {
		a.respondError(w, err)
		return
	}
	decorated, err := a.decorateSecretBindings(r.Context(), reqCtx.Workspace.ID, []model.Secret{secret})
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, decorated[0])
}

func (a *App) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManageSecrets(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	secretID := strings.TrimSpace(chi.URLParam(r, "id"))
	if secretID == "" {
		a.respondError(w, badRequest("secret id is required"))
		return
	}

	forceDelete := parseBoolQuery(r, "force")
	if !forceDelete {
		usage, err := a.buildSecretUsageResponse(r.Context(), reqCtx.Workspace.ID, secretID)
		if err != nil {
			a.respondError(w, err)
			return
		}
		if !usage.CanDelete {
			writeJSON(w, http.StatusConflict, usage)
			return
		}
	}

	if err := a.store.DeleteSecret(r.Context(), reqCtx.Workspace.ID, secretID); err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func parseBoolQuery(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "y":
		return true
	default:
		return false
	}
}

func normalizeSecretCreateRequest(workspace model.Workspace, req createSecretRequest) (model.CreateSecretInput, error) {
	name, err := store.NormalizeSecretName(req.Name)
	if err != nil {
		return model.CreateSecretInput{}, badRequest(err.Error())
	}
	secretType := store.NormalizeSecretType(req.SecretType)
	if err := store.ValidateSecretType(secretType); err != nil {
		return model.CreateSecretInput{}, badRequest(err.Error())
	}
	valueSource := store.NormalizeSecretValueSource(req.ValueSource)
	if valueSource == "" {
		valueSource = "env"
	}
	if err := store.ValidateSecretValueSource(valueSource); err != nil {
		return model.CreateSecretInput{}, badRequest(err.Error())
	}
	valueRef, err := store.NormalizeSecretValueRef(req.ValueRef)
	if err != nil {
		return model.CreateSecretInput{}, badRequest(err.Error())
	}
	metadata, err := store.NormalizeSecretMetadata(req.Metadata)
	if err != nil {
		return model.CreateSecretInput{}, badRequest(err.Error())
	}

	return model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           name,
		Description:    strings.TrimSpace(req.Description),
		Enabled:        boolValue(req.Enabled, true),
		SecretType:     secretType,
		ValueSource:    valueSource,
		ValueRef:       valueRef,
		Metadata:       metadata,
	}, nil
}

func normalizeSecretUpdateRequest(req updateSecretRequest) (model.UpdateSecretInput, error) {
	name, err := store.NormalizeSecretName(req.Name)
	if err != nil {
		return model.UpdateSecretInput{}, badRequest(err.Error())
	}
	secretType := store.NormalizeSecretType(req.SecretType)
	if err := store.ValidateSecretType(secretType); err != nil {
		return model.UpdateSecretInput{}, badRequest(err.Error())
	}
	valueSource := store.NormalizeSecretValueSource(req.ValueSource)
	if valueSource == "" {
		valueSource = "env"
	}
	if err := store.ValidateSecretValueSource(valueSource); err != nil {
		return model.UpdateSecretInput{}, badRequest(err.Error())
	}
	valueRef, err := store.NormalizeSecretValueRef(req.ValueRef)
	if err != nil {
		return model.UpdateSecretInput{}, badRequest(err.Error())
	}
	metadata, err := store.NormalizeSecretMetadata(req.Metadata)
	if err != nil {
		return model.UpdateSecretInput{}, badRequest(err.Error())
	}

	return model.UpdateSecretInput{
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		Enabled:     req.Enabled,
		SecretType:  secretType,
		ValueSource: valueSource,
		ValueRef:    valueRef,
		Metadata:    metadata,
	}, nil
}

func (a *App) decorateSecretBindings(ctx context.Context, workspaceID string, secrets []model.Secret) ([]model.Secret, error) {
	if len(secrets) == 0 {
		return []model.Secret{}, nil
	}

	connectors, err := a.store.ListConnectors(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	secretByName := make(map[string]int, len(secrets))
	for index := range secrets {
		secrets[index].Bindings = nil
		secretByName[strings.ToLower(strings.TrimSpace(secrets[index].Name))] = index
	}

	a.collectSecretBindingsFromConnectors(secretByName, secrets, connectors)

	for index := range secrets {
		secrets[index].Bindings = store.CloneSecretBindings(secrets[index].Bindings)
	}
	return secrets, nil
}

func (a *App) buildSecretUsageResponse(ctx context.Context, workspaceID, secretID string) (model.SecretUsageResponse, error) {
	secret, err := a.store.GetSecretByID(ctx, workspaceID, secretID)
	if err != nil {
		return model.SecretUsageResponse{}, err
	}
	decorated, err := a.decorateSecretBindings(ctx, workspaceID, []model.Secret{secret})
	if err != nil {
		return model.SecretUsageResponse{}, err
	}
	if len(decorated) == 0 {
		return model.SecretUsageResponse{}, store.ErrNotFound
	}
	secret = decorated[0]

	connectors, err := a.store.ListConnectors(ctx, workspaceID)
	if err != nil {
		return model.SecretUsageResponse{}, err
	}
	usages := a.collectSecretUsagesFromConnectors(secret.Name, connectors)
	reason := ""
	if len(usages) > 0 {
		reason = fmt.Sprintf("secret is referenced by %d connector(s)", len(uniqueSecretUsageConnectors(usages)))
	}
	return model.SecretUsageResponse{
		Secret:              secret,
		Usages:              usages,
		CanDelete:           len(usages) == 0,
		DeleteBlockedReason: reason,
	}, nil
}

func (a *App) collectSecretBindingsFromConnectors(secretByName map[string]int, secrets []model.Secret, connectors []model.Connector) {
	for _, connector := range connectors {
		if !isSecretAwareConnectorType(connector.Type) {
			continue
		}
		var decoded any
		if err := json.Unmarshal(connector.ConfigJSON, &decoded); err != nil {
			continue
		}
		a.collectSecretBindingsFromValue(secretByName, secrets, connector, nil, decoded)
	}
}

func (a *App) collectSecretUsagesFromConnectors(secretName string, connectors []model.Connector) []model.SecretUsage {
	normalizedSecretName := strings.ToLower(strings.TrimSpace(secretName))
	usages := make([]model.SecretUsage, 0)
	for _, connector := range connectors {
		if !isSecretAwareConnectorType(connector.Type) {
			continue
		}
		var decoded any
		if err := json.Unmarshal(connector.ConfigJSON, &decoded); err != nil {
			continue
		}
		a.collectSecretUsagesFromValue(normalizedSecretName, connector, nil, decoded, &usages)
	}
	return usages
}

func isSecretAwareConnectorType(connectorType string) bool {
	return strings.EqualFold(strings.TrimSpace(connectorType), "github") ||
		strings.EqualFold(strings.TrimSpace(connectorType), "http") ||
		strings.EqualFold(strings.TrimSpace(connectorType), "mcp")
}

func uniqueSecretUsageConnectors(usages []model.SecretUsage) map[string]struct{} {
	unique := make(map[string]struct{}, len(usages))
	for _, usage := range usages {
		if strings.TrimSpace(usage.ConnectorID) != "" {
			unique[usage.ConnectorID] = struct{}{}
			continue
		}
		unique[usage.Target] = struct{}{}
	}
	return unique
}

func (a *App) collectSecretUsagesFromValue(secretName string, connector model.Connector, path []string, value any, usages *[]model.SecretUsage) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			nextPath := append(append([]string(nil), path...), key)
			if isSecretReferenceJSONKey(key) {
				a.attachSecretUsagesIfMatched(secretName, connector, nextPath, item, usages)
				continue
			}
			a.collectSecretUsagesFromValue(secretName, connector, nextPath, item, usages)
		}
	case []any:
		for index, item := range typed {
			nextPath := append(append([]string(nil), path...), fmt.Sprintf("[%d]", index))
			a.collectSecretUsagesFromValue(secretName, connector, nextPath, item, usages)
		}
	}
}

func (a *App) attachSecretUsagesIfMatched(secretName string, connector model.Connector, path []string, value any, usages *[]model.SecretUsage) {
	switch typed := value.(type) {
	case string:
		a.attachSecretUsage(secretName, connector, path, typed, usages)
	case map[string]any:
		for key, item := range typed {
			childPath := append(append([]string(nil), path...), key)
			if ref, ok := item.(string); ok {
				a.attachSecretUsage(secretName, connector, childPath, ref, usages)
			}
		}
	}
}

func (a *App) attachSecretUsage(secretName string, connector model.Connector, path []string, ref string, usages *[]model.SecretUsage) {
	if strings.ToLower(strings.TrimSpace(ref)) != secretName {
		return
	}
	field := strings.Join(path, ".")
	target := strings.TrimSpace(connector.Type) + "." + strings.TrimSpace(connector.Name)
	*usages = append(*usages, model.SecretUsage{
		Kind:                 "connector",
		ConnectorID:          connector.ID,
		ConnectorType:        connector.Type,
		ConnectorName:        connector.Name,
		ConnectorDisplayName: connector.DisplayName,
		Field:                field,
		Target:               target,
	})
}

func (a *App) collectSecretBindingsFromValue(secretByName map[string]int, secrets []model.Secret, connector model.Connector, path []string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			nextPath := append(append([]string(nil), path...), key)
			if isSecretReferenceJSONKey(key) {
				a.attachSecretBindingIfMatched(secretByName, secrets, connector, nextPath, item)
				continue
			}
			a.collectSecretBindingsFromValue(secretByName, secrets, connector, nextPath, item)
		}
	case []any:
		for index, item := range typed {
			nextPath := append(append([]string(nil), path...), fmt.Sprintf("[%d]", index))
			a.collectSecretBindingsFromValue(secretByName, secrets, connector, nextPath, item)
		}
	}
}

func (a *App) attachSecretBindingIfMatched(secretByName map[string]int, secrets []model.Secret, connector model.Connector, path []string, value any) {
	switch typed := value.(type) {
	case string:
		a.attachSecretBinding(secretByName, secrets, connector, path, typed)
	case map[string]any:
		for key, item := range typed {
			childPath := append(append([]string(nil), path...), key)
			if ref, ok := item.(string); ok {
				a.attachSecretBinding(secretByName, secrets, connector, childPath, ref)
			}
		}
	}
}

func (a *App) attachSecretBinding(secretByName map[string]int, secrets []model.Secret, connector model.Connector, path []string, ref string) {
	normalizedRef := strings.ToLower(strings.TrimSpace(ref))
	index, ok := secretByName[normalizedRef]
	if !ok {
		return
	}
	field := strings.Join(path, ".")
	target := strings.TrimSpace(connector.Type) + "." + strings.TrimSpace(connector.Name)
	secrets[index].Bindings = append(secrets[index].Bindings, model.SecretBinding{
		Kind:   "connector",
		Target: target,
		Field:  field,
	})
}

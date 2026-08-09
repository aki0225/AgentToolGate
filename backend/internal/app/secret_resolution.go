package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

var protectedRuntimeSecretEnvNames = map[string]struct{}{
	"AGENTTOOLGATE_API_KEY":      {},
	"AGENTTOOLGATE_BEARER_TOKEN": {},
	"AGT_LOCAL_REVIEWER_TOKEN":   {},
	"DATABASE_QUERY_URL":         {},
	"DATABASE_URL":               {},
	"GITHUB_TOKEN":               {},
	"OIDC_CLIENT_SECRET":         {},
	"OTEL_EXPORTER_OTLP_HEADERS": {},
	"PGPASSWORD":                 {},
	"POSTGRES_PASSWORD":          {},
	"ZITADEL_CLIENT_SECRET":      {},
}

func (a *App) resolveSecretRefValue(ctx context.Context, workspaceID, ref string) (string, error) {
	value, found, err := a.resolveWorkspaceSecretRefValue(ctx, workspaceID, ref)
	if err != nil {
		return "", err
	}
	if found {
		return value, nil
	}
	return "", badRequest(fmt.Sprintf("secret ref %s was not found", normalizeSecretReferenceValue(ref)))
}

func (a *App) resolveWorkspaceSecretRefValue(ctx context.Context, workspaceID, ref string) (string, bool, error) {
	normalizedRef := normalizeSecretReferenceValue(ref)
	if normalizedRef == "" {
		return "", false, badRequest("secret ref is required")
	}

	secret, err := a.store.GetSecretByName(ctx, workspaceID, normalizedRef)
	if err == nil {
		if !secret.Enabled {
			return "", false, badRequest(fmt.Sprintf("secret %s is disabled", secret.Name))
		}
		if strings.ToLower(strings.TrimSpace(secret.ValueSource)) != "env" {
			return "", false, badRequest(fmt.Sprintf("secret %s value source is not supported", secret.Name))
		}
		envName := strings.TrimSpace(secret.ValueRef)
		if isProtectedRuntimeSecretEnvName(envName) {
			return "", false, badRequest("secret valueRef refers to a protected runtime variable")
		}
		value := os.Getenv(envName)
		if strings.TrimSpace(value) == "" {
			return "", false, badRequest(fmt.Sprintf("secret %s is not configured", secret.Name))
		}
		return value, true, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	return "", false, nil
}

func isProtectedRuntimeSecretEnvName(value string) bool {
	_, ok := protectedRuntimeSecretEnvNames[strings.ToUpper(strings.TrimSpace(value))]
	return ok
}

func (a *App) resolveSecretRefs(ctx context.Context, workspaceID string, secretRefs map[string]string) (map[string]string, error) {
	if len(secretRefs) == 0 {
		return map[string]string{}, nil
	}
	resolved := make(map[string]string, len(secretRefs))
	for header, ref := range secretRefs {
		trimmedHeader := strings.TrimSpace(header)
		if trimmedHeader == "" {
			return nil, badRequest("secret ref header name is required")
		}
		value, err := a.resolveSecretRefValue(ctx, workspaceID, ref)
		if err != nil {
			return nil, err
		}
		resolved[trimmedHeader] = value
	}
	return resolved, nil
}

func lookupConnectorByTypeAndName(ctx context.Context, st store.Store, workspaceID, connectorType, connectorName string) (model.Connector, error) {
	connectors, err := st.ListConnectors(ctx, workspaceID)
	if err != nil {
		return model.Connector{}, err
	}
	for _, connector := range connectors {
		if strings.EqualFold(strings.TrimSpace(connector.Type), connectorType) && strings.EqualFold(strings.TrimSpace(connector.Name), connectorName) {
			return connector, nil
		}
	}
	return model.Connector{}, store.ErrNotFound
}

func (a *App) ensureConnectorEnabledIfPresent(ctx context.Context, workspaceID, connectorType, connectorName string) error {
	connector, err := lookupConnectorByTypeAndName(ctx, a.store, workspaceID, connectorType, connectorName)
	if errors.Is(err, store.ErrNotFound) {
		// 兼容仅通过环境变量配置、尚未创建 connector 记录的旧本地实例。
		return nil
	}
	if err != nil {
		return err
	}
	if !connector.Enabled {
		return badRequest(fmt.Sprintf("%s connector %s is disabled", connectorType, connectorName))
	}
	return nil
}

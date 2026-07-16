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

func (a *App) resolveSecretRefValue(ctx context.Context, workspaceID, ref string) (string, error) {
	return a.resolveSecretRefValueWithFallback(ctx, workspaceID, ref, false)
}

func (a *App) resolveLegacyEnvSecretRefValue(ctx context.Context, workspaceID, ref string) (string, error) {
	return a.resolveSecretRefValueWithFallback(ctx, workspaceID, ref, true)
}

func (a *App) resolveSecretRefValueWithFallback(ctx context.Context, workspaceID, ref string, allowEnvFallback bool) (string, error) {
	normalizedRef := normalizeSecretReferenceValue(ref)
	if normalizedRef == "" {
		return "", badRequest("secret ref is required")
	}

	secret, err := a.store.GetSecretByName(ctx, workspaceID, normalizedRef)
	if err == nil {
		if !secret.Enabled {
			return "", badRequest(fmt.Sprintf("secret %s is disabled", secret.Name))
		}
		if strings.ToLower(strings.TrimSpace(secret.ValueSource)) != "env" {
			return "", badRequest(fmt.Sprintf("secret %s value source is not supported", secret.Name))
		}
		envName := strings.TrimSpace(secret.ValueRef)
		value := os.Getenv(envName)
		if strings.TrimSpace(value) == "" {
			return "", badRequest(fmt.Sprintf("secret %s is not configured", secret.Name))
		}
		return value, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	if !allowEnvFallback {
		return "", badRequest(fmt.Sprintf("secret ref %s was not found", normalizedRef))
	}

	value := os.Getenv(normalizedRef)
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	return "", badRequest(fmt.Sprintf("secret ref %s is not configured", normalizedRef))
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

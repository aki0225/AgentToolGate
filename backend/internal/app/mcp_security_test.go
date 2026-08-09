package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
)

func TestMCPSecretBearingConnectorRequiresDeploymentHostCeiling(t *testing.T) {
	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "credentialed",
		DisplayName: "Credentialed MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
			"headerSecretRefs": map[string]string{
				"Authorization": "missing_secret",
			},
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	resp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "MCP_ALLOWED_HOSTS") {
		t.Fatalf("credentialed MCP without deployment ceiling must fail closed, got %d body=%s", resp.Code, resp.Body.String())
	}
	if len(mockServer.methodsSnapshot()) != 0 {
		t.Fatalf("rejected connector must not reach MCP server")
	}
}

func TestMCPConnectorCannotWidenDeploymentHostCeiling(t *testing.T) {
	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{
		MCPAllowedHosts: []string{"example.invalid:443"},
	})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "outside",
		DisplayName: "Outside MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	resp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "not allowed") {
		t.Fatalf("MCP connector must not widen deployment host ceiling, got %d body=%s", resp.Code, resp.Body.String())
	}
	if len(mockServer.methodsSnapshot()) != 0 {
		t.Fatalf("blocked host must not reach MCP server")
	}
}

func TestMCPConnectorRejectsLiteralAuthorizationHeader(t *testing.T) {
	err := validateMCPConnectorHeaders(map[string]string{
		"Authorization": "Bearer synthetic",
	})
	if err == nil {
		t.Fatalf("literal Authorization header must be rejected")
	}
	if err := validateMCPConnectorHeaderSecretRefs(map[string]string{
		"Authorization": "workspace_secret",
	}); err != nil {
		t.Fatalf("Authorization secret ref should remain supported: %v", err)
	}
}

func TestWorkspaceSecretCannotResolveProtectedRuntimeVariable(t *testing.T) {
	const protectedValue = "synthetic-reviewer-token-value"
	t.Setenv("AGT_LOCAL_REVIEWER_TOKEN", protectedValue)

	srv, st, workspace := newMCPAppTestServer(t, config.Config{})
	if _, err := st.CreateSecret(context.Background(), model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "reviewer_token",
		Description:    "must stay in the control plane",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "AGT_LOCAL_REVIEWER_TOKEN",
		Metadata:       json.RawMessage(`{"scope":"test"}`),
	}); err != nil {
		t.Fatalf("create protected Secret fixture: %v", err)
	}

	if value, err := srv.resolveSecretRefValue(context.Background(), workspace.ID, "reviewer_token"); err == nil || value != "" {
		t.Fatalf("protected runtime variable must not resolve, got value=%q err=%v", value, err)
	}
}

func TestResolvedSecretRedactionPreservesCommonSubstrings(t *testing.T) {
	input := map[string]any{
		"status":       "passed",
		"allow":        "token stays intact",
		"contest":      "book looks normal",
		"a":            "a",
		"ok":           "status ok",
		"test":         "prefix-test suffix",
		"testStatus":   "contest",
		"nested_value": "a ok test",
	}

	redacted, ok := redactResolvedSecretValues(input, []string{"a", "ok", "test"}).(map[string]any)
	if !ok {
		t.Fatalf("expected redacted map")
	}
	for _, key := range []string{"status", "allow", "contest", "testStatus", "nested_value"} {
		if _, exists := redacted[key]; !exists {
			t.Fatalf("common key %q was corrupted: %+v", key, redacted)
		}
	}
	if redacted["status"] != "passed" || redacted["allow"] != "token stays intact" || redacted["contest"] != "book looks normal" {
		t.Fatalf("common substrings were corrupted: %+v", redacted)
	}
	if !strings.Contains(redacted["nested_value"].(string), "[REDACTED]") {
		t.Fatalf("standalone secret tokens must still be redacted: %+v", redacted)
	}
}

func TestMCPOutboundSyncRedactsReflectedSecretMetadata(t *testing.T) {
	const (
		secretRef   = "mcp_sync_secret"
		secretEnv   = "MCP_SYNC_SECRET_ENV"
		secretValue = "sync-secret-12345"
	)
	t.Setenv(secretEnv, secretValue)

	mockServer := newMockOutboundMCPServer(t)
	mockServer.mu.Lock()
	mockServer.tools = []map[string]any{
		{
			"name":        "get_reflected",
			"title":       "Title " + secretValue,
			"description": "Description " + secretValue,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					secretValue: map[string]any{"description": secretValue},
				},
			},
			"annotations": map[string]any{"readOnlyHint": true},
		},
	}
	mockServer.mu.Unlock()

	srv, st, workspace := newMCPAppTestServer(t, config.Config{
		MCPAllowedHosts: []string{mustURLHost(t, mockServer.URL)},
	})
	if _, err := st.CreateSecret(context.Background(), model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           secretRef,
		Description:    "sync reflection test",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       secretEnv,
		Metadata:       json.RawMessage(`{"scope":"mcp"}`),
	}); err != nil {
		t.Fatalf("create Secret: %v", err)
	}
	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "reflected_sync",
		DisplayName: "Reflected Sync MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
			"headerSecretRefs": map[string]string{
				"Authorization": secretRef,
			},
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	resp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("sync connector: %d body=%s", resp.Code, resp.Body.String())
	}
	tool, err := st.GetToolByKey(context.Background(), workspace.ID, "mcp_reflected_sync.get_reflected")
	if err != nil {
		t.Fatalf("get synced tool: %v", err)
	}
	persisted := strings.Join([]string{tool.DisplayName, tool.Description, string(tool.InputSchemaJSON)}, "\n")
	if strings.Contains(persisted, secretValue) || !strings.Contains(persisted, "[REDACTED]") {
		t.Fatalf("synced MCP metadata must be redacted, got %s", persisted)
	}
}

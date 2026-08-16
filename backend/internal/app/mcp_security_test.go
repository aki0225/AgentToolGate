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

func TestMCPConnectorRequiresDeploymentHostCeiling(t *testing.T) {
	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "uncredentialed",
		DisplayName: "Uncredentialed MCP",
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
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "MCP_ALLOWED_HOSTS") {
		t.Fatalf("MCP without deployment ceiling must fail closed, got %d body=%s", resp.Code, resp.Body.String())
	}
	if len(mockServer.methodsSnapshot()) != 0 {
		t.Fatalf("rejected connector must not reach MCP server")
	}
}

func TestMCPSecretBearingConnectorRequiresHTTPSExceptExplicitLoopback(t *testing.T) {
	t.Parallel()

	t.Run("remote http is rejected", func(t *testing.T) {
		app := &App{cfg: config.Config{
			MCPAllowedHosts: []string{"mcp.example.com:80"},
		}}
		err := app.validateMCPConnectorRuntimeTarget(mcpConnectorConfig{
			URL: "http://mcp.example.com:80/sse",
			HeaderSecretRefs: map[string]string{
				"Authorization": "workspace_secret",
			},
		})
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("secret-bearing remote MCP must require HTTPS, got %v", err)
		}
	})

	t.Run("remote https is accepted", func(t *testing.T) {
		app := &App{cfg: config.Config{
			MCPAllowedHosts: []string{"mcp.example.com:443"},
		}}
		err := app.validateMCPConnectorRuntimeTarget(mcpConnectorConfig{
			URL: "https://mcp.example.com:443/sse",
			HeaderSecretRefs: map[string]string{
				"Authorization": "workspace_secret",
			},
		})
		if err != nil {
			t.Fatalf("allowlisted HTTPS MCP should be accepted: %v", err)
		}
	})

	for _, testCase := range []struct {
		name        string
		url         string
		allowedHost string
	}{
		{name: "localhost", url: "http://localhost:18081/sse", allowedHost: "localhost:18081"},
		{name: "ipv4 loopback", url: "http://127.0.0.1:18081/sse", allowedHost: "127.0.0.1:18081"},
		{name: "ipv6 loopback", url: "http://[::1]:18081/sse", allowedHost: "[::1]:18081"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			app := &App{cfg: config.Config{
				MCPAllowedHosts: []string{testCase.allowedHost},
			}}
			err := app.validateMCPConnectorRuntimeTarget(mcpConnectorConfig{
				URL: testCase.url,
				HeaderSecretRefs: map[string]string{
					"Authorization": "workspace_secret",
				},
			})
			if err != nil {
				t.Fatalf("explicitly allowlisted loopback MCP should permit HTTP: %v", err)
			}
		})
	}

	t.Run("other ipv4 loopback is rejected", func(t *testing.T) {
		app := &App{cfg: config.Config{
			MCPAllowedHosts: []string{"127.0.0.2:18081"},
		}}
		err := app.validateMCPConnectorRuntimeTarget(mcpConnectorConfig{
			URL: "http://127.0.0.2:18081/sse",
			HeaderSecretRefs: map[string]string{
				"Authorization": "workspace_secret",
			},
		})
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("HTTP secret exception must be limited to 127.0.0.1, got %v", err)
		}
	})
}

func TestMCPConnectorRejectsMetadataAndLinkLocalEvenWhenAllowlisted(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://169.254.1.10/mcp/sse",
	} {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			app := &App{cfg: config.Config{
				MCPAllowedHosts: []string{mustURLHost(t, rawURL)},
			}}
			if err := app.validateMCPConnectorRuntimeTarget(mcpConnectorConfig{URL: rawURL}); err == nil ||
				!strings.Contains(err.Error(), "metadata and link-local") {
				t.Fatalf("metadata/link-local MCP target must fail closed, got %v", err)
			}
		})
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

func TestResolvedSecretRedactionDoesNotLeakShortValues(t *testing.T) {
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
	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal redacted value: %v", err)
	}
	for _, secret := range []string{"a", "ok", "test"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("short resolved Secret %q leaked from output: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %s", raw)
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

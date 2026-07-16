package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestConnectorCRUDSeedsDefaultsAndRedactsConfig(t *testing.T) {
	t.Parallel()

	srv, _, _ := newConnectorTestApp(t)

	listResp := getJSON(t, srv, "/api/connectors")
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", listResp.Code, listResp.Body.String())
	}
	var list struct {
		Items []model.Connector `json:"items"`
	}
	decodeBody(t, listResp.Body.Bytes(), &list)
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 seeded connectors, got %d", len(list.Items))
	}
	for _, connector := range list.Items {
		if strings.Contains(strings.ToLower(string(connector.ConfigJSON)), "secret") {
			t.Fatalf("listed connector config must be response-redacted: %s", connector.ConfigJSON)
		}
	}

	createResp := postJSON(t, srv, "/api/connectors", `{
		"type":"mcp",
		"name":"weather",
		"displayName":"Weather MCP",
		"configJson":{
			"transport":"sse",
			"url":"https://example.com/sse",
			"headers":{
				"Authorization":"Bearer secret-token",
				"x_api_key":"secret-key"
			}
		},
		"enabled":true
	}`)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createResp.Code, createResp.Body.String())
	}

	var created model.Connector
	decodeBody(t, createResp.Body.Bytes(), &created)
	if created.Type != "mcp" || created.Name != "weather" {
		t.Fatalf("unexpected connector response: %+v", created)
	}
	assertConnectorConfigRedacted(t, created.ConfigJSON)

	patchResp := patchJSON(t, srv, "/api/connectors/"+created.ID, `{
		"displayName":"Weather MCP v2",
		"configJson":{
			"transport":"sse",
			"url":"https://example.com/updated",
			"headers":{
				"token":"secret-two"
			}
		},
		"enabled":false
	}`)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", patchResp.Code, patchResp.Body.String())
	}

	var updated model.Connector
	decodeBody(t, patchResp.Body.Bytes(), &updated)
	if updated.DisplayName != "Weather MCP v2" || updated.Enabled {
		t.Fatalf("unexpected updated connector: %+v", updated)
	}
	assertConnectorConfigRedacted(t, updated.ConfigJSON)

	getResp := getJSON(t, srv, "/api/connectors/"+created.ID)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", getResp.Code, getResp.Body.String())
	}
	var fetched model.Connector
	decodeBody(t, getResp.Body.Bytes(), &fetched)
	assertConnectorConfigRedacted(t, fetched.ConfigJSON)

	stored, err := srv.store.GetConnectorByID(context.Background(), created.WorkspaceID, created.ID)
	if err != nil {
		t.Fatalf("get stored connector: %v", err)
	}
	if strings.Contains(string(stored.ConfigJSON), "secret-token") || strings.Contains(string(stored.ConfigJSON), "secret-two") {
		t.Fatalf("stored connector config must remain a redacted snapshot, got %s", stored.ConfigJSON)
	}
}

func newConnectorTestApp(t *testing.T) (*App, store.Store, model.Workspace) {
	t.Helper()

	st := store.NewMemoryStore()
	cfg := config.Config{
		AuthMode:                "local",
		DefaultWorkspaceOrgID:   "local-org",
		LocalSubject:            "local-dev",
		LocalEmail:              "dev@agenttoolgate.local",
		LocalName:               "Local Developer",
		LocalRole:               "owner",
		RateLimitPerMinute:      60,
		DatabaseQueryDatasource: "local_postgres",
		GitHubAPIBaseURL:        "https://api.github.com",
		GitHubAllowedRepos:      []string{"acme/demo"},
		HTTPAllowedHosts:        []string{"localhost:18080"},
		HTTPAllowedMethods:      []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"},
		CORSAllowedOrigins:      []string{"*"},
	}
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
		Connectors:              DefaultBootstrapConnectors(cfg),
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) == 0 {
		t.Fatalf("expected bootstrap workspace")
	}

	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, st, workspaces[0]
}

func patchJSON(t *testing.T, srv *App, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func assertConnectorConfigRedacted(t *testing.T, raw json.RawMessage) {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	headers, _ := decoded["headers"].(map[string]any)
	if headers == nil {
		t.Fatalf("expected headers in connector config: %s", raw)
	}
	foundSensitive := false
	for key, value := range headers {
		normalized := strings.ToLower(key)
		if strings.Contains(normalized, "token") || strings.Contains(normalized, "authorization") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "cookie") {
			foundSensitive = true
			if value != "[REDACTED]" {
				t.Fatalf("expected sensitive connector config key %s to be redacted: %s", key, raw)
			}
		}
	}
	if !foundSensitive {
		t.Fatalf("expected at least one sensitive header field to be present for redaction test: %s", raw)
	}
}

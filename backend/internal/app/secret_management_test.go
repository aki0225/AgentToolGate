package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/google/uuid"
)

func TestSecretsAPICRUDAndBindingSummary(t *testing.T) {
	srv, _, _ := newGovernanceTestApp(t)
	const secretValue = "top-secret-token-123"
	t.Setenv("GITHUB_TOKEN_ENV", secretValue)

	createResp := postJSON(t, srv, "/api/secrets", `{
		"name":"github_token",
		"description":"GitHub token",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":"GITHUB_TOKEN_ENV",
		"metadata":{"scope":"github"}
	}`)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createResp.Code, createResp.Body.String())
	}
	if strings.Contains(createResp.Body.String(), secretValue) {
		t.Fatalf("secret create response leaked value: %s", createResp.Body.String())
	}

	var created model.Secret
	decodeBody(t, createResp.Body.Bytes(), &created)
	if created.Name != "github_token" || !created.Enabled || created.ValueRef != "GITHUB_TOKEN_ENV" {
		t.Fatalf("unexpected created secret: %+v", created)
	}

	connectorResp := postJSON(t, srv, "/api/connectors", `{
		"type":"github",
		"name":"default",
		"displayName":"GitHub Default",
		"configJson":{
			"apiBaseURL":"https://api.github.invalid",
			"allowedRepos":["acme/demo"],
			"tokenSecretRef":"github_token"
		},
		"enabled":true
	}`)
	if connectorResp.Code != http.StatusCreated {
		t.Fatalf("create connector failed: %d body=%s", connectorResp.Code, connectorResp.Body.String())
	}

	detailResp := getJSON(t, srv, "/api/secrets/"+created.ID)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("get secret failed: %d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detailed model.Secret
	decodeBody(t, detailResp.Body.Bytes(), &detailed)
	if len(detailed.Bindings) == 0 {
		t.Fatalf("expected secret binding summary, got %+v", detailed)
	}
	if detailed.Bindings[0].Target != "github.default" || detailed.Bindings[0].Field != "tokenSecretRef" {
		t.Fatalf("unexpected binding summary: %+v", detailed.Bindings)
	}

	updateResp := putSecretJSON(t, srv, "/api/secrets/"+created.ID, `{
		"name":"github_token",
		"description":"Updated GitHub token",
		"enabled":false,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":"GITHUB_TOKEN_ENV",
		"metadata":{"scope":"github","updated":true}
	}`)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update secret failed: %d body=%s", updateResp.Code, updateResp.Body.String())
	}
	var updated model.Secret
	decodeBody(t, updateResp.Body.Bytes(), &updated)
	if updated.Enabled {
		t.Fatalf("expected secret to be disabled after update, got %+v", updated)
	}

	listResp := getJSON(t, srv, "/api/secrets")
	if listResp.Code != http.StatusOK {
		t.Fatalf("list secrets failed: %d body=%s", listResp.Code, listResp.Body.String())
	}
	var list struct {
		Items []model.Secret `json:"items"`
	}
	decodeBody(t, listResp.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("unexpected secret list: %+v", list.Items)
	}

	usageResp := getJSON(t, srv, "/api/secrets/"+created.ID+"/usage")
	if usageResp.Code != http.StatusOK {
		t.Fatalf("get secret usage failed: %d body=%s", usageResp.Code, usageResp.Body.String())
	}
	var usage model.SecretUsageResponse
	decodeBody(t, usageResp.Body.Bytes(), &usage)
	if usage.CanDelete || len(usage.Usages) != 1 || usage.Usages[0].ConnectorType != "github" {
		t.Fatalf("unexpected secret usage response: %+v", usage)
	}
	if strings.Contains(usageResp.Body.String(), secretValue) {
		t.Fatalf("usage response leaked secret value: %s", usageResp.Body.String())
	}

	blockedDeleteResp := deleteSecretJSON(t, srv, "/api/secrets/"+created.ID)
	if blockedDeleteResp.Code != http.StatusConflict {
		t.Fatalf("bound secret delete should be blocked, got %d body=%s", blockedDeleteResp.Code, blockedDeleteResp.Body.String())
	}

	deleteResp := deleteSecretJSON(t, srv, "/api/secrets/"+created.ID+"?force=true")
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("force delete secret failed: %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}

	if resp := getJSON(t, srv, "/api/secrets/"+created.ID); resp.Code != http.StatusNotFound {
		t.Fatalf("expected deleted secret to return 404, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestSecretUsageAPIReturnsConnectorReferences(t *testing.T) {
	srv, _, _ := newGovernanceTestApp(t)
	const secretValue = "usage-secret-value"
	t.Setenv("GITHUB_USAGE_TOKEN_ENV", secretValue)
	t.Setenv("HTTP_USAGE_KEY_ENV", secretValue)
	t.Setenv("MCP_USAGE_AUTH_ENV", secretValue)

	created := map[string]model.Secret{}
	for _, item := range []struct {
		name       string
		secretType string
		valueRef   string
	}{
		{name: "github_usage_token", secretType: "token", valueRef: "GITHUB_USAGE_TOKEN_ENV"},
		{name: "http_usage_key", secretType: "api_key", valueRef: "HTTP_USAGE_KEY_ENV"},
		{name: "mcp_usage_auth", secretType: "token", valueRef: "MCP_USAGE_AUTH_ENV"},
	} {
		resp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
			"name":%q,
			"description":"usage test",
			"enabled":true,
			"secretType":%q,
			"valueSource":"env",
			"valueRef":%q,
			"metadata":{}
		}`, item.name, item.secretType, item.valueRef))
		if resp.Code != http.StatusCreated {
			t.Fatalf("create secret %s failed: %d body=%s", item.name, resp.Code, resp.Body.String())
		}
		var secret model.Secret
		decodeBody(t, resp.Body.Bytes(), &secret)
		created[item.name] = secret
	}

	createConnector(t, srv, `{
		"type":"github",
		"name":"usage_github",
		"displayName":"GitHub Usage",
		"configJson":{
			"apiBaseURL":"https://api.github.invalid",
			"allowedRepos":["acme/demo"],
			"tokenSecretRef":"github_usage_token"
		},
		"enabled":true
	}`)
	createConnector(t, srv, `{
		"type":"http",
		"name":"usage_http",
		"displayName":"HTTP Usage",
		"configJson":{
			"allowedHosts":["localhost:18080"],
			"headerSecretRefs":{"X-Api-Key":"http_usage_key"}
		},
		"enabled":true
	}`)
	createConnector(t, srv, `{
		"type":"mcp",
		"name":"usage_mcp",
		"displayName":"MCP Usage",
		"configJson":{
			"transport":"sse",
			"url":"http://localhost:8081/mcp/sse",
			"headerSecretRefs":{"Authorization":"mcp_usage_auth"}
		},
		"enabled":true
	}`)

	assertSecretUsage(t, srv, created["github_usage_token"].ID, "github", "tokenSecretRef", secretValue)
	assertSecretUsage(t, srv, created["http_usage_key"].ID, "http", "headerSecretRefs.X-Api-Key", secretValue)
	assertSecretUsage(t, srv, created["mcp_usage_auth"].ID, "mcp", "headerSecretRefs.Authorization", secretValue)
}

func TestSecretUsageAPIIsWorkspaceScoped(t *testing.T) {
	srv, st, workspace := newGovernanceTestApp(t)
	secretResp := postJSON(t, srv, "/api/secrets", `{
		"name":"shared_token",
		"description":"default workspace",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":"SHARED_TOKEN_ENV",
		"metadata":{}
	}`)
	if secretResp.Code != http.StatusCreated {
		t.Fatalf("create default secret failed: %d body=%s", secretResp.Code, secretResp.Body.String())
	}
	var defaultSecret model.Secret
	decodeBody(t, secretResp.Body.Bytes(), &defaultSecret)

	other, err := st.CreateWorkspace(context.Background(), model.CreateWorkspaceInput{
		Name:                  "Other Workspace",
		Slug:                  "other-secret-usage",
		ZitadelOrganizationID: "other-secret-usage-org",
	})
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	otherSecretResp := postJSONWithWorkspace(t, srv, other.ZitadelOrganizationID, "/api/secrets", `{
		"name":"shared_token",
		"description":"other workspace",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":"SHARED_TOKEN_ENV",
		"metadata":{}
	}`)
	if otherSecretResp.Code != http.StatusCreated {
		t.Fatalf("create other secret failed: %d body=%s", otherSecretResp.Code, otherSecretResp.Body.String())
	}

	otherConnectorResp := postJSONWithWorkspace(t, srv, other.ZitadelOrganizationID, "/api/connectors", `{
		"type":"github",
		"name":"other_github",
		"displayName":"Other GitHub",
		"configJson":{"tokenSecretRef":"shared_token"},
		"enabled":true
	}`)
	if otherConnectorResp.Code != http.StatusCreated {
		t.Fatalf("create other connector failed: %d body=%s", otherConnectorResp.Code, otherConnectorResp.Body.String())
	}

	usageResp := getJSON(t, srv, "/api/secrets/"+defaultSecret.ID+"/usage")
	if usageResp.Code != http.StatusOK {
		t.Fatalf("get default usage failed: %d body=%s", usageResp.Code, usageResp.Body.String())
	}
	var usage model.SecretUsageResponse
	decodeBody(t, usageResp.Body.Bytes(), &usage)
	if !usage.CanDelete || len(usage.Usages) != 0 {
		t.Fatalf("cross-workspace connector must not block default workspace secret, workspace=%s usage=%+v", workspace.ID, usage)
	}
}

func TestForceDeletedSecretKeepsGitHubFailClosed(t *testing.T) {
	const secretRef = "force_deleted_github_token"
	const secretEnv = "FORCE_DELETED_GITHUB_TOKEN_ENV"
	const secretValue = "ghp_force_delete_secret_value"
	t.Setenv(secretEnv, secretValue)

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockGitHub.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		noToken:      true,
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	createSecretResp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
		"name":%q,
		"description":"GitHub token",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{}
	}`, secretRef, secretEnv))
	if createSecretResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createSecretResp.Code, createSecretResp.Body.String())
	}
	var secret model.Secret
	decodeBody(t, createSecretResp.Body.Bytes(), &secret)

	createConnector(t, srv, fmt.Sprintf(`{
		"type":"github",
		"name":"default",
		"displayName":"GitHub Default",
		"configJson":{
			"apiBaseURL":%q,
			"allowedRepos":["acme/demo"],
			"tokenSecretRef":%q
		},
		"enabled":true
	}`, mockGitHub.URL, secretRef))

	deleteResp := deleteSecretJSON(t, srv, "/api/secrets/"+secret.ID+"?force=true")
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("force delete secret failed: %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected force-deleted secret to fail closed, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("force-deleted secret must not call GitHub upstream, got %d", got)
	}
	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 || calls[0].Status != "failed" || !strings.Contains(calls[0].ErrorMessage, "was not found") {
		t.Fatalf("expected failed audit for deleted secret, got %+v", calls)
	}
	if strings.Contains(calls[0].ErrorMessage, secretValue) || strings.Contains(string(calls[0].InputRedactedJSON), secretValue) {
		t.Fatalf("force-deleted secret value leaked into audit: %+v", calls[0])
	}
}

func TestPostgresSecretUsageAndDeleteGuard(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	st, err := store.NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	suffix := uuid.NewString()
	orgID := "org-secret-usage-" + suffix
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Secret Usage Postgres " + orgID,
		Slug:                  "secret-usage-" + suffix,
		ZitadelOrganizationID: orgID,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: workspace.ZitadelOrganizationID,
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(ctx, cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	secretResp := postJSON(t, srv, "/api/secrets", `{
		"name":"pg_usage_token",
		"description":"postgres usage",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":"PG_USAGE_TOKEN_ENV",
		"metadata":{}
	}`)
	if secretResp.Code != http.StatusCreated {
		t.Fatalf("create postgres secret failed: %d body=%s", secretResp.Code, secretResp.Body.String())
	}
	var secret model.Secret
	decodeBody(t, secretResp.Body.Bytes(), &secret)
	createConnector(t, srv, `{
		"type":"github",
		"name":"pg_usage_github",
		"displayName":"PG Usage GitHub",
		"configJson":{"tokenSecretRef":"pg_usage_token"},
		"enabled":true
	}`)

	usageResp := getJSON(t, srv, "/api/secrets/"+secret.ID+"/usage")
	if usageResp.Code != http.StatusOK {
		t.Fatalf("get postgres secret usage failed: %d body=%s", usageResp.Code, usageResp.Body.String())
	}
	var usage model.SecretUsageResponse
	decodeBody(t, usageResp.Body.Bytes(), &usage)
	if usage.CanDelete || len(usage.Usages) != 1 {
		t.Fatalf("expected postgres usage to block delete, got %+v", usage)
	}
	if blocked := deleteSecretJSON(t, srv, "/api/secrets/"+secret.ID); blocked.Code != http.StatusConflict {
		t.Fatalf("postgres bound secret delete should be 409, got %d body=%s", blocked.Code, blocked.Body.String())
	}
	if forced := deleteSecretJSON(t, srv, "/api/secrets/"+secret.ID+"?force=true"); forced.Code != http.StatusOK {
		t.Fatalf("postgres force delete should succeed, got %d body=%s", forced.Code, forced.Body.String())
	}
}

func TestSecretStoreBackedGitHubTokenInjectionAndFailClosed(t *testing.T) {
	const secretRef = "github_token"
	const secretEnv = "GITHUB_TOKEN_ENV_SECRET"
	const secretValue = "ghp_secret_store_token"
	t.Setenv(secretEnv, secretValue)

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Header.Get("Authorization") != "Bearer "+secretValue {
			t.Fatalf("missing resolved token header")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number":   42,
			"title":    "Improve gateway",
			"state":    "open",
			"html_url": "https://github.example/acme/demo/pull/42",
			"user":     map[string]any{"login": "alice"},
			"head":     map[string]any{"ref": "feature/github"},
			"base":     map[string]any{"ref": "main"},
		})
	}))
	t.Cleanup(mockGitHub.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		noToken:      true,
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	createSecretResp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
		"name":%q,
		"description":"GitHub token",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"github"}
	}`, secretRef, secretEnv))
	if createSecretResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createSecretResp.Code, createSecretResp.Body.String())
	}

	createConnectorResp := postJSON(t, srv, "/api/connectors", fmt.Sprintf(`{
		"type":"github",
		"name":"default",
		"displayName":"GitHub Default",
		"configJson":{
			"apiBaseURL":%q,
			"allowedRepos":["acme/demo"],
			"tokenSecretRef":%q
		},
		"enabled":true
	}`, mockGitHub.URL, secretRef))
	if createConnectorResp.Code != http.StatusCreated {
		t.Fatalf("create connector failed: %d body=%s", createConnectorResp.Code, createConnectorResp.Body.String())
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":42}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("github call failed: %d body=%s", callResp.Code, callResp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("expected one GitHub request, got %d", got)
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("unexpected response: %+v", response)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if strings.Contains(string(calls[0].InputRedactedJSON), secretValue) ||
		strings.Contains(string(calls[0].OutputRedactedJSON), secretValue) ||
		strings.Contains(calls[0].ErrorMessage, secretValue) {
		t.Fatalf("secret leaked into audit: %+v", calls[0])
	}

	listResp := getJSON(t, srv, "/api/secrets")
	if listResp.Code != http.StatusOK {
		t.Fatalf("list secrets failed: %d body=%s", listResp.Code, listResp.Body.String())
	}

	var listed struct {
		Items []model.Secret `json:"items"`
	}
	decodeBody(t, listResp.Body.Bytes(), &listed)
	if len(listed.Items) != 1 || len(listed.Items[0].Bindings) == 0 {
		t.Fatalf("expected secret binding summary, got %+v", listed.Items)
	}

	updateResp := putSecretJSON(t, srv, "/api/secrets/"+listed.Items[0].ID, fmt.Sprintf(`{
		"name":%q,
		"description":"GitHub token",
		"enabled":false,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"github"}
	}`, secretRef, secretEnv))
	if updateResp.Code != http.StatusOK {
		t.Fatalf("disable secret failed: %d body=%s", updateResp.Code, updateResp.Body.String())
	}

	failedResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":42}}`)
	if failedResp.Code != http.StatusBadRequest {
		t.Fatalf("expected secret-disabled call to fail closed, got %d body=%s", failedResp.Code, failedResp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("disabled secret must not trigger another GitHub request, got %d", got)
	}
}

func TestGitHubSecretConnectorAllowedReposDriveRuntimeValidation(t *testing.T) {
	const secretRef = "github_connector_token"
	const secretEnv = "GITHUB_CONNECTOR_TOKEN_ENV"
	const secretValue = "ghp_connector_repo_token"
	t.Setenv(secretEnv, secretValue)

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.URL.Path != "/repos/octo/tools/pulls/7" {
			t.Fatalf("unexpected GitHub path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+secretValue {
			t.Fatalf("missing resolved token header")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"number":   7,
			"title":    "Connector allowlist",
			"state":    "open",
			"html_url": "https://github.example/octo/tools/pull/7",
			"user":     map[string]any{"login": "alice"},
			"head":     map[string]any{"ref": "feature"},
			"base":     map[string]any{"ref": "main"},
		})
	}))
	t.Cleanup(mockGitHub.Close)

	// 全局 allowlist 故意不包含 octo/tools；本测试钉住 connector config 的
	// allowedRepos 必须同时驱动 list_repos 和实际 PR/Issue 调用校验。
	srv, _, _ := newGitHubTestApp(t, githubTestConfig{
		noToken:      true,
		apiBaseURL:   "https://api.github.invalid",
		allowedRepos: []string{"acme/demo"},
	})

	createSecretResp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
		"name":%q,
		"description":"GitHub connector token",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"github"}
	}`, secretRef, secretEnv))
	if createSecretResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createSecretResp.Code, createSecretResp.Body.String())
	}

	createConnectorResp := postJSON(t, srv, "/api/connectors", fmt.Sprintf(`{
		"type":"github",
		"name":"default",
		"displayName":"GitHub Default",
		"configJson":{
			"apiBaseURL":%q,
			"allowedRepos":["octo/tools"],
			"tokenSecretRef":%q
		},
		"enabled":true
	}`, mockGitHub.URL, secretRef))
	if createConnectorResp.Code != http.StatusCreated && createConnectorResp.Code != http.StatusConflict {
		t.Fatalf("create github connector failed: %d body=%s", createConnectorResp.Code, createConnectorResp.Body.String())
	}

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"octo","repo":"tools","pullNumber":7}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("github call should use connector allowedRepos, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("expected one GitHub request, got %d", got)
	}

	denied := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("global-only repo must be rejected by connector allowlist, got %d body=%s", denied.Code, denied.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("rejected repo must not call GitHub, got %d requests", got)
	}
}

func TestGitHubTokenSecretRefRequiresWorkspaceSecretDespiteEnvFallback(t *testing.T) {
	const secretRef = "github_missing_workspace_secret"
	t.Setenv(secretRef, "ghp_env_fallback_must_not_be_used")

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockGitHub.Close)

	srv, _, _ := newGitHubTestApp(t, githubTestConfig{
		noToken:      true,
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	createConnectorResp := postJSON(t, srv, "/api/connectors", fmt.Sprintf(`{
		"type":"github",
		"name":"default",
		"displayName":"GitHub Default",
		"configJson":{
			"apiBaseURL":%q,
			"allowedRepos":["acme/demo"],
			"tokenSecretRef":%q
		},
		"enabled":true
	}`, mockGitHub.URL, secretRef))
	if createConnectorResp.Code != http.StatusCreated && createConnectorResp.Code != http.StatusConflict {
		t.Fatalf("create github connector failed: %d body=%s", createConnectorResp.Code, createConnectorResp.Body.String())
	}

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected missing workspace Secret to fail closed, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("missing workspace Secret must not call GitHub, got %d", got)
	}
}

func TestSecretStoreBackedHTTPRequestHeaderRefInjectionAndFailClosed(t *testing.T) {
	const secretRef = "http_api_key"
	const secretEnv = "HTTP_API_KEY_ENV"
	const secretValue = "request-api-key-secret"
	t.Setenv(secretEnv, secretValue)

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Header.Get("X-Api-Key") != secretValue {
			t.Fatalf("expected resolved X-Api-Key header, got %q", r.Header.Get("X-Api-Key"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": secretValue})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	createSecretResp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
		"name":%q,
		"description":"HTTP API key",
		"enabled":true,
		"secretType":"api_key",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"http"}
	}`, secretRef, secretEnv))
	if createSecretResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createSecretResp.Code, createSecretResp.Body.String())
	}

	body := fmt.Sprintf(`{"tool":"http.request","arguments":{"method":"GET","url":%q,"headers":{"X-Demo":"hello"},"headerSecretRefs":{"X-Api-Key":%q}}}`, mockHTTP.URL+"/status", secretRef)
	resp := postJSON(t, srv, "/api/tool-calls", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("http call failed: %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("expected one HTTP request, got %d", got)
	}

	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("unexpected response: %+v", response)
	}

	calls := listHTTPTestCalls(t, st, workspace.ID)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if strings.Contains(string(calls[0].InputRedactedJSON), secretValue) || strings.Contains(string(calls[0].OutputRedactedJSON), secretValue) {
		t.Fatalf("secret leaked into HTTP audit: %+v", calls[0])
	}

	secretList := getSecretList(t, srv)
	if len(secretList) != 1 {
		t.Fatalf("expected one secret, got %+v", secretList)
	}
	updateResp := putSecretJSON(t, srv, "/api/secrets/"+secretList[0].ID, fmt.Sprintf(`{
		"name":%q,
		"description":"HTTP API key",
		"enabled":false,
		"secretType":"api_key",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"http"}
	}`, secretRef, secretEnv))
	if updateResp.Code != http.StatusOK {
		t.Fatalf("disable secret failed: %d body=%s", updateResp.Code, updateResp.Body.String())
	}

	failed := postJSON(t, srv, "/api/tool-calls", body)
	if failed.Code != http.StatusBadRequest {
		t.Fatalf("expected disabled secret to fail closed, got %d body=%s", failed.Code, failed.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("disabled secret must not trigger another HTTP request, got %d", got)
	}
}

func TestHTTPHeaderSecretRefRequiresWorkspaceSecretDespiteEnvFallback(t *testing.T) {
	const secretRef = "HTTP_MISSING_WORKSPACE_SECRET"
	t.Setenv(secretRef, "env-fallback-must-not-be-used")

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	srv, _, _ := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	body := fmt.Sprintf(`{"tool":"http.request","arguments":{"method":"GET","url":%q,"headerSecretRefs":{"Authorization":%q}}}`, mockHTTP.URL+"/status", secretRef)
	resp := postJSON(t, srv, "/api/tool-calls", body)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected missing workspace Secret to fail closed, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("missing workspace Secret must not call HTTP upstream, got %d", got)
	}
}

func TestSecretStoreBackedMCPHeaderRefInjectionAndFailClosed(t *testing.T) {
	const secretRef = "mcp_auth_token"
	const secretEnv = "MCP_AUTH_TOKEN_ENV"
	const secretValue = "Bearer mcp-secret-token-12345"
	t.Setenv(secretEnv, secretValue)

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	createSecretResp := postJSON(t, srv, "/api/secrets", fmt.Sprintf(`{
		"name":%q,
		"description":"MCP auth token",
		"enabled":true,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"mcp"}
	}`, secretRef, secretEnv))
	if createSecretResp.Code != http.StatusCreated {
		t.Fatalf("create secret failed: %d body=%s", createSecretResp.Code, createSecretResp.Body.String())
	}

	connectorResp := postJSON(t, srv, "/api/connectors", fmt.Sprintf(`{
		"type":"mcp",
		"name":"weather_secret",
		"displayName":"Weather Secret MCP",
		"configJson":{
			"transport":"sse",
			"url":%q,
			"headers":{"X-Demo":"hello"},
			"headerSecretRefs":{"Authorization":%q}
		},
		"enabled":true
	}`, mockServer.URL+"/mcp/sse", secretRef))
	if connectorResp.Code != http.StatusCreated {
		t.Fatalf("create connector failed: %d body=%s", connectorResp.Code, connectorResp.Body.String())
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+mustConnectorID(t, connectorResp.Body.Bytes())+"/sync", `{}`)
	if syncResp.Code != http.StatusOK {
		t.Fatalf("sync failed: %d body=%s", syncResp.Code, syncResp.Body.String())
	}
	if strings.Contains(syncResp.Body.String(), secretValue) {
		t.Fatalf("sync response leaked resolved secret: %s", syncResp.Body.String())
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather_secret.get_forecast","arguments":{"city":"Shanghai"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("mcp call failed: %d body=%s", callResp.Code, callResp.Body.String())
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 1 {
		t.Fatalf("expected one MCP request, got %d", got)
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("unexpected response: %+v", response)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected persisted tool call")
	}
	if strings.Contains(string(calls[0].InputRedactedJSON), secretValue) || strings.Contains(string(calls[0].OutputRedactedJSON), secretValue) {
		t.Fatalf("secret leaked into MCP audit: %+v", calls[0])
	}

	secretList := getSecretList(t, srv)
	if len(secretList) != 1 {
		t.Fatalf("expected one secret, got %+v", secretList)
	}
	updateResp := putSecretJSON(t, srv, "/api/secrets/"+secretList[0].ID, fmt.Sprintf(`{
		"name":%q,
		"description":"MCP auth token",
		"enabled":false,
		"secretType":"token",
		"valueSource":"env",
		"valueRef":%q,
		"metadata":{"scope":"mcp"}
	}`, secretRef, secretEnv))
	if updateResp.Code != http.StatusOK {
		t.Fatalf("disable secret failed: %d body=%s", updateResp.Code, updateResp.Body.String())
	}

	failedResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather_secret.get_forecast","arguments":{"city":"Shanghai"}}`)
	if failedResp.Code != http.StatusBadRequest {
		t.Fatalf("expected disabled secret to fail closed, got %d body=%s", failedResp.Code, failedResp.Body.String())
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 1 {
		t.Fatalf("disabled secret must not trigger another MCP request, got %d", got)
	}
}

func putSecretJSON(t *testing.T, srv *App, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func deleteSecretJSON(t *testing.T, srv *App, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func getSecretList(t *testing.T, srv *App) []model.Secret {
	t.Helper()

	resp := getJSON(t, srv, "/api/secrets")
	if resp.Code != http.StatusOK {
		t.Fatalf("list secrets failed: %d body=%s", resp.Code, resp.Body.String())
	}
	var list struct {
		Items []model.Secret `json:"items"`
	}
	decodeBody(t, resp.Body.Bytes(), &list)
	return list.Items
}

func createConnector(t *testing.T, srv *App, body string) model.Connector {
	t.Helper()

	resp := postJSON(t, srv, "/api/connectors", body)
	if resp.Code != http.StatusCreated && resp.Code != http.StatusConflict {
		t.Fatalf("create connector failed: %d body=%s", resp.Code, resp.Body.String())
	}
	var connector model.Connector
	if resp.Code == http.StatusCreated {
		decodeBody(t, resp.Body.Bytes(), &connector)
	}
	return connector
}

func assertSecretUsage(t *testing.T, srv *App, secretID, connectorType, field, leakedValue string) {
	t.Helper()

	resp := getJSON(t, srv, "/api/secrets/"+secretID+"/usage")
	if resp.Code != http.StatusOK {
		t.Fatalf("get secret usage failed: %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), leakedValue) {
		t.Fatalf("usage response leaked secret value: %s", resp.Body.String())
	}
	var usage model.SecretUsageResponse
	decodeBody(t, resp.Body.Bytes(), &usage)
	if usage.CanDelete || len(usage.Usages) != 1 {
		t.Fatalf("expected one blocking usage, got %+v", usage)
	}
	got := usage.Usages[0]
	if got.ConnectorType != connectorType || got.Field != field || got.ConnectorID == "" || got.ConnectorDisplayName == "" {
		t.Fatalf("unexpected usage: %+v", got)
	}
}

func mustConnectorID(t *testing.T, raw []byte) string {
	t.Helper()

	var connector model.Connector
	decodeBody(t, raw, &connector)
	if connector.ID == "" {
		t.Fatalf("connector id missing: %s", string(raw))
	}
	return connector.ID
}

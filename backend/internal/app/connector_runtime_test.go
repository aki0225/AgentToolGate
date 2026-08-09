package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestDisabledHTTPConnectorBlocksRuntimeCall(t *testing.T) {
	var requestCount int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, upstream.URL)},
	})
	createDisabledConnector(t, st, workspace.ID, "http", "default")

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+upstream.URL+`/status"}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "disabled") {
		t.Fatalf("expected disabled HTTP connector to reject call, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("disabled HTTP connector must not call upstream, got %d", got)
	}
}

func TestDisabledGitHubConnectorBlocksRuntimeCall(t *testing.T) {
	var requestCount int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		apiBaseURL:   upstream.URL,
		allowedRepos: []string{"acme/demo"},
	})
	createDisabledConnector(t, st, workspace.ID, "github", "default")

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "disabled") {
		t.Fatalf("expected disabled GitHub connector to reject call, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("disabled GitHub connector must not call upstream, got %d", got)
	}
}

func TestDisabledDatabaseConnectorBlocksRuntimeCall(t *testing.T) {
	srv, st, workspace := newDatabaseQueryTestApp(t, "", "", []string{"public.orders"}, 10)
	createDisabledConnector(t, st, workspace.ID, "database", "local_postgres")

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT id FROM public.orders"}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "disabled") {
		t.Fatalf("expected disabled database connector to reject call, got %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestConnectorConfigPreservesAllowedHosts(t *testing.T) {
	normalized, err := normalizeConnectorConfigJSON([]byte(`{"allowedHosts":["127.0.0.1:18080"],"allowedMethods":["GET"]}`))
	if err != nil {
		t.Fatalf("normalize connector config: %v", err)
	}
	if strings.Contains(string(normalized), "[REDACTED]") || !strings.Contains(string(normalized), "127.0.0.1:18080") {
		t.Fatalf("allowedHosts must remain usable runtime config, got %s", normalized)
	}
}

func TestHTTPConnectorConfigControlsRuntimeAllowlistAndMethods(t *testing.T) {
	var requestCount int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{"example.invalid:443", mustURLHost(t, upstream.URL)},
	})
	createRuntimeConnector(t, st, workspace.ID, "http", "default", `{
		"allowedHosts":["`+mustURLHost(t, upstream.URL)+`"],
		"allowedMethods":["GET"]
	}`, true)

	getResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+upstream.URL+`/status"}}`)
	if getResp.Code != http.StatusOK {
		t.Fatalf("connector allowlist should authorize GET, got %d body=%s", getResp.Code, getResp.Body.String())
	}
	var getResult toolCallResponse
	decodeBody(t, getResp.Body.Bytes(), &getResult)
	if getResult.Status != "success" {
		t.Fatalf("connector allowlist GET should execute, got %+v", getResult)
	}

	postResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+upstream.URL+`/items"}}`)
	if postResp.Code != http.StatusBadRequest || !strings.Contains(postResp.Body.String(), "method POST is not allowed") {
		t.Fatalf("connector method allowlist should reject POST before approval, got %d body=%s", postResp.Code, postResp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 1 {
		t.Fatalf("rejected POST must not reach upstream, got %d requests", got)
	}
}

func TestHTTPConnectorCannotWidenEnvironmentAllowlist(t *testing.T) {
	var requestCount int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{"example.invalid:443"},
	})
	createRuntimeConnector(t, st, workspace.ID, "http", "default", `{
		"allowedHosts":["`+mustURLHost(t, upstream.URL)+`"],
		"allowedMethods":["GET"]
	}`, true)

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+upstream.URL+`/status"}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "whitelist is not configured") {
		t.Fatalf("connector must not widen deployment allowlist, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("blocked host must not reach upstream, got %d requests", got)
	}
}

func TestGitHubConnectorCannotOverrideDeploymentAPIBaseURL(t *testing.T) {
	var deploymentRequests int32
	deployment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&deploymentRequests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(deployment.Close)

	var alternateRequests int32
	alternate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&alternateRequests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(alternate.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		apiBaseURL:   deployment.URL,
		allowedRepos: []string{"acme/demo"},
	})
	createRuntimeConnector(t, st, workspace.ID, "github", "default", fmt.Sprintf(`{
		"apiBaseURL":%q,
		"allowedRepos":["acme/demo"]
	}`, alternate.URL), true)

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "must match deployment") {
		t.Fatalf("connector must not override deployment GitHub API URL, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&deploymentRequests); got != 0 {
		t.Fatalf("rejected connector must not reach deployment upstream, got %d requests", got)
	}
	if got := atomic.LoadInt32(&alternateRequests); got != 0 {
		t.Fatalf("rejected connector must not reach alternate upstream, got %d requests", got)
	}
}

func TestGitHubConnectorCannotWidenDeploymentRepoAllowlist(t *testing.T) {
	var requestCount int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		apiBaseURL:   upstream.URL,
		allowedRepos: []string{"acme/demo"},
	})
	createRuntimeConnector(t, st, workspace.ID, "github", "default", `{
		"allowedRepos":["octo/tools"]
	}`, true)

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"octo","repo":"tools","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "whitelist is not configured") {
		t.Fatalf("connector must not widen deployment repository allowlist, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&requestCount); got != 0 {
		t.Fatalf("blocked repository must not reach GitHub, got %d requests", got)
	}
}

func TestGitHubConnectorCanNarrowDeploymentRepoAllowlist(t *testing.T) {
	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		allowedRepos: []string{"acme/demo", "octo/tools"},
	})
	createRuntimeConnector(t, st, workspace.ID, "github", "default", `{
		"allowedRepos":["acme/demo"]
	}`, true)

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.list_repos","arguments":{}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("connector subset should remain usable, got %d body=%s", resp.Code, resp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	rawResult := formatJSONForTest(response.Result)
	if response.Status != "success" || !strings.Contains(rawResult, "acme/demo") || strings.Contains(rawResult, "octo/tools") {
		t.Fatalf("expected connector to narrow repository list, got %+v", response)
	}
}

func TestGitHubConnectorRejectsUnsafeAPIBaseURL(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"userinfo": "https://user@example.com",
		"query":    "https://api.github.com?token=synthetic",
		"fragment": "https://api.github.com#fragment",
		"scheme":   "file:///tmp/github",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitHubConnectorConfig([]byte(fmt.Sprintf(`{"apiBaseURL":%q}`, raw))); err == nil {
				t.Fatalf("expected unsafe GitHub API base URL %q to be rejected", raw)
			}
		})
	}
}

func createDisabledConnector(t *testing.T, st store.Store, workspaceID, connectorType, name string) {
	t.Helper()

	createRuntimeConnector(t, st, workspaceID, connectorType, name, `{}`, false)
}

func createRuntimeConnector(t *testing.T, st store.Store, workspaceID, connectorType, name, configJSON string, enabled bool) {
	t.Helper()

	_, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspaceID,
		Type:        connectorType,
		Name:        name,
		DisplayName: connectorType + "." + name,
		ConfigJSON:  []byte(configJSON),
		Enabled:     enabled,
	})
	if err != nil {
		t.Fatalf("create %s connector: %v", connectorType, err)
	}
}

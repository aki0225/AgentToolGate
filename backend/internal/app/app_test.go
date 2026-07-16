package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
	"log/slog"
)

func TestMockEchoToolCallPersistsAuditWithoutExecutionInput(t *testing.T) {
	t.Parallel()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	req := httptest.NewRequest(http.MethodPost, "/api/tool-calls", bytes.NewBufferString(`{"tool":"mock.echo","arguments":{"message":"hello"}}`))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response toolCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "success" {
		t.Fatalf("expected success, got %+v", response)
	}
	if response.TraceID == "" {
		t.Fatalf("trace id missing: %+v", response)
	}

	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	calls, err := st.ListToolCalls(context.Background(), workspaces[0].ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if string(calls[0].InputRedactedJSON) == "" || string(calls[0].OutputRedactedJSON) == "" {
		t.Fatalf("expected persisted payloads, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("direct mock execution must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
	}
}

func TestCreateToolRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	req := httptest.NewRequest(http.MethodPost, "/api/tools", bytes.NewBufferString(`{"namespace":"mock","name":`))
	rec := httptest.NewRecorder()

	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequestedWorkspaceOrgIDOnlyFallsBackForLocalApprovalStream(t *testing.T) {
	t.Parallel()

	localApp := &App{cfg: config.Config{AuthMode: "local"}}
	localReq := httptest.NewRequest(http.MethodGet, "/api/approvals/stream?workspaceOrgId=local-org", nil)
	if got := localApp.requestedWorkspaceOrgID(localReq); got != "local-org" {
		t.Fatalf("expected local approval stream query fallback, got %q", got)
	}

	oidcApp := &App{cfg: config.Config{AuthMode: "oidc"}}
	oidcReq := httptest.NewRequest(http.MethodGet, "/api/approvals/stream?workspaceOrgId=oidc-org", nil)
	if got := oidcApp.requestedWorkspaceOrgID(oidcReq); got != "" {
		t.Fatalf("expected oidc query fallback to be disabled, got %q", got)
	}

	headerReq := httptest.NewRequest(http.MethodGet, "/api/tool-calls?workspaceOrgId=query-org", nil)
	headerReq.Header.Set("X-Workspace-Org-Id", "header-org")
	if got := oidcApp.requestedWorkspaceOrgID(headerReq); got != "header-org" {
		t.Fatalf("expected header to win, got %q", got)
	}
}

func TestEmbeddedFrontendDoesNotSwallowBackendRoutes(t *testing.T) {
	t.Parallel()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	indexReq := httptest.NewRequest(http.MethodGet, "/", nil)
	indexRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(indexRec, indexReq)
	if indexRec.Code != http.StatusOK {
		t.Fatalf("expected spa index to return 200, got %d body=%s", indexRec.Code, indexRec.Body.String())
	}
	if ct := indexRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected html content type, got %q body=%s", ct, indexRec.Body.String())
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health route to stay live, got %d body=%s", healthRec.Code, healthRec.Body.String())
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/api/public/workspaces", nil)
	apiRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusOK {
		t.Fatalf("expected api route to stay live, got %d body=%s", apiRec.Code, apiRec.Body.String())
	}
	if !strings.Contains(apiRec.Body.String(), "items") {
		t.Fatalf("expected api json response, got %s", apiRec.Body.String())
	}

	mcpReq := httptest.NewRequest(http.MethodGet, "/mcp/unknown", nil)
	mcpRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(mcpRec, mcpReq)
	if mcpRec.Code == http.StatusOK && strings.Contains(mcpRec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("mcp route was swallowed by spa fallback: %s", mcpRec.Body.String())
	}
}

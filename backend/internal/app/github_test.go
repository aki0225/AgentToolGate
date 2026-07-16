package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestMemoryBootstrapRegistersGitHubTools(t *testing.T) {
	t.Parallel()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
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

	expected := map[string]struct {
		operationType    string
		riskLevel        string
		requiresApproval bool
	}{
		"github.list_repos":       {"read", "low", false},
		"github.get_pull_request": {"read", "medium", false},
		"github.create_issue":     {"create", "medium", true},
	}
	for key, want := range expected {
		tool, err := st.GetToolByKey(context.Background(), workspaces[0].ID, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if tool.OperationType != want.operationType || tool.RiskLevel != want.riskLevel || tool.RequiresApproval != want.requiresApproval {
			t.Fatalf("unexpected %s metadata: %+v", key, tool)
		}
	}
}

func TestGitHubListReposReturnsAllowedRepos(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGitHubTestApp(t, githubTestConfig{
		allowedRepos: []string{"acme/demo", "octo/tools"},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.list_repos","arguments":{}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("expected success, got %+v", response)
	}
	if !strings.Contains(formatJSONForTest(response.Result), "acme/demo") || !strings.Contains(formatJSONForTest(response.Result), "octo/tools") {
		t.Fatalf("expected allowed repos in result, got %+v", response.Result)
	}
}

func TestGitHubGetPullRequestSuccessWritesAudit(t *testing.T) {
	t.Parallel()

	const secretToken = "ghp_test_secret"
	const argumentSecret = "agent_supplied_secret"
	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/demo/pulls/42" {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+secretToken {
			t.Fatalf("missing Authorization header")
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
		token:        secretToken,
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":42,"token":"agent_supplied_secret","authorization":"Bearer agent_supplied_secret"}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("expected success, got %+v", response)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("expected one GitHub request, got %d", requestCount)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Status != "success" || calls[0].PolicyDecision != "allow" || calls[0].ToolKey != "github.get_pull_request" {
		t.Fatalf("unexpected call audit: %+v", calls[0])
	}
	assertNoGitHubSecretInCalls(t, calls, secretToken)
	assertNoGitHubSecretInCalls(t, calls, argumentSecret)
}

func TestGitHubGetPullRequestRejectsNonWhitelistedRepoWithAudit(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(mockGitHub.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"evil","repo":"private","pullNumber":1}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("non-whitelisted repo should not call GitHub, got %d request(s)", requestCount)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Status != "failed" || calls[0].PolicyDecision != "allow" || !strings.Contains(calls[0].ErrorMessage, "not allowed") {
		t.Fatalf("unexpected failed audit: %+v", calls[0])
	}
}

func TestGitHubCreateIssueRequiresApprovalThenExecutesAfterApprove(t *testing.T) {
	t.Parallel()

	const secretToken = "ghp_issue_secret"
	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/demo/issues" {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+secretToken {
			t.Fatalf("missing Authorization header")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode issue body: %v", err)
		}
		if body["title"] != "Bug report" || body["body"] != "Created by AgentToolGate" {
			t.Fatalf("unexpected issue body: %+v", body)
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"number":   7,
			"title":    "Bug report",
			"state":    "open",
			"html_url": "https://github.example/acme/demo/issues/7",
		})
	}))
	t.Cleanup(mockGitHub.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		token:        secretToken,
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.create_issue","arguments":{"owner":"acme","repo":"demo","title":"Bug report","body":"Created by AgentToolGate"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("create_issue must not call GitHub before approval")
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.Approval.Status != "approved" || action.ToolCall.Status != "success" {
		t.Fatalf("unexpected approval action: %+v", action)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("expected one GitHub request after approval, got %d", requestCount)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 || calls[0].Status != "success" || calls[0].ApprovalStatus != "approved" {
		t.Fatalf("unexpected call audit: %+v", calls)
	}
	assertNoGitHubSecretInCalls(t, calls, secretToken)
}

func TestGitHubCreateIssueRejectSkipsGitHubRequest(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(mockGitHub.Close)

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		apiBaseURL:   mockGitHub.URL,
		allowedRepos: []string{"acme/demo"},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.create_issue","arguments":{"owner":"acme","repo":"demo","title":"Bug report","body":"Created by AgentToolGate"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", "")
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, rejectResp.Body.Bytes(), &action)
	if action.Approval.Status != "rejected" || action.ToolCall.Status != "rejected" {
		t.Fatalf("unexpected reject action: %+v", action)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("reject must not call GitHub, got %d request(s)", requestCount)
	}

	call, err := st.GetToolCallByID(context.Background(), workspace.ID, action.ToolCall.ID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if call.Status != "rejected" || call.ApprovalStatus != "rejected" {
		t.Fatalf("unexpected stored call: %+v", call)
	}
}

func TestGitHubGetPullRequestMissingTokenWritesFailedAudit(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
		noToken:      true,
		allowedRepos: []string{"acme/demo"},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":1}}`)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(strings.ToLower(resp.Body.String()), "authorization") {
		t.Fatalf("error response should not mention authorization header: %s", resp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Status != "failed" || !strings.Contains(calls[0].ErrorMessage, "github token is not configured") {
		t.Fatalf("unexpected failed audit: %+v", calls[0])
	}
}

func TestGitHubClientValidationErrorsWriteFailedAudit(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(mockGitHub.Close)

	cases := []struct {
		name string
		body string
	}{
		{
			name: "bad owner",
			body: `{"tool":"github.get_pull_request","arguments":{"owner":"-bad","repo":"demo","pullNumber":1}}`,
		},
		{
			name: "bad repo",
			body: `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"bad/repo","pullNumber":1}}`,
		},
		{
			name: "bad pull number",
			body: `{"tool":"github.get_pull_request","arguments":{"owner":"acme","repo":"demo","pullNumber":0}}`,
		},
		{
			name: "empty title",
			body: `{"tool":"github.create_issue","arguments":{"owner":"acme","repo":"demo","title":"","body":"body"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st, workspace := newGitHubTestApp(t, githubTestConfig{
				apiBaseURL:   mockGitHub.URL,
				allowedRepos: []string{"acme/demo"},
			})
			resp := postJSON(t, srv, "/api/tool-calls", tc.body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
			}
			calls, err := st.ListToolCalls(context.Background(), workspace.ID)
			if err != nil {
				t.Fatalf("list tool calls: %v", err)
			}
			if len(calls) != 1 || calls[0].Status != "failed" || calls[0].ErrorMessage == "" {
				t.Fatalf("expected failed audit, got %+v", calls)
			}
		})
	}

	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("validation failures must not call GitHub, got %d request(s)", requestCount)
	}
}

type githubTestConfig struct {
	token        string
	noToken      bool
	apiBaseURL   string
	allowedRepos []string
}

func newGitHubTestApp(t *testing.T, overrides githubTestConfig) (*App, store.Store, model.Workspace) {
	t.Helper()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
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

	token := overrides.token
	if overrides.noToken {
		token = ""
	} else if token == "" {
		token = "ghp_default_test_token"
	}
	apiBaseURL := overrides.apiBaseURL
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.invalid"
	}
	allowedRepos := overrides.allowedRepos
	if len(allowedRepos) == 0 {
		allowedRepos = []string{"acme/demo"}
	}

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
		GitHubToken:           token,
		GitHubAPIBaseURL:      apiBaseURL,
		GitHubAllowedRepos:    allowedRepos,
		GitHubTimeoutMs:       3000,
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	appStore := approvalReviewableTestStore(st)
	srv := New(cfg, appStore, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, appStore, workspaces[0]
}

func assertNoGitHubSecretInCalls(t *testing.T, calls []model.ToolCall, secret string) {
	t.Helper()

	for _, call := range calls {
		raw := strings.Join([]string{
			string(call.InputRedactedJSON),
			string(call.OutputRedactedJSON),
			call.ErrorMessage,
		}, "\n")
		if strings.Contains(raw, secret) {
			t.Fatalf("secret token leaked into audit fields: %+v", call)
		}
	}
}

func formatJSONForTest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

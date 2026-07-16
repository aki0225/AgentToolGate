package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
)

func TestRBACViewerCannotMutateOrExecute(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestAppWithRole(t, roleViewer)
	tool := createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", false)
	approval := seedRBACApproval(t, st, workspace.ID, tool)
	policy := seedRBACPolicy(t, st, workspace.ID)
	secret := seedRBACSecret(t, st, workspace)
	connector := seedRBACConnector(t, st, workspace.ID)

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "create tool", method: http.MethodPost, path: "/api/tools", body: `{"namespace":"mock","name":"viewer_created"}`},
		{name: "execute tool", method: http.MethodPost, path: "/api/tool-calls", body: `{"tool":"mock.write","arguments":{"message":"no"}}`},
		{name: "approve", method: http.MethodPost, path: "/api/approvals/" + approval.ID + "/approve"},
		{name: "reject", method: http.MethodPost, path: "/api/approvals/" + approval.ID + "/reject"},
		{name: "create policy", method: http.MethodPost, path: "/api/policies", body: rbacPolicyPayload("viewer-created")},
		{name: "update policy", method: http.MethodPut, path: "/api/policies/" + policy.ID, body: rbacPolicyPayload("viewer-updated")},
		{name: "delete policy", method: http.MethodDelete, path: "/api/policies/" + policy.ID},
		{name: "create secret", method: http.MethodPost, path: "/api/secrets", body: rbacSecretPayload("viewer_secret")},
		{name: "update secret", method: http.MethodPut, path: "/api/secrets/" + secret.ID, body: rbacSecretPayload("viewer_secret_updated")},
		{name: "delete secret", method: http.MethodDelete, path: "/api/secrets/" + secret.ID + "?force=true"},
		{name: "create connector", method: http.MethodPost, path: "/api/connectors", body: rbacConnectorPayload("viewer_connector")},
		{name: "update connector", method: http.MethodPatch, path: "/api/connectors/" + connector.ID, body: `{"displayName":"Viewer Updated"}`},
		{name: "sync connector", method: http.MethodPost, path: "/api/connectors/" + connector.ID + "/sync"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBACRequest(t, srv, tc.method, tc.path, tc.body, "")
			assertRBACForbidden(t, rec)
		})
	}
}

func TestRBACApproverCanReviewAuditAndExecuteButCannotManage(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestAppWithRole(t, roleApprover)
	createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected approver execute 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var callResult toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &callResult)
	if callResult.Status != "approval_required" || callResult.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", callResult)
	}

	approvalsResp := getJSON(t, srv, "/api/approvals")
	if approvalsResp.Code != http.StatusOK {
		t.Fatalf("expected approver approvals read 200, got %d body=%s", approvalsResp.Code, approvalsResp.Body.String())
	}
	approveResp := postJSON(t, srv, "/api/approvals/"+callResult.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approver approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	auditResp := getJSON(t, srv, "/api/tool-calls")
	if auditResp.Code != http.StatusOK {
		t.Fatalf("expected approver audit read 200, got %d body=%s", auditResp.Code, auditResp.Body.String())
	}

	policy := seedRBACPolicy(t, st, workspace.ID)
	secret := seedRBACSecret(t, st, workspace)
	connector := seedRBACConnector(t, st, workspace.ID)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "manage policy", method: http.MethodPut, path: "/api/policies/" + policy.ID, body: rbacPolicyPayload("approver-updated")},
		{name: "manage secret", method: http.MethodPut, path: "/api/secrets/" + secret.ID, body: rbacSecretPayload("approver_secret")},
		{name: "manage connector", method: http.MethodPatch, path: "/api/connectors/" + connector.ID, body: `{"displayName":"Approver Updated"}`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBACRequest(t, srv, tc.method, tc.path, tc.body, "")
			assertRBACForbidden(t, rec)
		})
	}
}

func TestRBACAdminCanManageProtectedResources(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestAppWithRole(t, roleAdmin)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{name: "create tool", method: http.MethodPost, path: "/api/tools", body: `{"namespace":"mock","name":"admin_created","displayName":"Admin Created","operationType":"mock","riskLevel":"low","inputSchemaJson":{"type":"object"},"outputSchemaJson":{"type":"object"},"enabled":true}`, status: http.StatusCreated},
		{name: "create policy", method: http.MethodPost, path: "/api/policies", body: rbacPolicyPayload("admin-policy"), status: http.StatusCreated},
		{name: "create secret", method: http.MethodPost, path: "/api/secrets", body: rbacSecretPayload("admin_secret"), status: http.StatusCreated},
		{name: "create connector", method: http.MethodPost, path: "/api/connectors", body: rbacConnectorPayload("admin_connector"), status: http.StatusCreated},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBACRequest(t, srv, tc.method, tc.path, tc.body, "")
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d body=%s", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRBACUnknownRoleFailsClosed(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestAppWithRole(t, "member")

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "dashboard", method: http.MethodGet, path: "/api/dashboard/summary"},
		{name: "tools", method: http.MethodGet, path: "/api/tools"},
		{name: "audit", method: http.MethodGet, path: "/api/tool-calls"},
		{name: "policies", method: http.MethodGet, path: "/api/policies"},
		{name: "simulate", method: http.MethodPost, path: "/api/policies/simulate", body: `{"connectorType":"mock","toolName":"mock.echo","operationType":"mock","riskLevel":"low","resource":""}`},
		{name: "execute", method: http.MethodPost, path: "/api/tool-calls", body: `{"tool":"mock.echo","arguments":{"message":"no"}}`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBACRequest(t, srv, tc.method, tc.path, tc.body, "")
			assertRBACForbidden(t, rec)
		})
	}

	for _, path := range []string{"/api/me", "/api/workspaces"} {
		rec := serveRBACRequest(t, srv, http.MethodGet, path, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected unknown role to read %s, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestRBACWorkspaceIsolationByID(t *testing.T) {
	t.Parallel()

	srv, st, workspaceA := newGovernanceTestApp(t)
	workspaceB, err := st.CreateWorkspace(context.Background(), model.CreateWorkspaceInput{
		Name:                  "Other Workspace",
		Slug:                  "other",
		ZitadelOrganizationID: "other-org",
	})
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	toolB := createMockTool(t, st, workspaceB.ID, "mock", "other", "Other Tool", "mock", "low", false)
	callB, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
		WorkspaceID:        workspaceB.ID,
		RequestID:          "req-other",
		ToolID:             toolB.ID,
		ToolKey:            toolB.Key(),
		Status:             "success",
		RiskLevel:          "low",
		PolicyDecision:     policyAllow,
		InputRedactedJSON:  json.RawMessage(`{}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("seed other tool call: %v", err)
	}
	approvalB := seedRBACApproval(t, st, workspaceB.ID, toolB)
	policyB := seedRBACPolicy(t, st, workspaceB.ID)
	secretB := seedRBACSecret(t, st, workspaceB)
	connectorB := seedRBACConnector(t, st, workspaceB.ID)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "tool", method: http.MethodGet, path: "/api/tools/" + toolB.ID},
		{name: "tool_call", method: http.MethodGet, path: "/api/tool-calls/" + callB.ID},
		{name: "approval", method: http.MethodPost, path: "/api/approvals/" + approvalB.ID + "/approve"},
		{name: "policy", method: http.MethodGet, path: "/api/policies/" + policyB.ID},
		{name: "secret", method: http.MethodGet, path: "/api/secrets/" + secretB.ID},
		{name: "connector", method: http.MethodGet, path: "/api/connectors/" + connectorB.ID},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := serveRBACRequest(t, srv, tc.method, tc.path, "", workspaceA.ZitadelOrganizationID)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected cross-workspace %s to return 404, got %d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRequestedWorkspaceOrgIDIgnoresOIDCQueryFallbackForNormalRoutes(t *testing.T) {
	t.Parallel()

	oidcApp := &App{cfg: config.Config{AuthMode: "oidc"}}
	req := httptest.NewRequest(http.MethodGet, "/api/tools?workspaceOrgId=other-org", nil)
	if got := oidcApp.requestedWorkspaceOrgID(req); got != "" {
		t.Fatalf("OIDC 普通接口不能使用 query workspace fallback，got %q", got)
	}
}

func serveRBACRequest(t *testing.T, srv *App, method, path, body, workspaceOrgID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if strings.TrimSpace(workspaceOrgID) != "" {
		req.Header.Set("X-Workspace-Org-Id", workspaceOrgID)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func assertRBACForbidden(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode forbidden response: %v body=%s", err, rec.Body.String())
	}
	if payload["error"] != "forbidden" {
		t.Fatalf("expected stable forbidden error, got %+v", payload)
	}
}

func seedRBACApproval(t *testing.T, st interface {
	CreateApprovalRequest(context.Context, model.CreateApprovalRequestInput) (model.ApprovalRequest, error)
	CreateToolCall(context.Context, model.CreateToolCallInput) (model.ToolCall, error)
}, workspaceID string, tool model.Tool) model.ApprovalRequest {
	t.Helper()

	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:     workspaceID,
		ToolKey:         tool.Key(),
		ToolDisplayName: tool.DisplayName,
		RequestedBy:     "rbac-test",
		Reason:          "rbac test approval",
		TTL:             time.Hour,
	})
	if err != nil {
		t.Fatalf("seed approval: %v", err)
	}
	if _, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
		WorkspaceID:        workspaceID,
		RequestID:          "req-" + approval.ID,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "low",
		PolicyDecision:     policyRequireApproval,
		ApprovalID:         approval.ID,
		InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
		InputExecutionJSON: json.RawMessage(`{"message":"hello"}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("seed approval tool call: %v", err)
	}
	return approval
}

func seedRBACPolicy(t *testing.T, st interface {
	CreatePolicyRule(context.Context, model.CreatePolicyRuleInput) (model.PolicyRule, error)
}, workspaceID string) model.PolicyRule {
	t.Helper()

	rule, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspaceID,
		Name:            "rbac deny",
		Enabled:         true,
		Priority:        10,
		Effect:          policyDeny,
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "rbac test",
	})
	if err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	return rule
}

func seedRBACSecret(t *testing.T, st interface {
	CreateSecret(context.Context, model.CreateSecretInput) (model.Secret, error)
}, workspace model.Workspace) model.Secret {
	t.Helper()

	secret, err := st.CreateSecret(context.Background(), model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "rbac_secret_" + strings.ReplaceAll(workspace.Slug, "-", "_"),
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "RBAC_SECRET_ENV",
		Metadata:       json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	return secret
}

func seedRBACConnector(t *testing.T, st interface {
	CreateConnector(context.Context, model.CreateConnectorInput) (model.Connector, error)
}, workspaceID string) model.Connector {
	t.Helper()

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspaceID,
		Type:        "github",
		Name:        "rbac_github_" + strings.ReplaceAll(workspaceID, "-", "_"),
		DisplayName: "RBAC GitHub",
		ConfigJSON:  json.RawMessage(`{"allowedRepos":["owner/repo"]}`),
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	return connector
}

func rbacPolicyPayload(name string) string {
	return `{"name":"` + name + `","enabled":true,"priority":10,"effect":"deny","connectorType":"mock","toolNamePattern":"mock.echo","operationType":"*","riskLevel":"*","resourcePattern":"*","reason":"rbac test"}`
}

func rbacSecretPayload(name string) string {
	return `{"name":"` + name + `","description":"rbac","enabled":true,"secretType":"token","valueSource":"env","valueRef":"RBAC_SECRET_ENV","metadata":{}}`
}

func rbacConnectorPayload(name string) string {
	return `{"type":"github","name":"` + name + `","displayName":"RBAC GitHub","configJson":{"allowedRepos":["owner/repo"]},"enabled":true}`
}

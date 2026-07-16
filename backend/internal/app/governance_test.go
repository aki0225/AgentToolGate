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

func TestToolCallRequiresApprovalThenExecutesAfterApprove(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" {
		t.Fatalf("expected approval_required, got %+v", response)
	}
	if response.ApprovalID == "" {
		t.Fatalf("approval id missing: %+v", response)
	}
	if response.ApprovalStatus != "pending" {
		t.Fatalf("expected pending approval status, got %+v", response)
	}

	approvalsResp := getJSON(t, srv, "/api/approvals")
	if approvalsResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approvalsResp.Code, approvalsResp.Body.String())
	}
	var approvals struct {
		Items []model.ApprovalRequest `json:"items"`
	}
	decodeBody(t, approvalsResp.Body.Bytes(), &approvals)
	if len(approvals.Items) != 1 {
		t.Fatalf("expected 1 approval, got %d", len(approvals.Items))
	}
	if approvals.Items[0].Status != "pending" {
		t.Fatalf("expected pending approval, got %+v", approvals.Items[0])
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.Approval.Status != "approved" {
		t.Fatalf("expected approved approval, got %+v", action.Approval)
	}
	if action.ToolCall.Status != "success" {
		t.Fatalf("expected successful tool call, got %+v", action.ToolCall)
	}
	if action.ToolCall.ApprovalStatus != "approved" {
		t.Fatalf("expected approved tool call status, got %+v", action.ToolCall)
	}
	if action.Result == nil {
		t.Fatalf("expected execution result, got nil")
	}

	resultMap, ok := action.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", action.Result)
	}
	if resultMap["tool"] != "mock.write" {
		t.Fatalf("unexpected result payload: %+v", resultMap)
	}

	callDetailResp := getJSON(t, srv, "/api/tool-calls/"+action.ToolCall.ID)
	if callDetailResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callDetailResp.Code, callDetailResp.Body.String())
	}
	var call model.ToolCall
	decodeBody(t, callDetailResp.Body.Bytes(), &call)
	if call.Status != "success" || call.ApprovalStatus != "approved" || call.PolicyDecision != "require_approval" {
		t.Fatalf("unexpected stored tool call: %+v", call)
	}

	callListResp := getJSON(t, srv, "/api/tool-calls")
	if callListResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callListResp.Code, callListResp.Body.String())
	}
	var calls struct {
		Items []model.ToolCall `json:"items"`
	}
	decodeBody(t, callListResp.Body.Bytes(), &calls)
	if len(calls.Items) != 1 {
		t.Fatalf("expected 1 listed tool call, got %d", len(calls.Items))
	}
	if calls.Items[0].ApprovalStatus != "approved" {
		t.Fatalf("expected list approval status to stay aligned, got %+v", calls.Items[0])
	}
}

func TestToolCallApprovalRejectsWithoutExecuting(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "patch", "Mock Patch", "patch", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.patch","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.ApprovalID == "" {
		t.Fatalf("approval id missing: %+v", response)
	}

	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", "")
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}

	var action approvalActionResponse
	decodeBody(t, rejectResp.Body.Bytes(), &action)
	if action.Approval.Status != "rejected" {
		t.Fatalf("expected rejected approval, got %+v", action.Approval)
	}
	if action.ToolCall.Status != "rejected" {
		t.Fatalf("expected rejected tool call, got %+v", action.ToolCall)
	}
	if action.ToolCall.ApprovalStatus != "rejected" {
		t.Fatalf("expected rejected tool call status, got %+v", action.ToolCall)
	}
	if action.Result != nil {
		t.Fatalf("expected no execution result on reject, got %+v", action.Result)
	}
}

func TestToolCallApprovalRedactsStoredOutputAndClearsExecutionInput(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", false)

	const emailLiteral = "alice@example.com"
	const phoneLiteral = "+15551234567"
	const tokenLiteral = "sk_test_1234567890abcdef"
	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"contact `+emailLiteral+` or `+phoneLiteral+` with `+tokenLiteral+`","password":"super-secret"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.ToolCall.Status != "success" || action.ToolCall.ApprovalStatus != "approved" {
		t.Fatalf("unexpected approval action: %+v", action)
	}

	output := string(action.ToolCall.OutputRedactedJSON)
	for _, leaked := range []string{emailLiteral, phoneLiteral, tokenLiteral, "super-secret"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("approval audit leaked %q into output: %s", leaked, output)
		}
	}

	call, err := st.GetToolCallByID(context.Background(), workspace.ID, action.ToolCall.ID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if string(call.InputExecutionJSON) != "{}" {
		t.Fatalf("approved call must clear raw execution input, got %s", call.InputExecutionJSON)
	}
	if strings.Contains(string(call.OutputRedactedJSON), emailLiteral) || strings.Contains(string(call.OutputRedactedJSON), phoneLiteral) {
		t.Fatalf("stored approval audit leaked raw output secrets: %+v", call)
	}
}

func TestApprovalDecisionRequiresReviewerRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{name: "approve", path: "/approve"},
		{name: "reject", path: "/reject"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newGovernanceTestAppWithRole(t, "agent")
			createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", false)

			callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"hello"}}`)
			if callResp.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
			}
			var response toolCallResponse
			decodeBody(t, callResp.Body.Bytes(), &response)
			if response.Status != "approval_required" || response.ApprovalID == "" {
				t.Fatalf("expected pending approval, got %+v", response)
			}

			decisionResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+tc.path, "")
			if decisionResp.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for non reviewer role, got %d body=%s", decisionResp.Code, decisionResp.Body.String())
			}

			approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, response.ApprovalID)
			if err != nil {
				t.Fatalf("get approval: %v", err)
			}
			if approval.Status != "pending" {
				t.Fatalf("forbidden decision must leave approval pending, got %+v", approval)
			}
			call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
			if err != nil {
				t.Fatalf("get tool call: %v", err)
			}
			if call.Status != "approval_required" || call.ApprovalStatus != "pending" {
				t.Fatalf("forbidden decision must not update tool call, got %+v", call)
			}
		})
	}
}

func TestToolCallDynamicRiskReflectsArguments(t *testing.T) {
	srv, st, workspace := newGovernanceTestApp(t)
	srv.cfg.DatabaseQueryAllowedTables = []string{"public.orders"}

	t.Run("sensitive key raises mock risk", func(t *testing.T) {
		createMockTool(t, st, workspace.ID, "mock", "riskcheck", "Mock Risk Check", "mock", "low", false)

		resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.riskcheck","arguments":{"message":"hello","password":"super-secret"}}`)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
		}

		call := latestGovernanceCall(t, st, workspace.ID)
		if call.Status != "success" {
			t.Fatalf("expected success, got %+v", call)
		}
		if call.RiskLevel != "high" {
			t.Fatalf("expected sensitive key to raise risk to high, got %+v", call)
		}
	})

	t.Run("database update raises risk", func(t *testing.T) {
		resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"UPDATE public.orders SET name = 'x'"}}`)
		if resp.Code != http.StatusBadRequest && resp.Code != http.StatusInternalServerError {
			t.Fatalf("expected guarded database update to fail, got %d body=%s", resp.Code, resp.Body.String())
		}

		call := latestGovernanceCall(t, st, workspace.ID)
		if call.ToolKey != "database.query" {
			t.Fatalf("expected database call, got %+v", call)
		}
		if call.RiskLevel != "high" {
			t.Fatalf("expected database update to raise risk to high, got %+v", call)
		}
	})

	t.Run("database select keeps static risk", func(t *testing.T) {
		resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT id FROM public.orders"}}`)
		if resp.Code != http.StatusBadRequest && resp.Code != http.StatusInternalServerError {
			t.Fatalf("expected database select to fail without datasource, got %d body=%s", resp.Code, resp.Body.String())
		}

		call := latestGovernanceCall(t, st, workspace.ID)
		if call.ToolKey != "database.query" {
			t.Fatalf("expected database call, got %+v", call)
		}
		if call.RiskLevel != "medium" {
			t.Fatalf("expected pure select to keep medium risk, got %+v", call)
		}
	})
}

func TestToolCallPolicyDeniesNonMockNamespace(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "demo", "blocked", "Blocked Tool", "mock", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"demo.blocked","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "denied" {
		t.Fatalf("expected denied response, got %+v", response)
	}
	if response.Reason == "" {
		t.Fatalf("expected denial reason, got %+v", response)
	}
	if response.ApprovalID != "" {
		t.Fatalf("did not expect approval id, got %+v", response)
	}

	approvalsResp := getJSON(t, srv, "/api/approvals")
	if approvalsResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approvalsResp.Code, approvalsResp.Body.String())
	}
	var approvals struct {
		Items []model.ApprovalRequest `json:"items"`
	}
	decodeBody(t, approvalsResp.Body.Bytes(), &approvals)
	if len(approvals.Items) != 0 {
		t.Fatalf("expected no approvals, got %d", len(approvals.Items))
	}

	callListResp := getJSON(t, srv, "/api/tool-calls")
	if callListResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callListResp.Code, callListResp.Body.String())
	}
	var calls struct {
		Items []model.ToolCall `json:"items"`
	}
	decodeBody(t, callListResp.Body.Bytes(), &calls)
	if len(calls.Items) != 1 {
		t.Fatalf("expected 1 stored call, got %d", len(calls.Items))
	}
	if calls.Items[0].Status != "denied" || calls.Items[0].PolicyDecision != "deny" {
		t.Fatalf("unexpected denied call state: %+v", calls.Items[0])
	}
}

func TestToolCallPolicyDeniesDisabledToolWithAuditRecord(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	_, err := st.CreateTool(context.Background(), model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mock",
		Name:             "disabled",
		DisplayName:      "Disabled Mock",
		Description:      "disabled test tool",
		OperationType:    "mock",
		RiskLevel:        "low",
		RequiresApproval: false,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          false,
	})
	if err != nil {
		t.Fatalf("create disabled tool: %v", err)
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.disabled","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200 policy response, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "denied" || response.Reason == "" {
		t.Fatalf("expected denied response with reason, got %+v", response)
	}

	callListResp := getJSON(t, srv, "/api/tool-calls")
	if callListResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callListResp.Code, callListResp.Body.String())
	}
	var calls struct {
		Items []model.ToolCall `json:"items"`
	}
	decodeBody(t, callListResp.Body.Bytes(), &calls)
	if len(calls.Items) != 1 {
		t.Fatalf("expected 1 stored call, got %d", len(calls.Items))
	}
	if calls.Items[0].Status != "denied" || calls.Items[0].PolicyDecision != "deny" || calls.Items[0].ErrorMessage == "" {
		t.Fatalf("unexpected disabled denied call state: %+v", calls.Items[0])
	}
}

func newGovernanceTestApp(t *testing.T) (*App, store.Store, model.Workspace) {
	t.Helper()
	return newGovernanceTestAppWithRole(t, "owner")
}

func newGovernanceTestAppWithRole(t *testing.T, role string) (*App, store.Store, model.Workspace) {
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

	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             role,
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	appStore := approvalReviewableTestStore(st)
	srv := New(cfg, appStore, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, appStore, workspaces[0]
}

func createMockTool(t *testing.T, st store.Store, workspaceID, namespace, name, displayName, operationType, riskLevel string, requiresApproval bool) model.Tool {
	t.Helper()

	tool, err := st.CreateTool(context.Background(), model.CreateToolInput{
		WorkspaceID:      workspaceID,
		Namespace:        namespace,
		Name:             name,
		DisplayName:      displayName,
		Description:      "test tool",
		OperationType:    operationType,
		RiskLevel:        riskLevel,
		RequiresApproval: requiresApproval,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}
	return tool
}

func postJSON(t *testing.T, srv *App, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func getJSON(t *testing.T, srv *App, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, raw []byte, target *T) {
	t.Helper()

	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func latestGovernanceCall(t *testing.T, st store.Store, workspaceID string) model.ToolCall {
	t.Helper()

	calls, err := st.ListToolCalls(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected at least one call")
	}
	return calls[0]
}

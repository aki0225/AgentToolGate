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
	"time"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/policy"
	"agenttoolgate/backend/internal/store"
)

const approvalHardeningReviewerSubject = "local-dev"
const approvalHardeningOtherRequester = "other-requester"

type approvalRequesterOverrideStore struct {
	store.Store
	requestedBy   string
	requesterRole string
}

func approvalReviewableTestStore(st store.Store, requesterRoles ...string) store.Store {
	requesterRole := roleOwner
	if len(requesterRoles) > 0 {
		requesterRole = requesterRoles[0]
	}
	return &approvalRequesterOverrideStore{
		Store:         st,
		requestedBy:   approvalHardeningOtherRequester,
		requesterRole: requesterRole,
	}
}

func (s *approvalRequesterOverrideStore) CreateApprovalRequest(ctx context.Context, input model.CreateApprovalRequestInput) (model.ApprovalRequest, error) {
	if strings.TrimSpace(s.requestedBy) != "" {
		input.RequestedBy = strings.TrimSpace(s.requestedBy)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(defaultJSON(input.DecisionPayloadJSON), &payload); err == nil {
		if _, exists := payload["requesterRole"]; !exists {
			frozenPayloadJSON, err := freezeApprovalRequesterRole(input.DecisionPayloadJSON, s.requesterRole)
			if err != nil {
				return model.ApprovalRequest{}, err
			}
			input.DecisionPayloadJSON = frozenPayloadJSON
		}
	}
	return s.Store.CreateApprovalRequest(ctx, input)
}

func TestApprovalSelfReviewForbiddenForApproveAndReject(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "approve", path: "/approve"},
		{name: "reject", path: "/reject"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newApprovalHardeningTestApp(t, false)
			createMockTool(t, st, workspace.ID, "mock", "selfreview", "Self Review", "write", "low", false)

			callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.selfreview","arguments":{"message":"blocked"}}`)
			if callResp.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
			}
			var response toolCallResponse
			decodeBody(t, callResp.Body.Bytes(), &response)
			if response.Status != "approval_required" || response.ApprovalID == "" {
				t.Fatalf("expected approval_required, got %+v", response)
			}

			decisionResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+tc.path, `{"reason":"self review"}`)
			if decisionResp.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d body=%s", decisionResp.Code, decisionResp.Body.String())
			}
			var errorPayload struct {
				Code string `json:"code"`
			}
			decodeBody(t, decisionResp.Body.Bytes(), &errorPayload)
			if errorPayload.Code != approvalSelfReviewDeniedCode {
				t.Fatalf("expected self-review code %q, got %+v", approvalSelfReviewDeniedCode, errorPayload)
			}

			approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, response.ApprovalID)
			if err != nil {
				t.Fatalf("get approval: %v", err)
			}
			if approval.Status != "pending" {
				t.Fatalf("self review must leave approval pending, got %+v", approval)
			}
			call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
			if err != nil {
				t.Fatalf("get tool call: %v", err)
			}
			if call.Status != "approval_required" || call.ApprovalStatus != "pending" {
				t.Fatalf("self review must not update tool call, got %+v", call)
			}
			if string(call.OutputRedactedJSON) != "{}" {
				t.Fatalf("self review must not execute runtime, got output %s", call.OutputRedactedJSON)
			}
		})
	}
}

func TestLocalApprovalAllowsExplicitIndependentReviewerCredential(t *testing.T) {
	const reviewerToken = "synthetic-local-reviewer-token-1234"
	t.Setenv("AGT_LOCAL_REVIEWER_TOKEN", reviewerToken)

	srv, st, workspace := newApprovalHardeningTestApp(t, false)
	createMockTool(t, st, workspace.ID, "mock", "localreview", "Local Review", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.localreview","arguments":{"message":"review me"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	approveResp := postJSONWithBearer(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", `{"reason":"reviewed locally"}`, reviewerToken)
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected explicit local reviewer credential to approve, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.Approval.Status != "approved" || action.Approval.ReviewedBy != "local-reviewer" {
		t.Fatalf("expected independent local reviewer identity, got %+v", action.Approval)
	}
}

func TestApprovalRevalidatesDisabledToolBeforeTransition(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newApprovalHardeningTestApp(t, true)
	tool := createMockTool(t, st, workspace.ID, "mock", "disabledafterrequest", "Disabled After Request", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.disabledafterrequest","arguments":{"message":"do not run"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	enabled := false
	if _, err := st.UpdateTool(context.Background(), workspace.ID, tool.ID, model.UpdateToolInput{Enabled: &enabled}); err != nil {
		t.Fatalf("disable tool: %v", err)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusConflict {
		t.Fatalf("expected 409 after tool was disabled, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	assertApprovalStillPending(t, st, workspace.ID, response.ApprovalID)
}

func TestApprovalRevalidatesManagedPolicyBeforeTransition(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newApprovalHardeningTestApp(t, true)
	createMockTool(t, st, workspace.ID, "mock", "policychanged", "Policy Changed", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.policychanged","arguments":{"message":"do not run"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	if _, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "deny pending call",
		Enabled:         true,
		Priority:        1000,
		Effect:          policyDeny,
		ConnectorType:   "mock",
		ToolNamePattern: "mock.policychanged",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "policy changed before approval",
	}); err != nil {
		t.Fatalf("create deny policy: %v", err)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusConflict {
		t.Fatalf("expected 409 after policy tightened, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	assertApprovalStillPending(t, st, workspace.ID, response.ApprovalID)
}

func TestApprovalRevalidationUsesFrozenRequesterRole(t *testing.T) {
	t.Parallel()

	for _, requesterRole := range []string{roleAgent, "member"} {
		requesterRole := requesterRole
		t.Run(requesterRole, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newApprovalHardeningTestApp(t, true)
			tool := createMockTool(t, st, workspace.ID, "mock", "frozenrole"+requesterRole, "Frozen Role "+requesterRole, "write", "low", false)
			requesterSubject := "requester-" + requesterRole
			requesterCtx := RequestContext{
				Identity: auth.Identity{
					Mode:           "oidc",
					Subject:        requesterSubject,
					Email:          requesterSubject + "@example.test",
					Name:           "Requester " + requesterRole,
					OrganizationID: workspace.ZitadelOrganizationID,
					Role:           requesterRole,
				},
				Workspace: workspace,
				User: model.User{
					ID:            requesterSubject,
					WorkspaceID:   workspace.ID,
					ZitadelUserID: requesterSubject,
					Email:         requesterSubject + "@example.test",
					Name:          "Requester " + requesterRole,
					Role:          requesterRole,
				},
			}

			response, err := srv.createToolCall(
				context.Background(),
				requesterCtx,
				tool.Key(),
				json.RawMessage(`{"message":"must not execute"}`),
			)
			if err != nil {
				t.Fatalf("create requester tool call: %v", err)
			}
			if response.Status != "approval_required" || response.ApprovalID == "" {
				t.Fatalf("expected pending approval, got %+v", response)
			}

			approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, response.ApprovalID)
			if err != nil {
				t.Fatalf("get approval: %v", err)
			}
			frozenRole, err := frozenApprovalRequesterRole(approval)
			if err != nil {
				t.Fatalf("read frozen requester role: %v", err)
			}
			if frozenRole != requesterRole {
				t.Fatalf("expected frozen requester role %q, got %q payload=%s", requesterRole, frozenRole, approval.DecisionPayloadJSON)
			}

			engine, err := policy.NewEngine(append([]policy.Rule{{
				Name:     "deny-original-" + requesterRole,
				Priority: 2000,
				Match: policy.Match{
					ToolNamespace: "mock",
					ToolName:      tool.Name,
					UserRole:      requesterRole,
				},
				Effect: policy.EffectDeny,
				Reason: "original requester role is now denied",
			}}, policy.DefaultRules()...))
			if err != nil {
				t.Fatalf("create tightened policy: %v", err)
			}
			srv.policies = engine

			approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
			if approveResp.Code != http.StatusConflict {
				t.Fatalf("owner approval must conflict after requester role deny, got %d body=%s", approveResp.Code, approveResp.Body.String())
			}
			assertApprovalStillPending(t, st, workspace.ID, response.ApprovalID)
		})
	}
}

func TestApprovalReviewReasonAndFrozenExecutionInput(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newApprovalHardeningTestApp(t, true)
	createMockTool(t, st, workspace.ID, "mock", "freeze", "Freeze Input", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.freeze","arguments":{"message":"original","token":"secret-value"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	badApprove := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", `{"reason":"try replace","arguments":{"message":"tampered"}}`)
	if badApprove.Code != http.StatusBadRequest {
		t.Fatalf("unknown approval body fields must be rejected with 400, got %d body=%s", badApprove.Code, badApprove.Body.String())
	}
	pending, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get pending approval: %v", err)
	}
	if pending.Status != "pending" {
		t.Fatalf("bad approval body must leave approval pending, got %+v", pending)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", `{"reason":"  approve original input  "}`)
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.Approval.Status != "approved" || action.Approval.ReviewedBy != approvalHardeningReviewerSubject {
		t.Fatalf("unexpected approval reviewer/status: %+v", action.Approval)
	}
	if action.Approval.Reason != "approve original input" {
		t.Fatalf("expected trimmed reason, got %q", action.Approval.Reason)
	}
	result, ok := action.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", action.Result)
	}
	echo, ok := result["echo"].(map[string]any)
	if !ok {
		t.Fatalf("expected echo map, got %+v", result["echo"])
	}
	if echo["message"] != "original" {
		t.Fatalf("approval must execute frozen original input, got echo=%+v", echo)
	}
	if echo["message"] == "tampered" {
		t.Fatalf("approval executed tampered input: %+v", echo)
	}
	if echo["token"] != "[REDACTED]" {
		t.Fatalf("approval response must not return the frozen secret, got echo=%+v", echo)
	}
	call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if string(call.InputExecutionJSON) != "{}" {
		t.Fatalf("approved call must clear raw execution input, got %s", call.InputExecutionJSON)
	}
}

func TestApprovalRejectStoresReasonWithoutExecuting(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newApprovalHardeningTestApp(t, true)
	createMockTool(t, st, workspace.ID, "mock", "rejectreason", "Reject Reason", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.rejectreason","arguments":{"message":"do not run"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", `{"reason":"  unsafe request  "}`)
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, rejectResp.Body.Bytes(), &action)
	if action.Approval.Status != "rejected" || action.Approval.ReviewedBy != approvalHardeningReviewerSubject || action.Approval.Reason != "unsafe request" {
		t.Fatalf("unexpected rejected approval: %+v", action.Approval)
	}
	if action.ToolCall.Status != "rejected" || action.ToolCall.ApprovalStatus != "rejected" {
		t.Fatalf("unexpected rejected tool call: %+v", action.ToolCall)
	}
	if action.Result != nil {
		t.Fatalf("reject must not execute runtime, got result=%+v", action.Result)
	}
	call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if string(call.OutputRedactedJSON) != "{}" {
		t.Fatalf("reject must not execute runtime, got output %s", call.OutputRedactedJSON)
	}
}

func TestExpiredApprovalCannotApproveOrReject(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "approve", path: "/approve"},
		{name: "reject", path: "/reject"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newApprovalHardeningTestApp(t, false)
			tool := createMockTool(t, st, workspace.ID, "mock", "expired"+tc.name, "Expired "+tc.name, "write", "low", false)
			approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
				WorkspaceID:     workspace.ID,
				ToolKey:         tool.Key(),
				ToolDisplayName: tool.DisplayName,
				RequestedBy:     approvalHardeningOtherRequester,
				Reason:          "expired test",
				TTL:             -time.Minute,
			})
			if err != nil {
				t.Fatalf("create expired approval: %v", err)
			}
			if _, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
				WorkspaceID:        workspace.ID,
				RequestID:          "req-expired-" + tc.name,
				ToolID:             tool.ID,
				ToolKey:            tool.Key(),
				Status:             "approval_required",
				RiskLevel:          "low",
				PolicyDecision:     policyRequireApproval,
				ApprovalID:         approval.ID,
				InputRedactedJSON:  json.RawMessage(`{"message":"expired"}`),
				InputExecutionJSON: json.RawMessage(`{"message":"expired"}`),
				OutputRedactedJSON: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatalf("create expired tool call: %v", err)
			}

			decisionResp := postJSON(t, srv, "/api/approvals/"+approval.ID+tc.path, `{"reason":"too late"}`)
			if decisionResp.Code != http.StatusConflict {
				t.Fatalf("expected 409 for expired approval, got %d body=%s", decisionResp.Code, decisionResp.Body.String())
			}
			call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, approval.ID)
			if err != nil {
				t.Fatalf("get expired tool call: %v", err)
			}
			if call.Status != "approval_required" || call.ApprovalStatus != "expired" {
				t.Fatalf("expired review must not execute or mark success, got %+v", call)
			}
			if string(call.OutputRedactedJSON) != "{}" {
				t.Fatalf("expired review must not execute runtime, got output %s", call.OutputRedactedJSON)
			}
		})
	}
}

func TestApprovalApproveEmptyBodyRemainsCompatible(t *testing.T) {
	t.Parallel()

	srv, _, workspace := newApprovalHardeningTestApp(t, true)
	createMockTool(t, srv.store, workspace.ID, "mock", "emptybody", "Empty Body", "write", "low", false)

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.emptybody","arguments":{"message":"legacy"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected legacy empty body approve to stay 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
}

func newApprovalHardeningTestApp(t *testing.T, overrideRequester bool) (*App, store.Store, model.Workspace) {
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
		LocalSubject:          approvalHardeningReviewerSubject,
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	var appStore store.Store = st
	if overrideRequester {
		appStore = approvalReviewableTestStore(st, cfg.LocalRole)
	}
	return New(cfg, appStore, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), appStore, workspaces[0]
}

func postJSONWithBearer(t *testing.T, srv *App, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func assertApprovalStillPending(t *testing.T, st store.Store, workspaceID, approvalID string) {
	t.Helper()

	approval, err := st.GetApprovalRequestByID(context.Background(), workspaceID, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if approval.Status != "pending" {
		t.Fatalf("approval must remain pending, got %+v", approval)
	}
	call, err := st.GetToolCallByApprovalID(context.Background(), workspaceID, approvalID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if call.Status != "approval_required" || call.ApprovalStatus != "pending" || string(call.OutputRedactedJSON) != "{}" {
		t.Fatalf("tool call must remain unexecuted, got %+v", call)
	}
}

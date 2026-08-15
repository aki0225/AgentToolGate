package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestApprovalListSupportsPaginationStatusCountsAndCallID(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	tool := createMockTool(t, st, workspace.ID, "mock", "approval_page", "Approval Page", "write", "medium", true)
	seedApprovalListItem(t, st, workspace.ID, tool, "pending")
	approved := seedApprovalListItem(t, st, workspace.ID, tool, "approved")
	seedApprovalListItem(t, st, workspace.ID, tool, "rejected")

	resp := getJSON(t, srv, "/api/approvals?page=1&pageSize=1&status=approved")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var payload approvalListResponse
	decodeBody(t, resp.Body.Bytes(), &payload)
	if payload.Total != 1 || payload.Page != 1 || payload.PageSize != 1 || len(payload.Items) != 1 {
		t.Fatalf("unexpected approval page: %+v", payload)
	}
	if payload.Items[0].ID != approved.approval.ID || payload.Items[0].CallID != approved.call.ID {
		t.Fatalf("approval list must expose the stable callId, got %+v", payload.Items[0])
	}
	if payload.StatusCounts["pending"] != 1 || payload.StatusCounts["approved"] != 1 || payload.StatusCounts["rejected"] != 1 {
		t.Fatalf("unexpected approval status counts: %+v", payload.StatusCounts)
	}
	for _, status := range []string{"expired", "consumed"} {
		if _, ok := payload.StatusCounts[status]; !ok {
			t.Fatalf("statusCounts must include %s even when zero: %+v", status, payload.StatusCounts)
		}
	}
}

func TestApprovalListValidatesAndBoundsPagination(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	for _, path := range []string{
		"/api/approvals?page=0",
		"/api/approvals?page=invalid",
		"/api/approvals?pageSize=0",
		"/api/approvals?pageSize=invalid",
		"/api/approvals?status=unknown",
	} {
		resp := getJSON(t, srv, path)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s expected 400, got %d body=%s", path, resp.Code, resp.Body.String())
		}
	}

	maxInt := int(^uint(0) >> 1)
	overflowResp := getJSON(t, srv, "/api/approvals?page="+strconv.Itoa(maxInt)+"&pageSize=100")
	if overflowResp.Code != http.StatusBadRequest {
		t.Fatalf("overflowing approval page expected 400, got %d body=%s", overflowResp.Code, overflowResp.Body.String())
	}

	resp := getJSON(t, srv, "/api/approvals?pageSize=1000")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected oversized pageSize to be bounded, got %d body=%s", resp.Code, resp.Body.String())
	}
	var payload approvalListResponse
	decodeBody(t, resp.Body.Bytes(), &payload)
	if payload.Page != 1 || payload.PageSize != 100 {
		t.Fatalf("unexpected bounded pagination: %+v", payload)
	}
}

func TestApprovalFailureResponsesExposeStableMachineCodes(t *testing.T) {
	t.Parallel()

	t.Run("permission denied", func(t *testing.T) {
		srv, _, _ := newGovernanceTestAppWithRole(t, roleViewer)
		resp := getJSON(t, srv, "/api/approvals")
		assertApprovalErrorCode(t, resp, http.StatusForbidden, approvalPermissionDeniedCode)
	})

	t.Run("approval expired", func(t *testing.T) {
		srv, st, workspace := newGovernanceTestApp(t)
		tool := createMockTool(t, st, workspace.ID, "mock", "expired_code", "Expired Code", "write", "medium", true)
		item := seedApprovalListItemWithTTL(t, st, workspace.ID, tool, "pending", -time.Minute)
		resp := postJSON(t, srv, "/api/approvals/"+item.approval.ID+"/approve", "")
		assertApprovalErrorCode(t, resp, http.StatusConflict, approvalExpiredCode)
	})

	t.Run("approval already reviewed", func(t *testing.T) {
		srv, st, workspace := newGovernanceTestApp(t)
		tool := createMockTool(t, st, workspace.ID, "mock", "reviewed_code", "Reviewed Code", "write", "medium", true)
		item := seedApprovalListItem(t, st, workspace.ID, tool, "rejected")
		resp := postJSON(t, srv, "/api/approvals/"+item.approval.ID+"/approve", "")
		assertApprovalErrorCode(t, resp, http.StatusConflict, approvalAlreadyReviewedCode)
	})

	t.Run("consumed agent guard reject conflicts", func(t *testing.T) {
		srv, st, workspace := newGovernanceTestApp(t)
		tool, err := st.GetToolByKey(context.Background(), workspace.ID, agentGuardEvaluateToolKey)
		if err != nil {
			t.Fatalf("get agent guard tool: %v", err)
		}
		item := seedApprovalListItem(t, st, workspace.ID, tool, "consumed")
		if _, err := st.UpdateToolCall(context.Background(), workspace.ID, item.call.ID, model.UpdateToolCallInput{
			Status:             "success",
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{"decision":"allow"}`),
		}); err != nil {
			t.Fatalf("complete consumed agent guard call: %v", err)
		}
		resp := postJSON(t, srv, "/api/approvals/"+item.approval.ID+"/reject", "")
		assertApprovalErrorCode(t, resp, http.StatusConflict, approvalAlreadyReviewedCode)
		current, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, item.approval.ID)
		if err != nil {
			t.Fatalf("get consumed approval: %v", err)
		}
		if current.Status != "consumed" {
			t.Fatalf("consumed reject changed approval status to %q", current.Status)
		}
	})

	t.Run("approval revalidation failed", func(t *testing.T) {
		srv, st, workspace := newGovernanceTestApp(t)
		tool := createMockTool(t, st, workspace.ID, "mock", "revalidation_code", "Revalidation Code", "write", "medium", false)
		callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.revalidation_code","arguments":{"message":"blocked"}}`)
		if callResp.Code != http.StatusOK {
			t.Fatalf("create approval call: status=%d body=%s", callResp.Code, callResp.Body.String())
		}
		var call toolCallResponse
		decodeBody(t, callResp.Body.Bytes(), &call)
		enabled := false
		if _, err := st.UpdateTool(context.Background(), workspace.ID, tool.ID, model.UpdateToolInput{Enabled: &enabled}); err != nil {
			t.Fatalf("disable tool: %v", err)
		}
		resp := postJSON(t, srv, "/api/approvals/"+call.ApprovalID+"/approve", "")
		assertApprovalErrorCode(t, resp, http.StatusConflict, approvalRevalidationFailedCode)
	})
}

func TestApprovalRevalidationPreservesOriginalErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		toolName   string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "permission denied",
			toolName:   "revalidation_permission",
			err:        forbidden("revalidation permission denied"),
			wantStatus: http.StatusForbidden,
			wantCode:   approvalPermissionDeniedCode,
		},
		{
			name:       "tool not found",
			toolName:   "revalidation_not_found",
			err:        store.ErrNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "internal error",
			toolName:   "revalidation_internal",
			err:        errors.New("revalidation backend failed"),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, st, workspace := newGovernanceTestApp(t)
			createMockTool(t, st, workspace.ID, "mock", test.toolName, "Revalidation Classification", "write", "medium", true)
			callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.`+test.toolName+`","arguments":{"message":"blocked"}}`)
			if callResp.Code != http.StatusOK {
				t.Fatalf("create approval call: status=%d body=%s", callResp.Code, callResp.Body.String())
			}
			var call toolCallResponse
			decodeBody(t, callResp.Body.Bytes(), &call)
			if call.ApprovalID == "" {
				t.Fatalf("expected pending approval, got %+v", call)
			}

			srv.store = &approvalRevalidationErrorStore{Store: st, err: test.err}
			resp := postJSON(t, srv, "/api/approvals/"+call.ApprovalID+"/approve", "")
			assertApprovalErrorCode(t, resp, test.wantStatus, test.wantCode)

			approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, call.ApprovalID)
			if err != nil {
				t.Fatalf("get approval after failed revalidation: %v", err)
			}
			if approval.Status != "pending" {
				t.Fatalf("failed revalidation changed approval status to %q", approval.Status)
			}
			storedCall, err := st.GetToolCallByID(context.Background(), workspace.ID, call.CallID)
			if err != nil {
				t.Fatalf("get call after failed revalidation: %v", err)
			}
			if storedCall.Status != "approval_required" {
				t.Fatalf("failed revalidation changed call status to %q", storedCall.Status)
			}
		})
	}
}

func TestApprovalActionResponseExposesRevalidationFailureCode(t *testing.T) {
	t.Parallel()

	response := newApprovalActionResponse(
		model.ApprovalRequest{},
		model.ToolCall{
			Status:       "failed",
			ErrorMessage: approvalRevalidationFailedMessage,
		},
	)
	if response.Code != approvalRevalidationFailedCode {
		t.Fatalf("revalidation failure code = %q, want %q", response.Code, approvalRevalidationFailedCode)
	}
}

type seededApprovalListItem struct {
	approval model.ApprovalRequest
	call     model.ToolCall
}

type approvalRevalidationErrorStore struct {
	store.Store
	err error
}

func (s *approvalRevalidationErrorStore) GetToolByKey(context.Context, string, string) (model.Tool, error) {
	return model.Tool{}, s.err
}

func seedApprovalListItem(
	t *testing.T,
	st store.Store,
	workspaceID string,
	tool model.Tool,
	status string,
) seededApprovalListItem {
	t.Helper()
	return seedApprovalListItemWithTTL(t, st, workspaceID, tool, status, time.Hour)
}

func seedApprovalListItemWithTTL(
	t *testing.T,
	st store.Store,
	workspaceID string,
	tool model.Tool,
	status string,
	ttl time.Duration,
) seededApprovalListItem {
	t.Helper()

	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:     workspaceID,
		ToolKey:         tool.Key(),
		ToolDisplayName: tool.DisplayName,
		RequestedBy:     "independent-requester",
		Reason:          "approval list test",
		TTL:             ttl,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	call, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
		WorkspaceID:        workspaceID,
		RequestID:          "req-" + approval.ID,
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "medium",
		PolicyDecision:     policyRequireApproval,
		ApprovalID:         approval.ID,
		InputRedactedJSON:  json.RawMessage(`{}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create approval tool call: %v", err)
	}
	if status != "pending" {
		approval, err = st.UpdateApprovalRequest(context.Background(), workspaceID, approval.ID, model.UpdateApprovalRequestInput{
			Status:     status,
			ReviewedBy: "reviewer",
		})
		if err != nil {
			t.Fatalf("set approval status %s: %v", status, err)
		}
	}
	return seededApprovalListItem{approval: approval, call: call}
}

func assertApprovalErrorCode(t *testing.T, resp interface {
	Result() *http.Response
}, wantStatus int, wantCode string) {
	t.Helper()

	result := resp.Result()
	defer result.Body.Close()
	var payload struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(result.Body).Decode(&payload); err != nil {
		t.Fatalf("decode approval error: %v", err)
	}
	if result.StatusCode != wantStatus || payload.Code != wantCode {
		t.Fatalf("expected status=%d code=%s, got status=%d payload=%+v", wantStatus, wantCode, result.StatusCode, payload)
	}
}

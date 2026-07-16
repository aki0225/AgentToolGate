package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"
)

var agentGuardHookControlMu sync.Mutex

func TestAgentGuardEvaluateUsesTicketLifecycle(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := filepath.Join(t.TempDir(), "drop.ps1")

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var first agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &first)
	if first.Decision != "deny_with_ticket" || first.ApprovalID == "" || first.Fingerprint == "" {
		t.Fatalf("unexpected first evaluation response: %+v", first)
	}

	approvals, err := st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected 1 approval, got %d", len(approvals))
	}
	if approvals[0].Status != "pending" || approvals[0].Fingerprint == "" || approvals[0].DecisionPayloadJSON == nil {
		t.Fatalf("unexpected pending approval: %+v", approvals[0])
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(calls))
	}
	if calls[0].Status != "approval_required" || string(calls[0].InputExecutionJSON) == "{}" {
		t.Fatalf("approval-required audit must keep raw execution input before review, got %+v", calls[0])
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+first.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var approveAction approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &approveAction)
	if approveAction.Approval.Status != "approved" {
		t.Fatalf("expected approval to become approved, got %+v", approveAction.Approval)
	}
	if approveAction.ToolCall.ApprovalStatus != "approved" {
		t.Fatalf("expected tool call approval status to update, got %+v", approveAction.ToolCall)
	}

	calls, err = st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls after approve: %v", err)
	}
	if calls[0].Status != "approval_required" {
		t.Fatalf("approval should only clear raw input before retry, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("approve must clear raw execution input, got %s", calls[0].InputExecutionJSON)
	}

	second := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
		TicketID:        first.ApprovalID,
	}))
	if second.Code != http.StatusOK {
		t.Fatalf("expected retry 200, got %d body=%s", second.Code, second.Body.String())
	}
	var allowed agentGuardEvaluateResponse
	decodeBody(t, second.Body.Bytes(), &allowed)
	if allowed.Decision != "allow" || allowed.ApprovalID != first.ApprovalID {
		t.Fatalf("unexpected retry allow response: %+v", allowed)
	}

	calls, err = st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls after consume: %v", err)
	}
	if calls[0].Status != "success" || calls[0].ApprovalStatus != "consumed" {
		t.Fatalf("expected ticket consume to finalize audit row, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("consumed ticket must keep raw execution input cleared, got %s", calls[0].InputExecutionJSON)
	}

	third := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
		TicketID:        first.ApprovalID,
	}))
	if third.Code != http.StatusOK {
		t.Fatalf("expected repeated retry 200, got %d body=%s", third.Code, third.Body.String())
	}
	var denied agentGuardEvaluateResponse
	decodeBody(t, third.Body.Bytes(), &denied)
	if denied.Decision != "deny" {
		t.Fatalf("expected consumed ticket to deny repeat retry, got %+v", denied)
	}
}

func TestAgentGuardApprovedTicketRetryWithoutExplicitTicketIDAllows(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := filepath.Join(t.TempDir(), "drop.ps1")

	body := mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var first agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &first)
	if first.Decision != "deny_with_ticket" || first.ApprovalID == "" {
		t.Fatalf("unexpected first evaluation response: %+v", first)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+first.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	retry := postJSON(t, srv, "/api/agent-guard/evaluate", body)
	if retry.Code != http.StatusOK {
		t.Fatalf("expected retry 200, got %d body=%s", retry.Code, retry.Body.String())
	}
	var allowed agentGuardEvaluateResponse
	decodeBody(t, retry.Body.Bytes(), &allowed)
	if allowed.Decision != "allow" {
		t.Fatalf("expected approved retry without ticket id to allow, got %+v", allowed)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected approval row plus remembered success row, got %d", len(calls))
	}
	if calls[0].Status != "success" || calls[0].ApprovalID != first.ApprovalID {
		t.Fatalf("expected current retry to create success audit row linked to approval, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("remembered allow must not persist execution input, got %s", calls[0].InputExecutionJSON)
	}
	if calls[1].Status != "approval_required" || calls[1].ApprovalID != first.ApprovalID {
		t.Fatalf("remembered allow must not mutate original approval audit row, got %+v", calls[1])
	}

	again := postJSON(t, srv, "/api/agent-guard/evaluate", body)
	if again.Code != http.StatusOK {
		t.Fatalf("expected repeated approved fingerprint 200, got %d body=%s", again.Code, again.Body.String())
	}
	var remembered agentGuardEvaluateResponse
	decodeBody(t, again.Body.Bytes(), &remembered)
	if remembered.Decision != "allow" || remembered.ApprovalID != first.ApprovalID {
		t.Fatalf("expected low/medium risk fingerprint memory to allow within TTL, got %+v", remembered)
	}

	calls, err = st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls after repeated memory: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected original approval plus two remembered success audit rows, got %d", len(calls))
	}
	successCount := 0
	approvalRequiredCount := 0
	for _, call := range calls {
		if call.ApprovalID != first.ApprovalID {
			t.Fatalf("expected all remembered audit rows to reference reused approval %s, got %+v", first.ApprovalID, call)
		}
		switch call.Status {
		case "success":
			successCount++
			if string(call.InputExecutionJSON) != "{}" {
				t.Fatalf("remembered allow audit must keep execution input empty, got %s", call.InputExecutionJSON)
			}
		case "approval_required":
			approvalRequiredCount++
		}
	}
	if successCount != 2 || approvalRequiredCount != 1 {
		t.Fatalf("expected two success rows and one original approval row, success=%d approval_required=%d calls=%+v", successCount, approvalRequiredCount, calls)
	}
}

func TestAgentGuardHighRiskApprovedFingerprintDoesNotAutoAllowWithoutTicketID(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := agentGuardSensitiveStartupTarget(t)

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var first agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &first)
	if first.Decision != "deny_with_ticket" || first.ApprovalID == "" {
		t.Fatalf("expected first high-risk request to require approval, got %+v", first)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+first.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	retry := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if retry.Code != http.StatusOK {
		t.Fatalf("expected retry 200, got %d body=%s", retry.Code, retry.Body.String())
	}

	var denied agentGuardEvaluateResponse
	decodeBody(t, retry.Body.Bytes(), &denied)
	if denied.Decision != "deny_with_ticket" || denied.ApprovalID == "" {
		t.Fatalf("expected approved high-risk fingerprint to demand a fresh ticket, got %+v", denied)
	}

	consume := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
		TicketID:        first.ApprovalID,
	}))
	if consume.Code != http.StatusOK {
		t.Fatalf("expected consume 200, got %d body=%s", consume.Code, consume.Body.String())
	}
	var consumed agentGuardEvaluateResponse
	decodeBody(t, consume.Body.Bytes(), &consumed)
	if consumed.Decision != "allow" {
		t.Fatalf("expected explicit ticket retry to allow once, got %+v", consumed)
	}

	afterConsume := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if afterConsume.Code != http.StatusOK {
		t.Fatalf("expected after-consume retry 200, got %d body=%s", afterConsume.Code, afterConsume.Body.String())
	}
	var fresh agentGuardEvaluateResponse
	decodeBody(t, afterConsume.Body.Bytes(), &fresh)
	if fresh.Decision != "deny_with_ticket" || fresh.ApprovalID == "" || fresh.ApprovalID == first.ApprovalID {
		t.Fatalf("expected consumed high-risk fingerprint to create a fresh approval, got %+v", fresh)
	}
	approvals, err := st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("expected high-risk repeat to create a fresh approval, got %+v", approvals)
	}
}

func TestAgentGuardLowRiskApprovedFingerprintExpiresBeforeReuse(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	reqCtx := agentGuardRequestContext(t, srv, st, workspace)
	req := agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          filepath.Join(t.TempDir(), "expired-memory.ps1"),
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}
	fingerprint := agentGuardFingerprintForRequest(t, reqCtx, req)
	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:         workspace.ID,
		ToolKey:             agentGuardEvaluateToolKey,
		ToolDisplayName:     "Agent Guard Evaluate",
		RequestedBy:         approvalRequestedBy(reqCtx.User.Name, reqCtx.Identity.Subject),
		Reason:              "expired remembered approval",
		Fingerprint:         fingerprint,
		Adapter:             req.Adapter,
		ActionType:          req.ActionType,
		Target:              req.Target,
		CanonicalTarget:     normalizeAgentGuardTarget(req.Target),
		ContentEncoding:     req.ContentEncoding,
		ContentHash:         hashAgentGuardPayload(req.Content),
		ScriptHash:          hashAgentGuardPayload(req.Content),
		DecisionPayloadJSON: json.RawMessage(`{"target":"` + req.Target + `"}`),
		TTL:                 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create expiring approval: %v", err)
	}
	if _, err := st.TransitionApprovalRequest(context.Background(), workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	}); err != nil {
		t.Fatalf("approve expiring approval: %v", err)
	}
	callTool, err := st.GetToolByKey(context.Background(), workspace.ID, agentGuardEvaluateToolKey)
	if err != nil {
		t.Fatalf("get agent guard tool: %v", err)
	}
	if _, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-expiring-memory",
		ActorID:            reqCtx.User.ID,
		ActorSubject:       reqCtx.Identity.Subject,
		ActorEmail:         reqCtx.Identity.Email,
		ActorName:          reqCtx.Identity.Name,
		ToolID:             callTool.ID,
		ToolKey:            callTool.Key(),
		Status:             "approval_required",
		RiskLevel:          "medium",
		PolicyDecision:     policyRequireApproval,
		ApprovalID:         approval.ID,
		InputRedactedJSON:  json.RawMessage(`{}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		TraceID:            "trace-expiring-memory",
	}); err != nil {
		t.Fatalf("create approval call: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, req))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision == "allow" {
		t.Fatalf("expired remembered approval must not allow, got %+v", response)
	}
}

func TestAgentGuardDirectAllowDoesNotPersistRawExecutionInput(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", `{
		"adapter":"claude",
		"tool":"Read",
		"actionType":"read",
		"target":"workspace/readme.md",
		"isScript":false,
		"contentEncoding":"plain",
		"content":"plain-read"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "allow" {
		t.Fatalf("expected allow, got %+v", response)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(calls))
	}
	if calls[0].Status != "success" {
		t.Fatalf("expected direct allow to be stored as success audit, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("direct allow must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
	}
	if containsJSONSecret(t, calls[0].InputRedactedJSON, "plain-read") {
		t.Fatalf("direct allow audit leaked raw input: %s", calls[0].InputRedactedJSON)
	}
}

func TestAgentGuardSafeWorkspaceEditAllowsWithoutApproval(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", `{
		"adapter":"claude",
		"tool":"Write",
		"actionType":"write",
		"target":"workspace/notes.txt",
		"isScript":false,
		"contentEncoding":"plain",
		"content":"hello world"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "allow" || response.ApprovalID != "" {
		t.Fatalf("expected safe workspace edit to allow without approval, got %+v", response)
	}
	if response.Explanation == nil {
		t.Fatalf("expected safe workspace edit explanation, got %+v", response)
	}
	if response.Explanation.TargetCategory != "workspace" {
		t.Fatalf("expected workspace target category, got %+v", response.Explanation)
	}
	if response.Explanation.RiskLevel == "high" || response.Explanation.RiskLevel == "critical" {
		t.Fatalf("safe workspace write must not be high risk, got %+v", response.Explanation)
	}
	if response.Explanation.MatchedRule != "agent-guard-safe-workspace-write-allow" {
		t.Fatalf("expected safe workspace matched rule, got %+v", response.Explanation)
	}
	if !agentGuardExplanationHasSignal(response.Explanation, "Workspace-scoped write") {
		t.Fatalf("expected workspace signal, got %+v", response.Explanation)
	}
	for _, forbidden := range []string{
		"Windows Startup persistence path",
		"PowerShell ExecutionPolicy Bypass",
		"PowerShell hidden window execution",
		"High-risk local action",
	} {
		if agentGuardExplanationHasSignal(response.Explanation, forbidden) {
			t.Fatalf("safe workspace explanation must not contain high-risk signal %q, got %+v", forbidden, response.Explanation)
		}
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(calls))
	}
	if calls[0].Status != "success" {
		t.Fatalf("expected safe edit to be stored as success, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("safe edit must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
	}
}

func TestAgentGuardSafeWorkspaceExecAllowsWithoutApproval(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", `{
		"adapter":"codex",
		"tool":"Bash",
		"actionType":"exec",
		"target":"git status",
		"isScript":false,
		"contentEncoding":"plain",
		"content":"git status --short"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "allow" || response.ApprovalID != "" {
		t.Fatalf("expected safe workspace exec to allow without approval, got %+v", response)
	}
	if response.Explanation == nil {
		t.Fatalf("expected safe workspace exec explanation, got %+v", response)
	}
	if response.Explanation.TargetCategory != "workspace" || response.Explanation.RiskLevel != "medium" {
		t.Fatalf("unexpected safe exec explanation: %+v", response.Explanation)
	}
	if response.Explanation.MatchedRule != "agent-guard-safe-workspace-exec-allow" {
		t.Fatalf("expected safe workspace exec matched rule, got %+v", response.Explanation)
	}
	if agentGuardExplanationHasSignal(response.Explanation, "High-risk local action") {
		t.Fatalf("safe workspace exec must not include high-risk signal, got %+v", response.Explanation)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 || calls[0].Status != "success" || calls[0].PolicyDecision != "allow" {
		t.Fatalf("expected safe exec success audit, got %+v", calls)
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("safe exec must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
	}
}

func TestAgentGuardPosixAbsolutePathIsExternalAndRequiresApproval(t *testing.T) {
	t.Parallel()

	target := "/tmp/agenttoolgate/drop.ps1"
	if got := classifyAgentGuardTargetCategory(target); got == "workspace" {
		t.Fatalf("expected POSIX absolute path to avoid workspace classification, got %q", got)
	}

	srv, _, _ := newGovernanceTestApp(t)
	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision != "deny_with_ticket" || response.ApprovalID == "" {
		t.Fatalf("expected POSIX absolute script write to require approval, got %+v", response)
	}
}

func TestAgentGuardSensitiveStartupExplanationIncludesSignals(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	target := agentGuardSensitiveStartupTarget(t)

	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "powershell -ExecutionPolicy Bypass -WindowStyle Hidden -File payload.ps1",
	}))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision != "deny_with_ticket" {
		t.Fatalf("expected sensitive startup script to require approval, got %+v", response)
	}
	if response.Explanation == nil {
		t.Fatalf("expected explanation for sensitive startup script, got %+v", response)
	}
	if response.Explanation.TargetCategory != "sensitive" {
		t.Fatalf("expected sensitive target category, got %+v", response.Explanation)
	}
	if response.Explanation.RiskLevel != "high" {
		t.Fatalf("expected high risk level, got %+v", response.Explanation)
	}
	if response.Explanation.MatchedRule != "agent-guard-sensitive-target-requires-approval" {
		t.Fatalf("expected sensitive target matched rule, got %+v", response.Explanation)
	}
	for _, signal := range []string{
		"Windows Startup persistence path",
		"PowerShell ExecutionPolicy Bypass",
		"PowerShell hidden window execution",
		"High-risk local action",
	} {
		if !agentGuardExplanationHasSignal(response.Explanation, signal) {
			t.Fatalf("expected signal %q, got %+v", signal, response.Explanation)
		}
	}
}

func TestAgentGuardToolCallAPIsIncludeExplanation(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	target := agentGuardSensitiveStartupTarget(t)

	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "powershell -ExecutionPolicy Bypass -WindowStyle Hidden -File payload.ps1",
	}))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var evaluateResponse agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &evaluateResponse)
	if evaluateResponse.Decision != "deny_with_ticket" || evaluateResponse.CallID == "" || evaluateResponse.ApprovalID == "" {
		t.Fatalf("expected deny_with_ticket with audit row, got %+v", evaluateResponse)
	}
	if evaluateResponse.Explanation == nil {
		t.Fatalf("expected evaluate response explanation, got %+v", evaluateResponse)
	}

	detailResp := getJSON(t, srv, "/api/tool-calls/"+evaluateResponse.CallID)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("expected detail 200, got %d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detailCall model.ToolCall
	decodeBody(t, detailResp.Body.Bytes(), &detailCall)
	if detailCall.Explanation == nil {
		t.Fatalf("expected tool call detail explanation, got %+v", detailCall)
	}
	if detailCall.Explanation.TargetCategory != "sensitive" || detailCall.Explanation.RiskLevel != "high" {
		t.Fatalf("unexpected tool call detail explanation: %+v", detailCall.Explanation)
	}
	if detailCall.Explanation.MatchedRule != "agent-guard-sensitive-target-requires-approval" {
		t.Fatalf("unexpected matched rule in detail explanation: %+v", detailCall.Explanation)
	}

	listResp := getJSON(t, srv, "/api/tool-calls?status=approval_required&page=1&pageSize=10")
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d body=%s", listResp.Code, listResp.Body.String())
	}
	var page model.ToolCallPage
	decodeBody(t, listResp.Body.Bytes(), &page)
	if len(page.Items) == 0 {
		t.Fatalf("expected tool call in list response, got %+v", page)
	}
	if page.Items[0].Explanation == nil || page.Items[0].Explanation.MatchedRule != "agent-guard-sensitive-target-requires-approval" {
		t.Fatalf("expected explanation on paged tool call item, got %+v", page.Items[0])
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+evaluateResponse.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var approveAction approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &approveAction)
	if approveAction.ToolCall.Explanation == nil {
		t.Fatalf("expected approval action toolCall explanation, got %+v", approveAction.ToolCall)
	}
	if approveAction.ToolCall.Explanation.TargetCategory != "sensitive" || approveAction.ToolCall.Explanation.RiskLevel != "high" {
		t.Fatalf("unexpected approval action explanation: %+v", approveAction.ToolCall.Explanation)
	}
}

func TestAgentGuardInvalidTicketDenyIncludesExplanation(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	target := agentGuardSensitiveStartupTarget(t)

	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
		TicketID:        "approval_invalid",
	}))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision != "deny" || response.Reason != "invalid ticket" {
		t.Fatalf("expected invalid ticket deny, got %+v", response)
	}
	if response.Explanation == nil {
		t.Fatalf("expected explanation for invalid ticket deny, got %+v", response)
	}
	if strings.TrimSpace(response.Explanation.TargetCategory) == "" ||
		strings.TrimSpace(response.Explanation.RiskLevel) == "" ||
		strings.TrimSpace(response.Explanation.MatchedRule) == "" ||
		len(response.Explanation.Signals) == 0 {
		t.Fatalf("expected non-empty invalid ticket explanation, got %+v", response.Explanation)
	}
}

func TestAgentGuardRejectClearsRawExecutionInput(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := filepath.Join(t.TempDir(), "drop.ps1")

	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'secret-token'",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "deny_with_ticket" {
		t.Fatalf("expected ticketed deny, got %+v", response)
	}

	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", "")
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected reject 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(calls))
	}
	if calls[0].Status != "rejected" {
		t.Fatalf("expected reject to finalize audit row as rejected, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("reject must clear raw execution input, got %s", calls[0].InputExecutionJSON)
	}
}

func TestAgentGuardInternalToolIsHiddenFromToolList(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)

	rec := getJSON(t, srv, "/api/tools")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []model.Tool `json:"items"`
	}
	decodeBody(t, rec.Body.Bytes(), &payload)
	for _, tool := range payload.Items {
		if tool.Key() == "agent_guard.evaluate" {
			t.Fatalf("internal agent guard tool must not be exposed in /api/tools")
		}
	}
}

func TestAgentGuardConcurrentTicketRetryConsumesOnce(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)

	req := agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          filepath.Join(t.TempDir(), "run.ps1"),
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}

	first := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, req))
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", first.Code, first.Body.String())
	}
	var initial agentGuardEvaluateResponse
	decodeBody(t, first.Body.Bytes(), &initial)
	if initial.Decision != "deny_with_ticket" || initial.ApprovalID == "" {
		t.Fatalf("expected pending ticket, got %+v", initial)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+initial.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	retryReq := req
	retryReq.TicketID = initial.ApprovalID
	retryBody := mustAgentGuardRequestBody(t, retryReq)

	gated := newGatedApprovalStore(st, 2)
	srv.store = gated

	results := make(chan agentGuardEvaluateResponse, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/agent-guard/evaluate", strings.NewReader(retryBody))
			resp := httptest.NewRecorder()
			srv.Router().ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				errs <- fmt.Errorf("expected retry 200, got %d body=%s", resp.Code, resp.Body.String())
				return
			}
			var decoded agentGuardEvaluateResponse
			if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
				errs <- err
				return
			}
			results <- decoded
		}()
	}
	gated.waitForTransitions(t)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("%v", err)
		}
	}

	allowCount := 0
	denyCount := 0
	for result := range results {
		switch result.Decision {
		case "allow":
			allowCount++
		case "deny":
			denyCount++
		default:
			t.Fatalf("unexpected retry decision: %+v", result)
		}
	}
	if allowCount != 1 || denyCount != 1 {
		t.Fatalf("expected one allow and one deny, got allow=%d deny=%d", allowCount, denyCount)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 audit rows, got %d", len(calls))
	}
	var successCount, deniedCount int
	for _, call := range calls {
		switch call.Status {
		case "success":
			successCount++
		case "denied":
			deniedCount++
		}
	}
	if successCount != 1 || deniedCount != 1 {
		t.Fatalf("expected one success and one denied audit row, got success=%d denied=%d calls=%+v", successCount, deniedCount, calls)
	}
}

func TestAgentGuardExpiredTicketDeniesRetry(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	reqCtx := agentGuardRequestContext(t, srv, st, workspace)
	req := agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          filepath.Join(t.TempDir(), "expired.ps1"),
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'secret-token'",
	}

	fingerprint := agentGuardFingerprintForRequest(t, reqCtx, req)
	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:         workspace.ID,
		ToolKey:             agentGuardEvaluateToolKey,
		ToolDisplayName:     "Agent Guard Evaluate",
		RequestedBy:         approvalRequestedBy(reqCtx.User.Name, reqCtx.Identity.Subject),
		Reason:              "expired ticket",
		Fingerprint:         fingerprint,
		Adapter:             req.Adapter,
		ActionType:          req.ActionType,
		Target:              req.Target,
		CanonicalTarget:     normalizeAgentGuardTarget(req.Target),
		ContentEncoding:     req.ContentEncoding,
		ContentHash:         hashAgentGuardPayload(req.Content),
		ScriptHash:          hashAgentGuardPayload(req.Content),
		DecisionPayloadJSON: json.RawMessage(`{"target":"` + req.Target + `"}`),
		TTL:                 -time.Minute,
	})
	if err != nil {
		t.Fatalf("create expired approval: %v", err)
	}

	retryReq := req
	retryReq.TicketID = approval.ID
	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, retryReq))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision != "deny" {
		t.Fatalf("expected expired ticket to deny retry, got %+v", response)
	}

	got, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, approval.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != "expired" {
		t.Fatalf("expected approval to stay expired, got %+v", got)
	}
}

func TestAgentGuardDifferentTargetOrContentDoesNotReuseTicket(t *testing.T) {
	t.Parallel()

	otherTarget := filepath.Join(t.TempDir(), "other-target.ps1")
	cases := []struct {
		name   string
		mutate func(*agentGuardEvaluateRequest)
	}{
		{
			name: "different target",
			mutate: func(req *agentGuardEvaluateRequest) {
				req.Target = otherTarget
			},
		},
		{
			name: "different content",
			mutate: func(req *agentGuardEvaluateRequest) {
				req.Content = "Write-Host 'different'"
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newGovernanceTestApp(t)
			baseReq := agentGuardEvaluateRequest{
				Adapter:         "codex",
				Tool:            "Write",
				ActionType:      "write",
				Target:          filepath.Join(t.TempDir(), "base.ps1"),
				IsScript:        true,
				ContentEncoding: "plain",
				Content:         "Write-Host 'hello'",
			}

			first := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, baseReq))
			if first.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", first.Code, first.Body.String())
			}
			var initial agentGuardEvaluateResponse
			decodeBody(t, first.Body.Bytes(), &initial)
			if initial.Decision != "deny_with_ticket" || initial.ApprovalID == "" {
				t.Fatalf("expected pending ticket, got %+v", initial)
			}

			retryReq := baseReq
			retryReq.TicketID = initial.ApprovalID
			tc.mutate(&retryReq)

			second := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, retryReq))
			if second.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", second.Code, second.Body.String())
			}
			var denied agentGuardEvaluateResponse
			decodeBody(t, second.Body.Bytes(), &denied)
			if denied.Decision != "deny" {
				t.Fatalf("expected reused ticket to deny, got %+v", denied)
			}

			approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, initial.ApprovalID)
			if err != nil {
				t.Fatalf("get approval: %v", err)
			}
			if approval.Status != "pending" {
				t.Fatalf("fingerprint mismatch must leave approval pending, got %+v", approval)
			}
		})
	}
}

func TestAgentGuardEquivalentPathsReuseApprovedTicket(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	tempDir := t.TempDir()
	source := filepath.Join(tempDir, "payload.ps1")
	if err := os.WriteFile(source, []byte("Write-Host 'hello'"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	hardlink := filepath.Join(tempDir, "payload-link.ps1")
	if err := os.Link(source, hardlink); err != nil {
		t.Fatalf("create hardlink: %v", err)
	}

	baseReq := agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          source,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}
	first := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, baseReq))
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", first.Code, first.Body.String())
	}
	var initial agentGuardEvaluateResponse
	decodeBody(t, first.Body.Bytes(), &initial)
	if initial.Decision != "deny_with_ticket" || initial.ApprovalID == "" {
		t.Fatalf("expected pending ticket, got %+v", initial)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+initial.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	retryReq := baseReq
	retryReq.Target = hardlink
	retryReq.TicketID = initial.ApprovalID
	retry := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, retryReq))
	if retry.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", retry.Code, retry.Body.String())
	}
	var allowed agentGuardEvaluateResponse
	decodeBody(t, retry.Body.Bytes(), &allowed)
	if allowed.Decision != "allow" || allowed.ApprovalID != initial.ApprovalID {
		t.Fatalf("expected equivalent path to reuse approved ticket, got %+v", allowed)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected audit rows, got none")
	}
}

func TestAgentGuardHookAdaptersTranslateDecisions(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}

	repoRoot := agentGuardRepoRoot(t)
	inputJSON := mustAgentGuardHookInput(t, repoRoot, "Write", `{"path":"C:\\Users\\demo\\AppData\\Roaming\\Microsoft\\Windows\\Start Menu\\Programs\\Startup\\run.ps1","content":"Write-Host 'secret-token'"}`)

	t.Run("codex denies deny_with_ticket and never asks", func(t *testing.T) {
		t.Parallel()

		server := newAgentGuardDecisionServer(t, `{"decision":"deny_with_ticket","reason":"approval required","approvalId":"approval-1","approvalStatus":"pending","callId":"call-1","fingerprint":"fp-1"}`)
		defer server.Close()

		output := runAgentGuardHook(t, python, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), server.URL, inputJSON)
		got := output.HookSpecificOutput.PermissionDecision
		if got != "deny" {
			t.Fatalf("expected codex to deny deny_with_ticket, got %q output=%+v", got, output)
		}
		if got == "ask" {
			t.Fatalf("codex hook must never ask, output=%+v", output)
		}
	})

	t.Run("claude asks on deny_with_ticket", func(t *testing.T) {
		t.Parallel()

		server := newAgentGuardDecisionServer(t, `{"decision":"deny_with_ticket","reason":"approval required","approvalId":"approval-1","approvalStatus":"pending","callId":"call-1","fingerprint":"fp-1"}`)
		defer server.Close()

		output := runAgentGuardHook(t, python, filepath.Join(repoRoot, ".claude", "hooks", "agent-guard-pretool.py"), server.URL, inputJSON)
		if got := output.HookSpecificOutput.PermissionDecision; got != "ask" {
			t.Fatalf("expected claude to ask on deny_with_ticket, got %q output=%+v", got, output)
		}
	})

	for _, tc := range []struct {
		name       string
		body       string
		want       string
		expectNoop bool
	}{
		{name: "allow", body: `{"decision":"allow","reason":"allowed"}`, expectNoop: true},
		{name: "deny", body: `{"decision":"deny","reason":"denied"}`, want: "deny"},
		{name: "deny_with_ticket", body: `{"decision":"deny_with_ticket","reason":"approval required"}`, want: "deny"},
		{name: "invalid", body: `not-json`, want: "deny"},
		{name: "empty", body: ``, want: "deny"},
	} {
		tc := tc
		t.Run("codex-"+tc.name, func(t *testing.T) {
			t.Parallel()

			server := newAgentGuardDecisionServer(t, tc.body)
			defer server.Close()

			raw := runAgentGuardHookRaw(t, python, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), server.URL, inputJSON)
			if tc.expectNoop {
				if strings.TrimSpace(raw) != "" {
					t.Fatalf("expected codex allow to be no-op, got %s", raw)
				}
				return
			}
			output := decodeAgentGuardHookOutput(t, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), raw, "")
			if got := output.HookSpecificOutput.PermissionDecision; got != tc.want {
				t.Fatalf("expected codex permissionDecision %q, got %q output=%+v", tc.want, got, output)
			}
			if got := output.HookSpecificOutput.PermissionDecision; got != "deny" {
				t.Fatalf("codex hook must only emit deny or no-op, got %q output=%+v", got, output)
			}
		})
	}
}

func TestAgentGuardHookOfflineHighRiskDeniesAndWorkspaceAllows(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}

	repoRoot := agentGuardRepoRoot(t)
	unreachableURL := "http://127.0.0.1:1"

	cases := []struct {
		name     string
		toolJSON string
		want     string
		reason   string
	}{
		{
			name:     "high risk startup target denies offline",
			toolJSON: `{"path":"C:\\Users\\aki\\AppData\\Roaming\\Microsoft\\Windows\\Start Menu\\Programs\\Startup\\run.ps1","content":"Write-Host 'hello'"}`,
			want:     "deny",
			reason:   "ATG offline, sensitive target denied",
		},
		{
			name:     "relative claude hook denies offline",
			toolJSON: `{"path":".claude\\hooks\\agent-guard-pretool.py","content":"print('tamper')"}`,
			want:     "deny",
			reason:   "ATG offline, sensitive target denied",
		},
		{
			name:     "relative policy config denies offline",
			toolJSON: `{"path":"configs\\policies.yaml","content":"rules: []"}`,
			want:     "deny",
			reason:   "ATG offline, sensitive target denied",
		},
		{
			name:     "relative git hook denies offline",
			toolJSON: `{"path":".git\\hooks\\pre-commit","content":"#!/bin/sh"}`,
			want:     "deny",
			reason:   "ATG offline, sensitive target denied",
		},
		{
			name:     "relative ssh key denies offline",
			toolJSON: `{"path":".ssh\\id_rsa","content":"private key"}`,
			want:     "deny",
			reason:   "ATG offline, sensitive target denied",
		},
		{
			name:     "workspace target allows offline",
			toolJSON: `{"path":"workspace\\notes.md","content":"hello world"}`,
			want:     "allow",
			reason:   "ATG offline, local pending audit",
		},
		{
			name:     "workspace script allows offline",
			toolJSON: `{"path":"workspace\\script.ps1","content":"Write-Host 'hello'"}`,
			want:     "allow",
			reason:   "ATG offline, local pending audit",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inputJSON := mustAgentGuardHookInput(t, repoRoot, "Write", tc.toolJSON)
			raw := runAgentGuardHookRaw(t, python, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), unreachableURL, inputJSON)
			if tc.want == "allow" {
				if strings.TrimSpace(raw) != "" {
					t.Fatalf("expected codex offline allow to be no-op, got %s", raw)
				}
			} else {
				output := decodeAgentGuardHookOutput(t, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), raw, "")
				if got := output.HookSpecificOutput.PermissionDecision; got != tc.want {
					t.Fatalf("expected codex decision %q, got %q output=%+v", tc.want, got, output)
				}
				if got := output.HookSpecificOutput.PermissionDecisionReason; !strings.Contains(strings.ToLower(got), strings.ToLower(tc.reason)) {
					t.Fatalf("expected offline reason %q, got %q", tc.reason, got)
				}
				if output.HookSpecificOutput.PermissionDecision != "deny" {
					t.Fatalf("codex hook must only emit deny or no-op, got %q output=%+v", output.HookSpecificOutput.PermissionDecision, output)
				}
			}

			claudeOutput := runAgentGuardHook(t, python, filepath.Join(repoRoot, ".claude", "hooks", "agent-guard-pretool.py"), unreachableURL, inputJSON)
			if got := claudeOutput.HookSpecificOutput.PermissionDecision; got != tc.want {
				t.Fatalf("expected claude decision %q, got %q output=%+v", tc.want, got, claudeOutput)
			}
			if got := claudeOutput.HookSpecificOutput.PermissionDecisionReason; !strings.Contains(strings.ToLower(got), strings.ToLower(tc.reason)) {
				t.Fatalf("expected claude offline reason %q, got %q", tc.reason, got)
			}
		})
	}
}

func TestAgentGuardHookTimeoutBudget(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}

	repoRoot := agentGuardRepoRoot(t)
	scripts := []string{
		filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"),
		filepath.Join(repoRoot, ".claude", "hooks", "agent-guard-pretool.py"),
	}
	for _, script := range scripts {
		script := script
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(script))), func(t *testing.T) {
			t.Parallel()

			code := `
import importlib.util
import json
import os
import sys

spec = importlib.util.spec_from_file_location("agent_guard_hook", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

def read_timeout(value):
    if value is None:
        os.environ.pop("AGENTTOOLGATE_HOOK_TIMEOUT_MS", None)
    else:
        os.environ["AGENTTOOLGATE_HOOK_TIMEOUT_MS"] = value
    return module.hook_timeout_seconds()

print(json.dumps({
    "default": read_timeout(None),
    "override": read_timeout("750"),
    "invalid": read_timeout("bad"),
    "too_small": read_timeout("20"),
    "too_large": read_timeout("3000"),
}))
`
			cmd := exec.Command(python, "-c", code, script)
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(), "AGENTTOOLGATE_HOOK_TIMEOUT_MS=")
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("run timeout helper: %v stderr=%s", err, stderr.String())
			}
			var result map[string]float64
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("decode timeout helper output: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			if got := result["default"]; got != 0.2 {
				t.Fatalf("expected default timeout 0.2s, got %v", got)
			}
			if got := result["override"]; got != 0.75 {
				t.Fatalf("expected env override 0.75s, got %v", got)
			}
			if got := result["invalid"]; got != 0.2 {
				t.Fatalf("expected invalid env to fall back to 0.2s, got %v", got)
			}
			if got := result["too_small"]; got != 0.2 {
				t.Fatalf("expected too-small env to fall back to 0.2s, got %v", got)
			}
			if got := result["too_large"]; got != 0.2 {
				t.Fatalf("expected too-large env to fall back to 0.2s, got %v", got)
			}
		})
	}
}

func TestAgentGuardHookReadsScriptFileContentsWhenExecutingScriptFile(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}

	repoRoot := agentGuardRepoRoot(t)
	scriptPath := filepath.Join(t.TempDir(), "payload.sh")
	scriptContent := "powershell -ExecutionPolicy Bypass -WindowStyle Hidden"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o600); err != nil {
		t.Fatalf("write script file: %v", err)
	}

	captured := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode hook payload: %v", err)
		}
		captured <- payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"allow","reason":"allowed"}`))
	}))
	t.Cleanup(server.Close)

	commandPayload, err := json.Marshal(map[string]any{
		"command": "bash " + scriptPath,
	})
	if err != nil {
		t.Fatalf("marshal command payload: %v", err)
	}
	inputJSON := mustAgentGuardHookInput(t, repoRoot, "Bash", string(commandPayload))
	raw := runAgentGuardHookRaw(t, python, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), server.URL, inputJSON)
	if strings.TrimSpace(raw) != "" {
		t.Fatalf("expected codex allow to be no-op, got %s", raw)
	}

	select {
	case payload := <-captured:
		content, _ := payload["content"].(string)
		if content != scriptContent {
			t.Fatalf("expected hook to read script file content, got %q", content)
		}
		if target, _ := payload["target"].(string); !strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(scriptPath)) {
			t.Fatalf("expected inferred target to be script path, got %q", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for captured hook payload")
	}
}

func TestAgentGuardSensitivePathWithoutFileIdentityDenies(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := agentGuardSensitiveStartupTarget(t)
	srv.agentGuardResolveTarget = func(raw string) agentGuardTargetResolution {
		return agentGuardTargetResolution{
			CanonicalTarget:      normalizeAgentGuardTarget(raw),
			TargetExists:         true,
			TargetIdentityStable: false,
			ParentIdentity:       normalizeAgentGuardTarget(filepath.Dir(raw)),
		}
	}
	resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response agentGuardEvaluateResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Decision != "deny" {
		t.Fatalf("expected sensitive path without identity to deny, got %+v", response)
	}
	if !strings.Contains(strings.ToLower(response.Reason), "sensitive path without stable file identity") {
		t.Fatalf("expected explicit denial reason, got %+v", response)
	}

	approvals, err := st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("expected no approval ticket for missing identity, got %+v", approvals)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one denied call, got %d", len(calls))
	}
	if calls[0].Status != "denied" || calls[0].ErrorMessage == "" {
		t.Fatalf("expected denied audit row, got %+v", calls[0])
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected metrics 200, got %d body=%s", metricsRec.Code, metricsRec.Body.String())
	}
	if !matchesAgentGuardRuleMetric(metricsRec.Body.String(), "agent-guard-sensitive-target-requires-approval", "denied") {
		t.Fatalf("expected denied rule metric for sensitive target, got %s", metricsRec.Body.String())
	}
}

func TestAgentGuardResolvedSensitiveTargetBypassesSafeWorkspaceAllow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		resolution func(raw string) agentGuardTargetResolution
	}{
		{
			name: "resolved target points to startup",
			resolution: func(raw string) agentGuardTargetResolution {
				resolved := `C:\Users\demo\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1`
				return agentGuardTargetResolution{
					ResolvedPath:         resolved,
					CanonicalTarget:      "fileid:startup-run",
					ResolvedFileIdentity: "startup-run",
					ResolvedParentPath:   filepath.Dir(resolved),
					ParentIdentity:       "fileid:startup-parent",
					TargetExists:         true,
					TargetIdentityStable: true,
				}
			},
		},
		{
			name: "resolved parent points to codex hooks",
			resolution: func(raw string) agentGuardTargetResolution {
				resolvedParent := `C:\Users\demo\repo\.codex\hooks`
				return agentGuardTargetResolution{
					CanonicalTarget:      normalizeAgentGuardTarget(raw),
					ResolvedParentPath:   resolvedParent,
					ParentIdentity:       "fileid:codex-hooks-parent",
					TargetExists:         false,
					TargetIdentityStable: false,
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, _, _ := newGovernanceTestApp(t)
			srv.agentGuardResolveTarget = tc.resolution

			resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
				Adapter:         "codex",
				Tool:            "Write",
				ActionType:      "write",
				Target:          `workspace\linked\run.ps1`,
				IsScript:        true,
				ContentEncoding: "plain",
				Content:         "Write-Host 'hello'",
			}))
			if resp.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
			}

			var response agentGuardEvaluateResponse
			decodeBody(t, resp.Body.Bytes(), &response)
			if response.Decision == "allow" {
				t.Fatalf("resolved sensitive target must not match safe workspace allow, got %+v", response)
			}
			if response.Decision != "deny_with_ticket" && response.Decision != "deny" {
				t.Fatalf("expected resolved sensitive target to require approval or deny, got %+v", response)
			}
		})
	}
}

func TestAgentGuardSensitiveNewFileApproveRetryDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	target := agentGuardMissingSensitiveStartupTarget(t)

	first := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
	}))
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", first.Code, first.Body.String())
	}

	var initial agentGuardEvaluateResponse
	decodeBody(t, first.Body.Bytes(), &initial)
	if initial.Decision != "deny_with_ticket" || initial.ApprovalID == "" {
		t.Fatalf("expected sensitive new file to require approval, got %+v", initial)
	}

	approvals, err := st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].Status != "pending" {
		t.Fatalf("expected one pending approval, got %+v", approvals)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+initial.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	retry := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Write",
		ActionType:      "write",
		Target:          target,
		IsScript:        true,
		ContentEncoding: "plain",
		Content:         "Write-Host 'hello'",
		TicketID:        initial.ApprovalID,
	}))
	if retry.Code != http.StatusOK {
		t.Fatalf("expected retry 200, got %d body=%s", retry.Code, retry.Body.String())
	}

	var allowed agentGuardEvaluateResponse
	decodeBody(t, retry.Body.Bytes(), &allowed)
	if allowed.Decision != "allow" || allowed.ApprovalID != initial.ApprovalID {
		t.Fatalf("expected approved new file retry to allow, got %+v", allowed)
	}

	approvals, err = st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals after retry: %v", err)
	}
	if len(approvals) != 1 || approvals[0].Status != "consumed" {
		t.Fatalf("expected approval to be consumed after retry, got %+v", approvals)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(calls))
	}
	if calls[0].Status != "success" || calls[0].ApprovalStatus != "consumed" {
		t.Fatalf("expected retry to finalize as consumed success, got %+v", calls[0])
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("consumed retry must clear raw execution input, got %s", calls[0].InputExecutionJSON)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected metrics 200, got %d body=%s", metricsRec.Code, metricsRec.Body.String())
	}
	if !matchesAgentGuardRuleMetric(metricsRec.Body.String(), "agent-guard-sensitive-target-requires-approval", "approval_required") {
		t.Fatalf("expected approval rule metric, got %s", metricsRec.Body.String())
	}
	if !matchesAgentGuardRuleMetric(metricsRec.Body.String(), "agent-guard-sensitive-target-requires-approval", "retry_allowed") {
		t.Fatalf("expected approved retry rule metric, got %s", metricsRec.Body.String())
	}
}

func TestAgentGuardScriptContentRaisesRiskIndependentOfTarget(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	safeTarget := "workspace/notes.ps1"

	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "bypass hidden",
			content: "powershell -ExecutionPolicy Bypass -WindowStyle Hidden -File payload.ps1",
		},
		{
			name:    "encoded payload",
			content: base64.StdEncoding.EncodeToString([]byte("powershell -ExecutionPolicy Bypass -WindowStyle Hidden")),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
				Adapter:         "codex",
				Tool:            "Write",
				ActionType:      "write",
				Target:          safeTarget,
				IsScript:        true,
				ContentEncoding: "plain",
				Content:         tc.content,
			}))
			if resp.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
			}

			var response agentGuardEvaluateResponse
			decodeBody(t, resp.Body.Bytes(), &response)
			if response.Decision != "deny_with_ticket" || response.ApprovalID == "" {
				t.Fatalf("expected malicious script content to require approval, got %+v", response)
			}
		})
	}
}

func TestAgentGuardHighRiskRulesDoNotRecommendDemote(t *testing.T) {
	t.Parallel()

	stats := telemetry.AgentGuardRuleFrictionStats{
		Triggers:        100,
		Approvals:       98,
		Denials:         2,
		ApprovedRetries: 40,
	}
	if telemetry.ShouldRecommendAgentGuardRuleDemote("high", stats) {
		t.Fatalf("high risk rule must not produce demote recommendation, stats=%+v", stats)
	}
	if telemetry.ShouldRecommendAgentGuardRuleDemote("critical", stats) {
		t.Fatalf("critical risk rule must not produce demote recommendation, stats=%+v", stats)
	}
}

func TestAgentGuardHighApprovalRateWithoutExplicitFalsePositiveDoesNotRecommendDemote(t *testing.T) {
	t.Parallel()

	stats := telemetry.AgentGuardRuleFrictionStats{
		Triggers:        100,
		Approvals:       99,
		Denials:         1,
		ApprovedRetries: 1,
	}
	if telemetry.ShouldRecommendAgentGuardRuleDemote("medium", stats) {
		t.Fatalf("approval rate alone must not recommend demote, stats=%+v", stats)
	}
}

func TestAgentGuardExplicitReviewSignalCanRecommendDemoteForNonHighRisk(t *testing.T) {
	t.Parallel()

	stats := telemetry.AgentGuardRuleFrictionStats{
		Triggers:             20,
		Approvals:            0,
		Denials:              0,
		ApprovedRetries:      0,
		FalsePositiveSignals: 1,
	}
	if !telemetry.ShouldRecommendAgentGuardRuleDemote("medium", stats) {
		t.Fatalf("explicit false-positive signal should recommend review for non-high risk, stats=%+v", stats)
	}
	if telemetry.ShouldRecommendAgentGuardRuleDemote("high", stats) {
		t.Fatalf("high risk rule must not recommend demote even with explicit review signal, stats=%+v", stats)
	}
}

func TestAgentGuardClassifiesSelfTamperTargetsAsSensitive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		target string
		want   string
	}{
		{target: `C:\workspace\.claude\settings.json`, want: "self_tamper"},
		{target: `C:\workspace\.codex\hooks.json`, want: "self_tamper"},
		{target: `C:\workspace\configs\policies.yaml`, want: "self_tamper"},
		{target: `C:\workspace\backend\cmd\server\main.go`, want: "self_tamper"},
		{target: `.claude/hooks/agent-guard-pretool.py`, want: "self_tamper"},
		{target: `.codex/hooks/agent-guard-pretool.py`, want: "self_tamper"},
		{target: `.claude/settings.json`, want: "self_tamper"},
		{target: `.codex/hooks.json`, want: "self_tamper"},
		{target: `configs/policies.yaml`, want: "self_tamper"},
		{target: `backend/cmd/server/main.go`, want: "self_tamper"},
		{target: `.ssh/id_rsa`, want: "sensitive"},
		{target: `.git/hooks/pre-commit`, want: "sensitive"},
	}
	for _, tc := range cases {
		if got := classifyAgentGuardTargetCategory(tc.target); got != tc.want {
			t.Fatalf("expected category %q for %q, got %q", tc.want, tc.target, got)
		}
	}
}

func TestAgentGuardClassifiesStartupSensitiveTargetsAcrossSeparators(t *testing.T) {
	t.Parallel()

	cases := []string{
		`/tmp/Users/demo/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/run.ps1`,
		`C:\Users\demo\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1`,
		`Users/demo/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/run.ps1`,
	}
	for _, target := range cases {
		if got := classifyAgentGuardTargetCategory(target); got != "sensitive" {
			t.Fatalf("expected startup path %q to classify as sensitive, got %q", target, got)
		}
	}
}

func TestAgentGuardRelativeSensitiveTargetsRequireApprovalAndDoNotRemember(t *testing.T) {
	t.Parallel()

	cases := []string{
		`.claude/hooks/agent-guard-pretool.py`,
		`.codex/hooks/agent-guard-pretool.py`,
		`.claude/settings.json`,
		`.codex/hooks.json`,
		`configs/policies.yaml`,
		`backend/cmd/server/main.go`,
		`.ssh/id_rsa`,
		`.git/hooks/pre-commit`,
	}

	for _, target := range cases {
		target := target
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			t.Parallel()

			srv, _, _ := newGovernanceTestApp(t)
			body := mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
				Adapter:         "codex",
				Tool:            "Write",
				ActionType:      "write",
				Target:          target,
				IsScript:        true,
				ContentEncoding: "plain",
				Content:         "Write-Host 'hello'",
			})

			first := postJSON(t, srv, "/api/agent-guard/evaluate", body)
			if first.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", first.Code, first.Body.String())
			}
			var initial agentGuardEvaluateResponse
			decodeBody(t, first.Body.Bytes(), &initial)
			if initial.Decision == "allow" {
				t.Fatalf("relative sensitive target must not allow without approval, target=%s response=%+v", target, initial)
			}
			if initial.Decision != "deny_with_ticket" {
				t.Fatalf("expected relative sensitive target to require approval, target=%s response=%+v", target, initial)
			}

			approveResp := postJSON(t, srv, "/api/approvals/"+initial.ApprovalID+"/approve", "")
			if approveResp.Code != http.StatusOK {
				t.Fatalf("expected approve 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
			}

			retryWithoutTicket := postJSON(t, srv, "/api/agent-guard/evaluate", body)
			if retryWithoutTicket.Code != http.StatusOK {
				t.Fatalf("expected retry 200, got %d body=%s", retryWithoutTicket.Code, retryWithoutTicket.Body.String())
			}
			var remembered agentGuardEvaluateResponse
			decodeBody(t, retryWithoutTicket.Body.Bytes(), &remembered)
			if remembered.Decision == "allow" {
				t.Fatalf("self_tamper/high-risk target must not enter approve-once memory allow, target=%s response=%+v", target, remembered)
			}
		})
	}
}

func TestAgentGuardOfflineAllowWritesLocalPendingAudit(t *testing.T) {
	t.Parallel()

	python, err := exec.LookPath("python")
	if err != nil {
		t.Skipf("python not available: %v", err)
	}

	repoRoot := agentGuardRepoRoot(t)
	auditPath := filepath.Join(repoRoot, ".tmp", "local-action-firewall", "pending-audit.jsonl")
	_ = os.Remove(auditPath)

	inputJSON := mustAgentGuardHookInput(t, repoRoot, "Write", `{"path":"workspace\\notes.md","content":"hello world"}`)
	raw := runAgentGuardHookRaw(t, python, filepath.Join(repoRoot, ".codex", "hooks", "agent-guard-pretool.py"), "http://127.0.0.1:1", inputJSON)
	if strings.TrimSpace(raw) != "" {
		t.Fatalf("expected offline workspace action to be codex no-op, got %s", raw)
	}

	content, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read pending audit: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected pending audit record, got %s", string(content))
	}
	found := false
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode pending audit record: %v content=%s", err, line)
		}
		target, _ := record["target"].(string)
		if target != "workspace\\notes.md" {
			continue
		}
		found = true
		if offline, ok := record["offline"].(bool); !ok || !offline {
			t.Fatalf("expected offline pending audit record, got %+v", record)
		}
		if workspace, ok := record["workspace"].(string); !ok || strings.TrimSpace(workspace) == "" {
			t.Fatalf("expected workspace in pending audit, got %+v", record)
		}
		if tool, ok := record["tool"].(string); !ok || tool != "Write" {
			t.Fatalf("expected tool in pending audit, got %+v", record)
		}
		break
	}
	if !found {
		t.Fatalf("expected pending audit record for workspace\\notes.md, got %s", string(content))
	}
}

func newAgentGuardDecisionServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
}

func runAgentGuardHook(t *testing.T, python, scriptPath, serverURL, inputJSON string) agentGuardHookOutput {
	t.Helper()
	raw := runAgentGuardHookRaw(t, python, scriptPath, serverURL, inputJSON)
	return decodeAgentGuardHookOutput(t, scriptPath, raw, "")
}

func runAgentGuardHookRaw(t *testing.T, python, scriptPath, serverURL, inputJSON string) string {
	t.Helper()
	agentGuardHookControlMu.Lock()
	defer agentGuardHookControlMu.Unlock()
	restoreControl := writeAgentGuardHookControlLive(t)
	defer restoreControl()

	cmd := exec.Command(python, scriptPath)
	cmd.Dir = agentGuardRepoRoot(t)
	cmd.Env = append(os.Environ(),
		"AGENTTOOLGATE_URL="+serverURL,
		"TRELLIS_HOOKS=1",
		"TRELLIS_DISABLE_HOOKS=0",
	)
	cmd.Stdin = strings.NewReader(inputJSON)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run hook script %s: %v stderr=%s", scriptPath, err, stderr.String())
	}
	return stdout.String()
}

func writeAgentGuardHookControlLive(t *testing.T) func() {
	t.Helper()

	controlPath := filepath.Join(agentGuardRepoRoot(t), ".tmp", "agenttoolgate", "hook-control.json")
	original, readErr := os.ReadFile(controlPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read hook control file: %v", readErr)
	}
	if err := os.MkdirAll(filepath.Dir(controlPath), 0o700); err != nil {
		t.Fatalf("create hook control dir: %v", err)
	}
	body := []byte(`{"mode":"live","reason":"test"}` + "\n")
	if err := os.WriteFile(controlPath, body, 0o600); err != nil {
		t.Fatalf("write hook control file: %v", err)
	}
	return func() {
		if readErr == nil {
			_ = os.WriteFile(controlPath, original, 0o600)
			return
		}
		_ = os.Remove(controlPath)
	}
}

func decodeAgentGuardHookOutput(t *testing.T, scriptPath, stdout, stderr string) agentGuardHookOutput {
	t.Helper()
	var output agentGuardHookOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode hook output %s: %v stdout=%s stderr=%s", scriptPath, err, stdout, stderr)
	}
	return output
}

func agentGuardRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func mustAgentGuardHookInput(t *testing.T, repoRoot, toolName, toolInputJSON string) string {
	t.Helper()

	var toolInput map[string]any
	if err := json.Unmarshal([]byte(toolInputJSON), &toolInput); err != nil {
		t.Fatalf("decode hook tool input: %v", err)
	}
	payload := map[string]any{
		"cwd":        repoRoot,
		"tool_name":  toolName,
		"tool_input": toolInput,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	return string(raw)
}

func agentGuardSensitiveStartupTarget(t *testing.T) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), "Users", "aki", "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "run.ps1")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create startup path: %v", err)
	}
	if err := os.WriteFile(target, []byte("Write-Host 'hello'"), 0o600); err != nil {
		t.Fatalf("write startup file: %v", err)
	}
	return target
}

func agentGuardMissingSensitiveStartupTarget(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "Users", "aki", "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "missing.ps1")
}

type agentGuardHookOutput struct {
	HookSpecificOutput struct {
		PermissionDecision       string         `json:"permissionDecision"`
		PermissionDecisionReason string         `json:"permissionDecisionReason"`
		UpdatedInput             map[string]any `json:"updatedInput"`
		HookEventName            string         `json:"hookEventName"`
	} `json:"hookSpecificOutput"`
}

type agentGuardExplanationResponse struct {
	TargetCategory string   `json:"targetCategory"`
	RiskLevel      string   `json:"riskLevel"`
	MatchedRule    string   `json:"matchedRule"`
	Signals        []string `json:"signals"`
}

type agentGuardEvaluateResponse struct {
	Decision       string                         `json:"decision"`
	Reason         string                         `json:"reason,omitempty"`
	ApprovalID     string                         `json:"approvalId,omitempty"`
	ApprovalStatus string                         `json:"approvalStatus,omitempty"`
	CallID         string                         `json:"callId,omitempty"`
	Fingerprint    string                         `json:"fingerprint,omitempty"`
	Explanation    *agentGuardExplanationResponse `json:"explanation,omitempty"`
}

func agentGuardExplanationHasSignal(explanation *agentGuardExplanationResponse, signal string) bool {
	if explanation == nil {
		return false
	}
	for _, candidate := range explanation.Signals {
		if strings.TrimSpace(candidate) == signal {
			return true
		}
	}
	return false
}

func containsJSONSecret(t *testing.T, raw json.RawMessage, secret string) bool {
	t.Helper()
	return secret != "" && strings.Contains(string(raw), secret)
}

func mustAgentGuardRequestBody(t *testing.T, req agentGuardEvaluateRequest) string {
	t.Helper()

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal agent guard request: %v", err)
	}
	return string(raw)
}

func agentGuardRequestContext(t *testing.T, srv *App, st store.Store, workspace model.Workspace) RequestContext {
	t.Helper()

	identity, err := srv.authenticator.Authenticate(context.Background(), "", workspace.ZitadelOrganizationID)
	if err != nil {
		t.Fatalf("authenticate request context: %v", err)
	}
	resolvedWorkspace, user, err := srv.authenticator.ResolvePrincipal(context.Background(), st, identity)
	if err != nil {
		t.Fatalf("resolve principal: %v", err)
	}
	return RequestContext{
		Identity:  identity,
		Workspace: resolvedWorkspace,
		User:      user,
	}
}

func agentGuardFingerprintForRequest(t *testing.T, reqCtx RequestContext, req agentGuardEvaluateRequest) string {
	t.Helper()

	normalizedTarget := normalizeAgentGuardTarget(req.Target)
	decodedContent, err := decodeAgentGuardContent(req.ContentEncoding, req.Content)
	if err != nil {
		t.Fatalf("decode content: %v", err)
	}
	contentHash := hashAgentGuardPayload(decodedContent)
	scriptHash := ""
	if req.IsScript {
		scriptHash = contentHash
	}
	resolution := resolveAgentGuardTarget(normalizedTarget)
	return hashAgentGuardPayload(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Adapter)),
		reqCtx.Workspace.ID,
		reqCtx.User.ID,
		reqCtx.Identity.Subject,
		strings.ToLower(strings.TrimSpace(req.Tool)),
		strings.ToLower(strings.TrimSpace(req.ActionType)),
		resolution.CanonicalTarget,
		resolution.ResolvedFileIdentity,
		resolution.ParentIdentity,
		contentHash,
		scriptHash,
	}, "\x00"))
}

func matchesAgentGuardRuleMetric(body, rule, outcome string) bool {
	quotedRule := regexp.QuoteMeta(rule)
	quotedOutcome := regexp.QuoteMeta(outcome)
	patterns := []string{
		fmt.Sprintf(`(?m)^agent_guard_rule_total\{[^}]*rule="%s"[^}]*outcome="%s"[^}]*\} [0-9.]+$`, quotedRule, quotedOutcome),
		fmt.Sprintf(`(?m)^agent_guard_rule_total\{[^}]*outcome="%s"[^}]*rule="%s"[^}]*\} [0-9.]+$`, quotedOutcome, quotedRule),
	}
	for _, pattern := range patterns {
		if regexp.MustCompile(pattern).MatchString(body) {
			return true
		}
	}
	return false
}

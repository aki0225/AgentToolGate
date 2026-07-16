package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestToolCallPolicyUsesConfiguredYAMLRules(t *testing.T) {
	t.Parallel()

	policyPath := writeAppPolicyFile(t, `
rules:
  - name: configured-mock-deny
    priority: 1000
    match:
      tool_namespace: mock
      tool_name: echo
      operation_type: mock
      user_role: owner
    effect: deny
    reason: configured policy denied mock echo
`)
	srv, st, workspace := newGovernanceTestAppWithConfig(t, config.Config{PolicyConfigPath: policyPath})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected policy response 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "denied" || response.Reason != "configured policy denied mock echo" {
		t.Fatalf("expected configured YAML deny to win, got %+v", response)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 || calls[0].PolicyDecision != "deny" || calls[0].ErrorMessage != "configured policy denied mock echo" {
		t.Fatalf("unexpected audited policy decision: %+v", calls)
	}
}

func TestPolicyRuleAPIAndSimulatorUseManagedRules(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)

	createResp := postJSON(t, srv, "/api/policies", `{
		"name":"deny mock echo",
		"description":"demo deny rule",
		"enabled":true,
		"priority":10,
		"effect":"deny",
		"connectorType":"mock",
		"toolNamePattern":"mock.echo",
		"operationType":"*",
		"riskLevel":"*",
		"resourcePattern":"*",
		"reason":"managed policy denied mock echo"
	}`)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected policy create 201, got %d body=%s", createResp.Code, createResp.Body.String())
	}
	var created model.PolicyRule
	decodeBody(t, createResp.Body.Bytes(), &created)
	if created.ID == "" || created.WorkspaceID == "" || created.Effect != "deny" {
		t.Fatalf("unexpected created policy rule: %+v", created)
	}

	resp := getJSON(t, srv, "/api/policies")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Items []model.PolicyRule `json:"items"`
	}
	decodeBody(t, resp.Body.Bytes(), &payload)
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 policy rule, got %+v", payload)
	}
	item := payload.Items[0]
	if item.ID != created.ID || item.Name != "deny mock echo" || item.Effect != "deny" || item.ToolNamePattern != "mock.echo" {
		t.Fatalf("unexpected policy payload: %+v", item)
	}

	simResp := postJSON(t, srv, "/api/policies/simulate", `{
		"connectorType":"mock",
		"toolName":"mock.echo",
		"operationType":"mock",
		"riskLevel":"low",
		"resource":""
	}`)
	if simResp.Code != http.StatusOK {
		t.Fatalf("expected simulate 200, got %d body=%s", simResp.Code, simResp.Body.String())
	}
	var simulation policySimulationResponse
	decodeBody(t, simResp.Body.Bytes(), &simulation)
	if simulation.Decision != "deny" || simulation.MatchedRuleID != created.ID || simulation.Defaulted {
		t.Fatalf("expected managed deny simulation, got %+v", simulation)
	}
	updateResp := putJSON(t, srv, "/api/policies/"+created.ID, `{
		"name":"deny mock echo updated",
		"description":"updated demo deny rule",
		"enabled":false,
		"priority":15,
		"effect":"deny",
		"connectorType":"mock",
		"toolNamePattern":"mock.echo",
		"operationType":"*",
		"riskLevel":"*",
		"resourcePattern":"*",
		"reason":"updated deny reason"
	}`)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("expected policy update 200, got %d body=%s", updateResp.Code, updateResp.Body.String())
	}
	var updated model.PolicyRule
	decodeBody(t, updateResp.Body.Bytes(), &updated)
	if updated.Enabled || updated.Priority != 15 || updated.Name != "deny mock echo updated" {
		t.Fatalf("unexpected updated policy rule: %+v", updated)
	}
	getResp := getJSON(t, srv, "/api/policies/"+created.ID)
	if getResp.Code != http.StatusOK {
		t.Fatalf("expected get policy 200, got %d body=%s", getResp.Code, getResp.Body.String())
	}
	deleteResp := deleteJSON(t, srv, "/api/policies/"+created.ID)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("expected delete policy 200, got %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	if encoded, err := json.Marshal(payload); err != nil || string(encoded) == "" {
		t.Fatalf("policy payload must stay JSON-serializable: encoded=%s err=%v", encoded, err)
	}
}

func TestPolicyRuleCRUDRequiresOwnerOrAdminRole(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestAppWithRole(t, "member")
	ctx := context.Background()

	createResp := postJSON(t, srv, "/api/policies", `{
		"name":"member denied create",
		"enabled":true,
		"priority":10,
		"effect":"deny",
		"connectorType":"mock",
		"toolNamePattern":"mock.echo",
		"operationType":"*",
		"riskLevel":"*",
		"resourcePattern":"*",
		"reason":"member should not create"
	}`)
	if createResp.Code != http.StatusForbidden {
		t.Fatalf("expected member create to return 403, got %d body=%s", createResp.Code, createResp.Body.String())
	}
	rules, err := st.ListPolicyRules(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list policy rules after forbidden create: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("forbidden create must not persist policy rules, got %+v", rules)
	}

	enabled := true
	rule, err := st.CreatePolicyRule(ctx, model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "existing deny",
		Enabled:         enabled,
		Priority:        10,
		Effect:          "deny",
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "original reason",
	})
	if err != nil {
		t.Fatalf("seed policy rule: %v", err)
	}

	updateResp := putJSON(t, srv, "/api/policies/"+rule.ID, `{
		"name":"member changed rule",
		"enabled":false,
		"priority":1,
		"effect":"allow",
		"connectorType":"mock",
		"toolNamePattern":"mock.echo",
		"operationType":"*",
		"riskLevel":"*",
		"resourcePattern":"*",
		"reason":"member should not update"
	}`)
	if updateResp.Code != http.StatusForbidden {
		t.Fatalf("expected member update to return 403, got %d body=%s", updateResp.Code, updateResp.Body.String())
	}
	unchanged, err := st.GetPolicyRuleByID(ctx, workspace.ID, rule.ID)
	if err != nil {
		t.Fatalf("get policy rule after forbidden update: %v", err)
	}
	if unchanged.Name != rule.Name || unchanged.Effect != rule.Effect || unchanged.Reason != rule.Reason || unchanged.Priority != rule.Priority || unchanged.Enabled != rule.Enabled {
		t.Fatalf("forbidden update must leave policy rule unchanged, before=%+v after=%+v", rule, unchanged)
	}

	deleteResp := deleteJSON(t, srv, "/api/policies/"+rule.ID)
	if deleteResp.Code != http.StatusForbidden {
		t.Fatalf("expected member delete to return 403, got %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	remaining, err := st.GetPolicyRuleByID(ctx, workspace.ID, rule.ID)
	if err != nil {
		t.Fatalf("forbidden delete must leave policy rule present: %v", err)
	}
	if remaining.ID != rule.ID {
		t.Fatalf("unexpected remaining policy rule: %+v", remaining)
	}
}

func TestManagedPolicyRulesAffectToolCallAudit(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	rule, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "deny mock echo",
		Enabled:         true,
		Priority:        1,
		Effect:          "deny",
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "managed policy denied mock echo",
	})
	if err != nil {
		t.Fatalf("create policy rule: %v", err)
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "denied" || response.Reason != "managed policy denied mock echo" {
		t.Fatalf("expected managed policy deny, got %+v", response)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one audited call, got %+v", calls)
	}
	call := calls[0]
	if call.PolicyDecision != "deny" || call.Explanation == nil {
		t.Fatalf("expected denied call with explanation, got %+v", call)
	}
	if !hasSignalPrefix(call.Explanation.Signals, "matchedPolicyRuleId:"+rule.ID) ||
		!hasSignalPrefix(call.Explanation.Signals, "policyExplanation:managed policy denied mock echo") {
		t.Fatalf("expected matched policy rule signals, got %+v", call.Explanation)
	}
}

func TestManagedPolicyRequireApprovalAndDisabledRuleFallback(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	if _, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "disabled deny",
		Enabled:         false,
		Priority:        1,
		Effect:          "deny",
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "disabled rule should not match",
	}); err != nil {
		t.Fatalf("create disabled policy: %v", err)
	}
	if _, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "approval mock",
		Enabled:         true,
		Priority:        20,
		Effect:          "require_approval",
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "managed rule requires approval",
	}); err != nil {
		t.Fatalf("create approval policy: %v", err)
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" || response.Reason != "managed rule requires approval" {
		t.Fatalf("expected managed approval requirement, got %+v", response)
	}
}

func TestManagedPolicyAllowCannotBypassDefaultDeny(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "demo", "blocked", "Blocked Tool", "mock", "low", false)
	if _, err := st.CreatePolicyRule(context.Background(), model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "allow unsupported",
		Enabled:         true,
		Priority:        1,
		Effect:          "allow",
		ConnectorType:   "demo",
		ToolNamePattern: "demo.blocked",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
		Reason:          "attempt to allow unsupported tool",
	}); err != nil {
		t.Fatalf("create allow policy: %v", err)
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"demo.blocked","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "denied" || !strings.Contains(response.Reason, "默认安全兜底") {
		t.Fatalf("expected default deny to remain stricter, got %+v", response)
	}
}

func TestPolicySimulatorFallsBackWithoutExecuting(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	simResp := postJSON(t, srv, "/api/policies/simulate", `{
		"connectorType":"mock",
		"toolName":"mock.echo",
		"operationType":"mock",
		"riskLevel":"low",
		"resource":""
	}`)
	if simResp.Code != http.StatusOK {
		t.Fatalf("expected simulate 200, got %d body=%s", simResp.Code, simResp.Body.String())
	}
	var simulation policySimulationResponse
	decodeBody(t, simResp.Body.Bytes(), &simulation)
	if simulation.Decision != "allow" || !simulation.Defaulted {
		t.Fatalf("expected default allow simulation, got %+v", simulation)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("simulator must not execute or audit tool calls, got %+v", calls)
	}
}

func TestPolicySimulatorUsesRequestUserRoleForFallback(t *testing.T) {
	t.Parallel()

	policyPath := writeAppPolicyFile(t, `
rules:
  - name: owner-mock-allow
    priority: 1000
    match:
      tool_namespace: mock
      tool_name: echo
      operation_type: mock
      user_role: owner
      supported_tool: true
    effect: allow
    reason: owner mock allowed
  - name: viewer-mock-deny
    priority: 990
    match:
      tool_namespace: mock
      tool_name: echo
      operation_type: mock
      user_role: viewer
      supported_tool: true
    effect: deny
    reason: viewer mock denied
`)
	srv, st, workspace := newGovernanceTestAppWithRole(t, "viewer")
	srv.cfg.PolicyConfigPath = policyPath
	srv.reloadPolicyEngine()

	simResp := postJSON(t, srv, "/api/policies/simulate", `{
		"connectorType":"mock",
		"toolName":"mock.echo",
		"operationType":"mock",
		"riskLevel":"low",
		"resource":""
	}`)
	if simResp.Code != http.StatusOK {
		t.Fatalf("expected simulate 200, got %d body=%s", simResp.Code, simResp.Body.String())
	}
	var simulation policySimulationResponse
	decodeBody(t, simResp.Body.Bytes(), &simulation)
	if simulation.Decision != "deny" || simulation.Explanation != "viewer mock denied" || !simulation.Defaulted {
		t.Fatalf("expected viewer fallback deny without owner contamination, got %+v", simulation)
	}
	if len(simulation.EvaluationTrace) == 0 || simulation.EvaluationTrace[0].RuleName != "viewer-mock-deny" {
		t.Fatalf("expected viewer fallback rule in trace, got %+v", simulation.EvaluationTrace)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("simulator must not execute or audit tool calls, got %+v", calls)
	}
}

func newGovernanceTestAppWithConfig(t *testing.T, overrides config.Config) (*App, store.Store, model.Workspace) {
	t.Helper()

	srv, st, workspace := newGovernanceTestApp(t)
	if overrides.PolicyConfigPath != "" {
		srv.cfg.PolicyConfigPath = overrides.PolicyConfigPath
		srv.reloadPolicyEngine()
	}
	return srv, st, workspace
}

func writeAppPolicyFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write app policy file: %v", err)
	}
	return path
}

func hasSignalPrefix(signals []string, want string) bool {
	for _, signal := range signals {
		if strings.HasPrefix(signal, want) {
			return true
		}
	}
	return false
}

func putJSON(t *testing.T, srv *App, path string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func deleteJSON(t *testing.T, srv *App, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

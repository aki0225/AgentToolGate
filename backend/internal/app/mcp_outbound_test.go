package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/mcp"
	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
)

func TestMCPOutboundSyncAndGovernedCallFlow(t *testing.T) {
	t.Parallel()

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "weather",
		DisplayName: "Weather MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
			"headers": map[string]string{
				"X-Test": "hello",
			},
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{"force":true}`)
	if syncResp.Code != http.StatusOK {
		t.Fatalf("expected sync 200, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}
	var syncBody struct {
		CreatedTools []string `json:"createdTools"`
		SkippedTools []string `json:"skippedTools"`
	}
	decodeBody(t, syncResp.Body.Bytes(), &syncBody)
	if len(syncBody.CreatedTools) != 2 {
		t.Fatalf("expected 2 synced tools, got %+v", syncBody)
	}

	readTool, err := st.GetToolByKey(context.Background(), workspace.ID, "mcp_weather.get_forecast")
	if err != nil {
		t.Fatalf("get read tool: %v", err)
	}
	if readTool.OperationType != "read" || readTool.RequiresApproval {
		t.Fatalf("unexpected synced read tool: %+v", readTool)
	}

	writeTool, err := st.GetToolByKey(context.Background(), workspace.ID, "mcp_weather.create_note")
	if err != nil {
		t.Fatalf("get write tool: %v", err)
	}
	if writeTool.OperationType != "create" || !writeTool.RequiresApproval {
		t.Fatalf("unexpected synced write tool: %+v", writeTool)
	}

	readResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather.get_forecast","arguments":{"city":"Shanghai"}}`)
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected read tool success, got %d body=%s", readResp.Code, readResp.Body.String())
	}
	var readResult struct {
		Status string `json:"status"`
	}
	decodeBody(t, readResp.Body.Bytes(), &readResult)
	if readResult.Status != "success" {
		t.Fatalf("unexpected read tool result: %+v", readResult)
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 1 {
		t.Fatalf("expected one remote call after read tool, got %d", got)
	}
	callsAfterRead, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls after read: %v", err)
	}
	if len(callsAfterRead) == 0 || callsAfterRead[0].Explanation == nil {
		t.Fatalf("expected mcp audit explanation, got %+v", callsAfterRead)
	}
	if callsAfterRead[0].Explanation.TargetCategory != "mcp_connector" ||
		!hasString(callsAfterRead[0].Explanation.Signals, "connector:weather") ||
		!hasString(callsAfterRead[0].Explanation.Signals, "remoteTool:get_forecast") {
		t.Fatalf("unexpected mcp audit explanation: %+v", callsAfterRead[0].Explanation)
	}

	writeResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather.create_note","arguments":{"title":"Hello"}}`)
	if writeResp.Code != http.StatusOK {
		t.Fatalf("expected approval_required, got %d body=%s", writeResp.Code, writeResp.Body.String())
	}
	var writeResult struct {
		Status     string `json:"status"`
		CallID     string `json:"callId"`
		ApprovalID string `json:"approvalId"`
	}
	decodeBody(t, writeResp.Body.Bytes(), &writeResult)
	if writeResult.Status != "approval_required" || writeResult.ApprovalID == "" {
		t.Fatalf("unexpected approval response: %+v", writeResult)
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 1 {
		t.Fatalf("write tool must not execute before approval, got %d remote calls", got)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+writeResult.ApprovalID+"/approve", `{}`)
	if approveResp.Code != http.StatusOK {
		t.Fatalf("approve failed: %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var approveBody struct {
		Approval model.ApprovalRequest `json:"approval"`
		ToolCall model.ToolCall        `json:"toolCall"`
	}
	decodeBody(t, approveResp.Body.Bytes(), &approveBody)
	if approveBody.ToolCall.Status != "success" || approveBody.ToolCall.ApprovalStatus != "approved" {
		t.Fatalf("unexpected approved tool call: %+v", approveBody.ToolCall)
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 2 {
		t.Fatalf("expected remote call after approval, got %d", got)
	}
	if approveBody.ToolCall.Explanation == nil || !hasString(approveBody.ToolCall.Explanation.Signals, "remoteTool:create_note") {
		t.Fatalf("expected approval action to include mcp explanation, got %+v", approveBody.ToolCall.Explanation)
	}
}

func TestMCPOutboundHeaderSecretRefsInjectEnvAndDoNotLeak(t *testing.T) {
	const secretRef = "MCP_WEATHER_AUTH_TEST"
	const secretValue = "Bearer mcp-secret-token-12345"
	t.Setenv(secretRef, secretValue)

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	createResp := postJSON(t, srv, "/api/connectors", fmt.Sprintf(`{
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
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create connector failed: %d body=%s", createResp.Code, createResp.Body.String())
	}
	if strings.Contains(createResp.Body.String(), secretValue) {
		t.Fatalf("connector response leaked resolved secret: %s", createResp.Body.String())
	}
	if !strings.Contains(createResp.Body.String(), secretRef) {
		t.Fatalf("connector response should keep secret ref name, got %s", createResp.Body.String())
	}

	var created model.Connector
	decodeBody(t, createResp.Body.Bytes(), &created)
	syncResp := postJSON(t, srv, "/api/connectors/"+created.ID+"/sync", `{}`)
	if syncResp.Code != http.StatusOK {
		t.Fatalf("sync with secret ref failed: %d body=%s", syncResp.Code, syncResp.Body.String())
	}
	if strings.Contains(syncResp.Body.String(), secretValue) {
		t.Fatalf("sync response leaked resolved secret: %s", syncResp.Body.String())
	}
	if !mockServer.sawHeader("Authorization", secretValue) {
		t.Fatalf("expected upstream MCP server to receive resolved Authorization header, got %+v", mockServer.headerValues("Authorization"))
	}

	readResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather_secret.get_forecast","arguments":{"city":"Shanghai"}}`)
	if readResp.Code != http.StatusOK {
		t.Fatalf("read with secret ref failed: %d body=%s", readResp.Code, readResp.Body.String())
	}
	if strings.Contains(readResp.Body.String(), secretValue) {
		t.Fatalf("tool response leaked resolved secret: %s", readResp.Body.String())
	}
	if !mockServer.sawHeader("Authorization", secretValue) {
		t.Fatalf("expected tool call to receive resolved Authorization header")
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	for _, call := range calls {
		if strings.Contains(string(call.InputRedactedJSON), secretValue) ||
			strings.Contains(string(call.OutputRedactedJSON), secretValue) ||
			strings.Contains(call.ErrorMessage, secretValue) {
			t.Fatalf("audit leaked resolved secret: %+v", call)
		}
	}
}

func TestMCPOutboundMissingSecretRefFailsBeforeApprovalAndRemoteCall(t *testing.T) {
	const secretRef = "MCP_MISSING_AUTH_TEST"
	_ = os.Unsetenv(secretRef)

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "missing_secret",
		DisplayName: "Missing Secret MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
			"headerSecretRefs": map[string]string{
				"Authorization": secretRef,
			},
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	if _, err := st.CreateTool(context.Background(), model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mcp_missing_secret",
		Name:             "create_note",
		DisplayName:      "Create Note",
		Description:      "write tool with missing secret ref",
		OperationType:    "create",
		RiskLevel:        "medium",
		RequiresApproval: true,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("create tool: %v", err)
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if syncResp.Code != http.StatusBadRequest {
		t.Fatalf("expected sync 400 for missing secret ref, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}
	if len(mockServer.methodsSnapshot()) != 0 {
		t.Fatalf("sync with missing secret must not touch upstream, got methods %+v", mockServer.methodsSnapshot())
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_missing_secret.create_note","arguments":{"title":"Hello"}}`)
	if callResp.Code != http.StatusBadRequest {
		t.Fatalf("expected tool call 400 for missing secret ref, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 0 {
		t.Fatalf("missing secret must not call upstream, got %d", got)
	}
	approvals, err := st.ListApprovalRequests(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("missing secret must not create approval, got %+v", approvals)
	}
	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 || calls[0].Status != "failed" {
		t.Fatalf("expected failed audit for missing secret, got %+v", calls)
	}
	if strings.Contains(calls[0].ErrorMessage, "Bearer") || strings.Contains(calls[0].ErrorMessage, "token") {
		t.Fatalf("missing secret error should not leak token-like value, got %q", calls[0].ErrorMessage)
	}
}

func TestMCPOutboundRejectsDuplicatePlainAndSecretRefHeader(t *testing.T) {
	const secretRef = "MCP_DUP_AUTH_TEST"
	t.Setenv(secretRef, "Bearer duplicate-secret")

	_, err := resolveMCPConnectorHeaders(mcpConnectorConfig{
		Headers: map[string]string{
			"Authorization": "Bearer plain",
		},
		HeaderSecretRefs: map[string]string{
			"authorization": secretRef,
		},
	})
	if err == nil {
		t.Fatalf("expected duplicate plain/secret header to be rejected")
	}
}

func TestMCPOutboundRejectDoesNotCallRemoteAndRedactsAudit(t *testing.T) {
	t.Parallel()

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "notes",
		DisplayName: "Notes MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
			"headers": map[string]string{
				"Authorization": "Bearer upstream-secret-token",
			},
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if syncResp.Code != http.StatusOK {
		t.Fatalf("expected sync 200, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}

	writeResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_notes.create_note","arguments":{"title":"Hello","body":"raw body secret","token":"caller-secret-token"}}`)
	if writeResp.Code != http.StatusOK {
		t.Fatalf("expected approval_required, got %d body=%s", writeResp.Code, writeResp.Body.String())
	}
	var writeResult struct {
		Status     string `json:"status"`
		ApprovalID string `json:"approvalId"`
	}
	decodeBody(t, writeResp.Body.Bytes(), &writeResult)
	if writeResult.Status != "approval_required" || writeResult.ApprovalID == "" {
		t.Fatalf("unexpected approval response: %+v", writeResult)
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 0 {
		t.Fatalf("write tool must not execute before review, got %d remote calls", got)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected pending tool call")
	}
	pendingInput := string(calls[0].InputRedactedJSON)
	for _, leaked := range []string{"raw body secret", "caller-secret-token", "upstream-secret-token"} {
		if strings.Contains(pendingInput, leaked) {
			t.Fatalf("mcp pending audit input leaked %q: %s", leaked, pendingInput)
		}
	}
	if !strings.Contains(pendingInput, "[REDACTED]") {
		t.Fatalf("expected redacted mcp audit input, got %s", pendingInput)
	}

	rejectResp := postJSON(t, srv, "/api/approvals/"+writeResult.ApprovalID+"/reject", `{}`)
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("reject failed: %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}
	var rejectBody struct {
		Approval model.ApprovalRequest `json:"approval"`
		ToolCall model.ToolCall        `json:"toolCall"`
	}
	decodeBody(t, rejectResp.Body.Bytes(), &rejectBody)
	if rejectBody.ToolCall.Status != "rejected" || rejectBody.ToolCall.ApprovalStatus != "rejected" {
		t.Fatalf("unexpected rejected tool call: %+v", rejectBody.ToolCall)
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 0 {
		t.Fatalf("rejected write must not execute remote MCP call, got %d", got)
	}
}

func TestMCPOutboundRedactsRemoteOutput(t *testing.T) {
	t.Parallel()

	mockServer := newMockOutboundMCPServer(t)
	mockServer.outputSecret = "remote-token-secret-12345"
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "weather",
		DisplayName: "Weather MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if syncResp.Code != http.StatusOK {
		t.Fatalf("expected sync 200, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}

	readResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather.get_forecast","arguments":{"city":"Shanghai"}}`)
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected read tool success, got %d body=%s", readResp.Code, readResp.Body.String())
	}
	if strings.Contains(readResp.Body.String(), mockServer.outputSecret) {
		t.Fatalf("mcp response leaked remote secret: %s", readResp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected stored tool call")
	}
	output := string(calls[0].OutputRedactedJSON)
	if strings.Contains(output, mockServer.outputSecret) {
		t.Fatalf("mcp audit output leaked remote secret: %s", output)
	}
	if !strings.Contains(output, "[REDACTED]") && !strings.Contains(output, "***") {
		t.Fatalf("expected redacted mcp output, got %s", output)
	}
}

func TestMCPOutboundSyncUpdatesExistingToolAndPreservesDisabledState(t *testing.T) {
	t.Parallel()

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "weather",
		DisplayName: "Weather MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	firstSync := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if firstSync.Code != http.StatusOK {
		t.Fatalf("expected first sync 200, got %d body=%s", firstSync.Code, firstSync.Body.String())
	}

	readTool, err := st.GetToolByKey(context.Background(), workspace.ID, "mcp_weather.get_forecast")
	if err != nil {
		t.Fatalf("get read tool: %v", err)
	}
	disabled := false
	if _, err := st.UpdateTool(context.Background(), workspace.ID, readTool.ID, model.UpdateToolInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable read tool: %v", err)
	}

	mockServer.replaceTools([]map[string]any{
		{
			"name":        "get_forecast",
			"title":       "Get Forecast V2",
			"description": "Updated description from upstream.",
			"annotations": map[string]any{
				"destructiveHint": true,
			},
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
					"unit": map[string]any{"type": "string"},
				},
			},
		},
	})

	secondSync := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if secondSync.Code != http.StatusOK {
		t.Fatalf("expected second sync 200, got %d body=%s", secondSync.Code, secondSync.Body.String())
	}
	var syncBody struct {
		UpdatedTools []string `json:"updatedTools"`
		StaleTools   []string `json:"staleTools"`
	}
	decodeBody(t, secondSync.Body.Bytes(), &syncBody)
	if !hasString(syncBody.UpdatedTools, "mcp_weather.get_forecast") {
		t.Fatalf("expected updated read tool summary, got %+v", syncBody)
	}
	if !hasString(syncBody.StaleTools, "mcp_weather.create_note") {
		t.Fatalf("expected stale create_note summary, got %+v", syncBody)
	}

	updatedTool, err := st.GetToolByKey(context.Background(), workspace.ID, "mcp_weather.get_forecast")
	if err != nil {
		t.Fatalf("get updated tool: %v", err)
	}
	if updatedTool.DisplayName != "Get Forecast V2" ||
		updatedTool.Description != "Updated description from upstream." ||
		updatedTool.OperationType != "delete" ||
		updatedTool.RiskLevel != "high" ||
		!updatedTool.RequiresApproval {
		t.Fatalf("expected sync to update metadata/governance, got %+v", updatedTool)
	}
	if updatedTool.Enabled {
		t.Fatalf("sync must preserve manual disabled state, got %+v", updatedTool)
	}
	if !strings.Contains(string(updatedTool.InputSchemaJSON), "unit") {
		t.Fatalf("expected input schema update, got %s", updatedTool.InputSchemaJSON)
	}
}

func TestMCPOutboundConnectorValidationRejectsUnsafeConfig(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "unsafe",
		DisplayName: "Unsafe MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       "https://user:pass@example.com/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`)
	if syncResp.Code != http.StatusBadRequest {
		t.Fatalf("expected unsafe config 400, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}
}

func TestMCPOutboundGovernanceUsesMostRestrictiveAnnotationsAndRejectsNullArguments(t *testing.T) {
	t.Parallel()

	destructiveRead := mcp.OutboundTool{
		Name: "get_secret",
		Annotations: map[string]any{
			"readOnlyHint":    true,
			"destructiveHint": true,
		},
	}
	operationType, riskLevel, requiresApproval := inferMCPToolGovernance(destructiveRead)
	if operationType != "delete" || riskLevel != "high" || !requiresApproval {
		t.Fatalf("destructiveHint must win over readOnlyHint, got %s/%s/%v", operationType, riskLevel, requiresApproval)
	}

	unknownTool := mcp.OutboundTool{Name: "summarize"}
	operationType, riskLevel, requiresApproval = inferMCPToolGovernance(unknownTool)
	if operationType != "write" || riskLevel != "medium" || !requiresApproval {
		t.Fatalf("unknown MCP tools must default to approval, got %s/%s/%v", operationType, riskLevel, requiresApproval)
	}

	if err := validateMCPToolArguments(nil); err == nil {
		t.Fatalf("nil MCP arguments must be rejected before creating an approval")
	}
	if err := validateMCPToolArguments([]any{}); err == nil {
		t.Fatalf("array MCP arguments must be rejected before creating an approval")
	}
	if err := validateMCPToolArguments(map[string]any{}); err != nil {
		t.Fatalf("object MCP arguments should be accepted: %v", err)
	}
}

func TestMCPOutboundWorkspaceScopedConnectorLookup(t *testing.T) {
	t.Parallel()

	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})
	otherWorkspace, err := st.CreateWorkspace(context.Background(), model.CreateWorkspaceInput{
		Name:                  "Other Workspace",
		Slug:                  "other-" + uuid.NewString(),
		ZitadelOrganizationID: "other-org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "weather",
		DisplayName: "Weather MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	if syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`); syncResp.Code != http.StatusOK {
		t.Fatalf("expected sync 200, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}

	if _, err := st.CreateTool(context.Background(), model.CreateToolInput{
		WorkspaceID:      otherWorkspace.ID,
		Namespace:        "mcp_weather",
		Name:             "get_forecast",
		DisplayName:      "Foreign Weather",
		Description:      "Synthetic cross-workspace tool without connector",
		OperationType:    "read",
		RiskLevel:        "low",
		RequiresApproval: false,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("create other workspace tool: %v", err)
	}

	resp := postJSONWithWorkspace(t, srv, otherWorkspace.ZitadelOrganizationID, "/api/tool-calls", `{"tool":"mcp_weather.get_forecast","arguments":{"city":"Shanghai"}}`)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected cross-workspace connector lookup 404, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := atomic.LoadInt32(&mockServer.callCount); got != 0 {
		t.Fatalf("cross-workspace call must not reach original MCP connector, got %d calls", got)
	}
	calls, err := st.ListToolCalls(context.Background(), otherWorkspace.ID)
	if err != nil {
		t.Fatalf("list other workspace calls: %v", err)
	}
	if len(calls) != 1 || calls[0].ToolKey != "mcp_weather.get_forecast" || calls[0].Status != "failed" {
		t.Fatalf("expected failed audit in other workspace, got %+v", calls)
	}
}

func TestMCPOutboundEmitsConnectorSpanAttributes(t *testing.T) {
	t.Parallel()

	exporter := installInMemoryTracer(t)
	mockServer := newMockOutboundMCPServer(t)
	srv, st, workspace := newMCPAppTestServer(t, config.Config{})

	connector, err := st.CreateConnector(context.Background(), model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "mcp",
		Name:        "weather",
		DisplayName: "Weather MCP",
		ConfigJSON: mustBootstrapConnectorJSON(map[string]any{
			"transport": "sse",
			"url":       mockServer.URL + "/mcp/sse",
		}),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}
	if syncResp := postJSON(t, srv, "/api/connectors/"+connector.ID+"/sync", `{}`); syncResp.Code != http.StatusOK {
		t.Fatalf("expected sync 200, got %d body=%s", syncResp.Code, syncResp.Body.String())
	}

	readResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mcp_weather.get_forecast","arguments":{"city":"Shanghai"}}`)
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected read success, got %d body=%s", readResp.Code, readResp.Body.String())
	}

	span, ok := findSpanByName(exporter.GetSpans(), "connector.mcp.get_forecast")
	if !ok {
		t.Fatalf("expected connector.mcp.get_forecast span, got %+v", exporter.GetSpans())
	}
	if got := stringAttribute(span, "tool.key"); got != "mcp_weather.get_forecast" {
		t.Fatalf("expected tool.key attribute, got %q", got)
	}
	if got := stringAttribute(span, "mcp.connector"); got != "weather" {
		t.Fatalf("expected mcp.connector=weather, got %q", got)
	}
	if got := stringAttribute(span, "mcp.tool"); got != "get_forecast" {
		t.Fatalf("expected mcp.tool=get_forecast, got %q", got)
	}
}

type mockOutboundMCPServer struct {
	URL            string
	mu             sync.Mutex
	sessions       map[string]chan mcp.JSONRPCResponse
	requestHeaders []http.Header
	requestMethods []string
	tools          []map[string]any
	callCount      int32
	outputSecret   string
	server         *httptest.Server
}

func newMockOutboundMCPServer(t *testing.T) *mockOutboundMCPServer {
	t.Helper()

	mock := &mockOutboundMCPServer{
		sessions: map[string]chan mcp.JSONRPCResponse{},
	}
	mock.tools = defaultMockOutboundMCPTools()
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/mcp/sse":
			mock.handleSSEGet(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/mcp/sse":
			mock.handleSSEPost(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	mock.URL = mock.server.URL
	t.Cleanup(mock.server.Close)
	return mock
}

func (m *mockOutboundMCPServer) handleSSEGet(w http.ResponseWriter, r *http.Request) {
	m.recordHeaders(r)
	sessionID := "session-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	responses := make(chan mcp.JSONRPCResponse, 16)

	m.mu.Lock()
	m.sessions[sessionID] = responses
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		close(responses)
	}()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprintf(w, "event: endpoint\n")
	fmt.Fprintf(w, "data: /mcp/sse?sessionId=%s\n\n", sessionID)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	for {
		select {
		case resp := <-responses:
			if !m.writeMCPEvent(w, resp) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (m *mockOutboundMCPServer) handleSSEPost(w http.ResponseWriter, r *http.Request) {
	m.recordHeaders(r)
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	if sessionID == "" {
		http.Error(w, "missing session", http.StatusNotFound)
		return
	}

	var req mcp.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json-rpc", http.StatusBadRequest)
		return
	}

	response := m.buildResponse(req)

	m.mu.Lock()
	ch, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	ch <- response
	w.WriteHeader(http.StatusAccepted)
}

func (m *mockOutboundMCPServer) buildResponse(req mcp.JSONRPCRequest) mcp.JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]any{
					"name":    "mock-mcp",
					"version": "1.0.0",
				},
			},
		}
	case "tools/list":
		m.mu.Lock()
		tools := cloneToolList(m.tools)
		m.mu.Unlock()
		return mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": tools,
			},
		}
	case "tools/call":
		atomic.AddInt32(&m.callCount, 1)
		var params struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &params)
		text := fmt.Sprintf("%s executed", params.Name)
		metadata := map[string]any{
			"status": "success",
		}
		if strings.TrimSpace(m.outputSecret) != "" {
			text = text + " token " + m.outputSecret
			metadata["token"] = m.outputSecret
		}
		return mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": text,
					},
				},
				"isError":  false,
				"metadata": metadata,
			},
		}
	default:
		return mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &mcp.JSONRPCError{
				Code:    mcp.ErrMethodNotFound,
				Message: "method not found",
			},
		}
	}
}

func (m *mockOutboundMCPServer) writeMCPEvent(w http.ResponseWriter, response mcp.JSONRPCResponse) bool {
	payload, err := json.Marshal(response)
	if err != nil {
		return false
	}
	fmt.Fprintf(w, "event: message\n")
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

func (m *mockOutboundMCPServer) recordHeaders(r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestHeaders = append(m.requestHeaders, r.Header.Clone())
	m.requestMethods = append(m.requestMethods, r.Method)
}

func (m *mockOutboundMCPServer) sawHeader(name, value string) bool {
	for _, candidate := range m.headerValues(name) {
		if candidate == value {
			return true
		}
	}
	return false
}

func (m *mockOutboundMCPServer) headerValues(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := make([]string, 0)
	for _, headers := range m.requestHeaders {
		values = append(values, headers.Values(name)...)
	}
	return values
}

func (m *mockOutboundMCPServer) replaceTools(tools []map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = cloneToolList(tools)
}

func (m *mockOutboundMCPServer) methodsSnapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.requestMethods...)
}

func defaultMockOutboundMCPTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "get_forecast",
			"title":       "Get Forecast",
			"description": "Returns a demo weather forecast.",
			"annotations": map[string]any{
				"readOnlyHint": true,
			},
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name":        "create_note",
			"title":       "Create Note",
			"description": "Creates a demo note.",
			"annotations": map[string]any{
				"readOnlyHint": false,
			},
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string"},
				},
			},
		},
	}
}

func cloneToolList(input []map[string]any) []map[string]any {
	cloned := make([]map[string]any, 0, len(input))
	for _, item := range input {
		raw, _ := json.Marshal(item)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		cloned = append(cloned, decoded)
	}
	return cloned
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

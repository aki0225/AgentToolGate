package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
)

func TestMemoryApprovalTransitionIsCompareAndSwap(t *testing.T) {
	t.Parallel()
	assertApprovalTransitionIsCompareAndSwap(t, NewMemoryStore())
}

func TestMemoryApprovalRequestExpiresAndBlocksTransition(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:     "workspace-1",
		ToolKey:         "mock.write",
		ToolDisplayName: "Mock Write",
		RequestedBy:     "requester",
		Reason:          "write operation requires approval",
		TTL:             -time.Minute,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	if !approval.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("expected expired approval timestamp, got %+v", approval)
	}

	items, err := st.ListApprovalRequests(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(items) != 1 || items[0].Status != "expired" {
		t.Fatalf("expected expired approval in list, got %+v", items)
	}

	got, err := st.GetApprovalRequestByID(context.Background(), "workspace-1", approval.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != "expired" {
		t.Fatalf("expected expired approval status, got %+v", got)
	}

	_, err = st.TransitionApprovalRequest(context.Background(), "workspace-1", approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired transition error, got %v", err)
	}
}

func assertApprovalTransitionIsCompareAndSwap(t *testing.T, st Store) {
	t.Helper()

	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(context.Background(), model.CreateWorkspaceInput{
		Name:                  "Approval Workspace " + suffix,
		Slug:                  "approval-" + suffix,
		ZitadelOrganizationID: "org-approval-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:     workspace.ID,
		ToolKey:         "mock.write",
		ToolDisplayName: "Mock Write",
		RequestedBy:     "requester",
		Reason:          "write operation requires approval",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	updated, err := st.TransitionApprovalRequest(context.Background(), workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	})
	if err != nil {
		t.Fatalf("first transition should succeed: %v", err)
	}
	if updated.Status != "approved" || updated.ReviewedBy != "owner" {
		t.Fatalf("unexpected updated approval: %+v", updated)
	}

	_, err = st.TransitionApprovalRequest(context.Background(), workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner-2",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second transition should conflict, got %v", err)
	}
}

func TestMemoryApprovalRequestMetadataPersists(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertApprovalRequestMetadataPersists(t, st)
}

func TestMemoryApprovalRequestFingerprintAllowsNewTicketAfterInactive(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertApprovalRequestFingerprintAllowsNewTicketAfterInactive(t, st)
}

func TestMemoryUpdateToolCallClearsExecutionInput(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertUpdateToolCallClearsExecutionInput(t, st)
}

func TestMemoryToolCallExplanationPersists(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertToolCallExplanationPersists(t, st)
}

func TestMemoryUpdateToolCanToggleEnabled(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
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
	tool, err := st.GetToolByKey(context.Background(), workspaces[0].ID, "mock.echo")
	if err != nil {
		t.Fatalf("get tool: %v", err)
	}

	updated, err := st.UpdateTool(context.Background(), workspaces[0].ID, tool.ID, model.UpdateToolInput{Enabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("update tool: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("expected tool to be disabled, got %+v", updated)
	}
}

func TestMemoryUpdateToolMetadataPreservesDisabledState(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertUpdateToolMetadataPreservesDisabledState(t, st)
}

func TestMemorySecretCRUDAndValidation(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertSecretCRUDAndValidation(t, st)
}

func TestMemoryPolicyRuleCRUDAndOrdering(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertPolicyRuleCRUDAndOrdering(t, st)
}

func TestMemoryStoreBootstrapRegistersConnectors(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
		Connectors: []model.BootstrapConnectorInput{
			{
				Type:        "database",
				Name:        "local_postgres",
				DisplayName: "Local PostgreSQL",
				ConfigJSON:  json.RawMessage(`{"datasource":"local_postgres"}`),
				Enabled:     true,
			},
			{
				Type:        "github",
				Name:        "default",
				DisplayName: "GitHub Default",
				ConfigJSON:  json.RawMessage(`{"apiBaseURL":"https://api.github.com","allowedRepos":["acme/demo"]}`),
				Enabled:     true,
			},
			{
				Type:        "http",
				Name:        "default",
				DisplayName: "HTTP Default",
				ConfigJSON:  json.RawMessage(`{"allowedHosts":["localhost:18080"],"allowedMethods":["GET"]}`),
				Enabled:     true,
			},
		},
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	connectors, err := st.ListConnectors(context.Background(), workspaces[0].ID)
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	if len(connectors) != 3 {
		t.Fatalf("expected 3 connectors, got %d", len(connectors))
	}
}

func TestMemoryListToolCallsPageAppliesPaginationAndFilters(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
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
	workspaceID := workspaces[0].ID
	tool, err := st.GetToolByKey(context.Background(), workspaceID, "mock.echo")
	if err != nil {
		t.Fatalf("get mock.echo: %v", err)
	}

	for i := 0; i < 15; i++ {
		if _, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
			WorkspaceID:        workspaceID,
			RequestID:          "req-page-" + uuid.NewString(),
			ActorSubject:       "subject",
			ToolID:             tool.ID,
			ToolKey:            tool.Key(),
			Status:             "success",
			RiskLevel:          "low",
			PolicyDecision:     "allow",
			DurationMs:         1,
			InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
			TraceID:            "trace-page",
		}); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}

	page, err := st.ListToolCallsPage(context.Background(), workspaceID, model.ToolCallQuery{
		Tool:     "mock.echo",
		Statuses: []string{"success"},
		Page:     2,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list tool calls page: %v", err)
	}
	if page.Total != 15 || page.Page != 2 || page.PageSize != 10 {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	if len(page.Items) != 5 {
		t.Fatalf("expected 5 calls on page 2, got %d", len(page.Items))
	}
}

func assertUpdateToolCallClearsExecutionInput(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Test Workspace " + suffix,
		Slug:                  "test-workspace-" + suffix,
		ZitadelOrganizationID: "org-test-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tool, err := st.CreateTool(ctx, model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mock",
		Name:             "write",
		DisplayName:      "Mock Write",
		OperationType:    "write",
		RiskLevel:        "low",
		RequiresApproval: true,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}
	call, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-clear-execution-input-" + suffix,
		ActorSubject:       "subject",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "low",
		PolicyDecision:     "require_approval",
		InputRedactedJSON:  json.RawMessage(`{"token":"[REDACTED]"}`),
		InputExecutionJSON: json.RawMessage(`{"token":"raw-secret"}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		TraceID:            "trace-clear-execution-input",
	})
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	assertJSONEqual(t, call.InputExecutionJSON, `{"token":"raw-secret"}`)

	updated, err := st.UpdateToolCall(ctx, workspace.ID, call.ID, model.UpdateToolCallInput{
		Status:             "rejected",
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		TraceID:            call.TraceID,
	})
	if err != nil {
		t.Fatalf("update tool call: %v", err)
	}
	assertJSONEqual(t, updated.InputExecutionJSON, `{}`)
}

func assertToolCallExplanationPersists(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Explanation Workspace " + suffix,
		Slug:                  "explanation-workspace-" + suffix,
		ZitadelOrganizationID: "org-explanation-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tool, err := st.CreateTool(ctx, model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "agent_guard",
		Name:             "evaluate",
		DisplayName:      "Agent Guard Evaluate",
		OperationType:    "write",
		RiskLevel:        "high",
		RequiresApproval: true,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}

	explanation := &model.ToolCallExplanation{
		TargetCategory: "sensitive",
		RiskLevel:      "high",
		MatchedRule:    "agent-guard-sensitive-target-requires-approval",
		Signals:        []string{"Windows Startup persistence path", "High-risk local action"},
	}
	call, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-explanation-" + suffix,
		ActorSubject:       "subject",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "high",
		PolicyDecision:     "require_approval",
		ApprovalID:         "approval-" + suffix,
		InputRedactedJSON:  json.RawMessage(`{"target":"[REDACTED]"}`),
		InputExecutionJSON: json.RawMessage(`{"target":"sensitive.ps1"}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		Explanation:        explanation,
		TraceID:            "trace-explanation",
	})
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	assertToolCallExplanationEqual(t, call.Explanation, explanation)

	got, err := st.GetToolCallByID(ctx, workspace.ID, call.ID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	assertToolCallExplanationEqual(t, got.Explanation, explanation)

	updated, err := st.UpdateToolCall(ctx, workspace.ID, call.ID, model.UpdateToolCallInput{
		Status:             "success",
		DurationMs:         12,
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
		ErrorMessage:       "",
		TraceID:            call.TraceID,
	})
	if err != nil {
		t.Fatalf("update tool call: %v", err)
	}
	assertToolCallExplanationEqual(t, updated.Explanation, explanation)

	page, err := st.ListToolCallsPage(ctx, workspace.ID, model.ToolCallQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list tool calls page: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 tool call in page, got %d", len(page.Items))
	}
	assertToolCallExplanationEqual(t, page.Items[0].Explanation, explanation)
}

func assertUpdateToolMetadataPreservesDisabledState(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Metadata Workspace " + suffix,
		Slug:                  "metadata-" + suffix,
		ZitadelOrganizationID: "org-metadata-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tool, err := st.CreateTool(ctx, model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mcp_metadata",
		Name:             "create_note",
		DisplayName:      "Create Note",
		Description:      "Original description",
		OperationType:    "create",
		RiskLevel:        "medium",
		RequiresApproval: true,
		InputSchemaJSON:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}
	if _, err := st.UpdateTool(ctx, workspace.ID, tool.ID, model.UpdateToolInput{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable tool: %v", err)
	}

	requiresApproval := false
	updated, err := st.UpdateTool(ctx, workspace.ID, tool.ID, model.UpdateToolInput{
		DisplayName:      "Create Note V2",
		Description:      "Updated metadata from sync",
		OperationType:    "read",
		RiskLevel:        "low",
		RequiresApproval: &requiresApproval,
		InputSchemaJSON:  json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"}}}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
	})
	if err != nil {
		t.Fatalf("update tool metadata: %v", err)
	}

	if updated.Enabled {
		t.Fatalf("metadata update must preserve disabled state, got %+v", updated)
	}
	if updated.DisplayName != "Create Note V2" ||
		updated.Description != "Updated metadata from sync" ||
		updated.OperationType != "read" ||
		updated.RiskLevel != "low" ||
		updated.RequiresApproval {
		t.Fatalf("metadata fields were not updated consistently: %+v", updated)
	}
	if !strings.Contains(string(updated.InputSchemaJSON), `"body"`) {
		t.Fatalf("expected input schema update, got %s", updated.InputSchemaJSON)
	}
	if !strings.Contains(string(updated.OutputSchemaJSON), `"ok"`) {
		t.Fatalf("expected output schema update, got %s", updated.OutputSchemaJSON)
	}
}

func assertSecretCRUDAndValidation(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Secret Workspace " + suffix,
		Slug:                  "secret-" + suffix,
		ZitadelOrganizationID: "org-secret-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	otherWorkspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Secret Workspace 2 " + suffix,
		Slug:                  "secret-2-" + suffix,
		ZitadelOrganizationID: "org-secret-2-" + suffix,
	})
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}

	secret, err := st.CreateSecret(ctx, model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "github_token",
		Description:    "GitHub token",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "GITHUB_TOKEN_ENV",
		Metadata:       json.RawMessage(`{"scope":"github"}`),
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if !secret.Enabled || secret.Name != "github_token" || secret.ValueRef != "GITHUB_TOKEN_ENV" {
		t.Fatalf("unexpected created secret: %+v", secret)
	}

	gotByID, err := st.GetSecretByID(ctx, workspace.ID, secret.ID)
	if err != nil {
		t.Fatalf("get secret by id: %v", err)
	}
	if gotByID.ID != secret.ID || gotByID.Name != secret.Name || gotByID.Description != secret.Description {
		t.Fatalf("unexpected secret by id: %+v", gotByID)
	}

	gotByName, err := st.GetSecretByName(ctx, workspace.ID, "GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("get secret by name: %v", err)
	}
	if gotByName.ID != secret.ID {
		t.Fatalf("expected case-insensitive lookup, got %+v", gotByName)
	}

	if _, err := st.CreateSecret(ctx, model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "github_token",
		Description:    "duplicate",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "GITHUB_TOKEN_ENV_2",
		Metadata:       json.RawMessage(`{}`),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate secret conflict, got %v", err)
	}

	duplicateOtherWorkspace, err := st.CreateSecret(ctx, model.CreateSecretInput{
		WorkspaceID:    otherWorkspace.ID,
		WorkspaceOrgID: otherWorkspace.ZitadelOrganizationID,
		Name:           "github_token",
		Description:    "duplicate in other workspace",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "GITHUB_TOKEN_ENV_2",
		Metadata:       json.RawMessage(`{"scope":"github"}`),
	})
	if err != nil {
		t.Fatalf("create secret in other workspace: %v", err)
	}
	if duplicateOtherWorkspace.ID == "" {
		t.Fatalf("expected secret id in other workspace")
	}

	if _, err := st.CreateSecret(ctx, model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "invalid_type",
		Description:    "invalid type",
		Enabled:        true,
		SecretType:     "unsupported",
		ValueSource:    "env",
		ValueRef:       "GITHUB_TOKEN_ENV",
		Metadata:       json.RawMessage(`{}`),
	}); err == nil {
		t.Fatalf("expected invalid secret type to be rejected")
	}

	if _, err := st.CreateSecret(ctx, model.CreateSecretInput{
		WorkspaceID:    workspace.ID,
		WorkspaceOrgID: workspace.ZitadelOrganizationID,
		Name:           "invalid_ref",
		Description:    "invalid ref",
		Enabled:        true,
		SecretType:     "token",
		ValueSource:    "env",
		ValueRef:       "bad-ref",
		Metadata:       json.RawMessage(`{}`),
	}); err == nil {
		t.Fatalf("expected invalid secret ref to be rejected")
	}

	disabled := false
	updated, err := st.UpdateSecret(ctx, workspace.ID, secret.ID, model.UpdateSecretInput{
		Name:        "github_token",
		Description: "Updated GitHub token",
		Enabled:     &disabled,
		SecretType:  "token",
		ValueSource: "env",
		ValueRef:    "GITHUB_TOKEN_ENV_2",
		Metadata:    json.RawMessage(`{"scope":"github","updated":true}`),
	})
	if err != nil {
		t.Fatalf("update secret: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("expected secret to be disabled after update, got %+v", updated)
	}
	if updated.Description != "Updated GitHub token" || updated.ValueRef != "GITHUB_TOKEN_ENV_2" {
		t.Fatalf("unexpected updated secret: %+v", updated)
	}

	gotByID, err = st.GetSecretByID(ctx, workspace.ID, secret.ID)
	if err != nil {
		t.Fatalf("get updated secret by id: %v", err)
	}
	if gotByID.Enabled {
		t.Fatalf("expected disabled secret from get by id, got %+v", gotByID)
	}

	items, err := st.ListSecrets(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	if len(items) != 1 || items[0].ID != secret.ID {
		t.Fatalf("expected one secret in workspace, got %+v", items)
	}

	if err := st.DeleteSecret(ctx, workspace.ID, secret.ID); err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	if _, err := st.GetSecretByID(ctx, workspace.ID, secret.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted secret to be missing, got %v", err)
	}
	if _, err := st.GetSecretByName(ctx, workspace.ID, "github_token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted secret lookup by name to fail, got %v", err)
	}

	remaining, err := st.ListSecrets(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list secrets after delete: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no secrets after delete, got %+v", remaining)
	}

	otherSecrets, err := st.ListSecrets(ctx, otherWorkspace.ID)
	if err != nil {
		t.Fatalf("list other workspace secrets: %v", err)
	}
	if len(otherSecrets) != 1 || otherSecrets[0].ID != duplicateOtherWorkspace.ID {
		t.Fatalf("expected isolated secret in other workspace, got %+v", otherSecrets)
	}
}

func assertPolicyRuleCRUDAndOrdering(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Policy Workspace " + suffix,
		Slug:                  "policy-" + suffix,
		ZitadelOrganizationID: "org-policy-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	otherWorkspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Other Policy Workspace " + suffix,
		Slug:                  "policy-other-" + suffix,
		ZitadelOrganizationID: "org-policy-other-" + suffix,
	})
	if err != nil {
		t.Fatalf("create other workspace: %v", err)
	}

	late, err := st.CreatePolicyRule(ctx, model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "late rule",
		Description:     "created first but lower precedence",
		Enabled:         true,
		Priority:        50,
		Effect:          "allow",
		ConnectorType:   "",
		ToolNamePattern: "",
		OperationType:   "",
		RiskLevel:       "",
		ResourcePattern: "",
		Reason:          "late",
	})
	if err != nil {
		t.Fatalf("create late policy rule: %v", err)
	}
	early, err := st.CreatePolicyRule(ctx, model.CreatePolicyRuleInput{
		WorkspaceID:     workspace.ID,
		Name:            "early rule",
		Description:     "higher precedence",
		Enabled:         true,
		Priority:        10,
		Effect:          "deny",
		ConnectorType:   "mock",
		ToolNamePattern: "mock.echo",
		OperationType:   "mock",
		RiskLevel:       "low",
		ResourcePattern: "*",
		Reason:          "early",
	})
	if err != nil {
		t.Fatalf("create early policy rule: %v", err)
	}
	if _, err := st.CreatePolicyRule(ctx, model.CreatePolicyRuleInput{
		WorkspaceID:     otherWorkspace.ID,
		Name:            "other workspace rule",
		Enabled:         true,
		Priority:        1,
		Effect:          "deny",
		ConnectorType:   "*",
		ToolNamePattern: "*",
		OperationType:   "*",
		RiskLevel:       "*",
		ResourcePattern: "*",
	}); err != nil {
		t.Fatalf("create other workspace rule: %v", err)
	}

	items, err := st.ListPolicyRules(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list policy rules: %v", err)
	}
	if len(items) != 2 || items[0].ID != early.ID || items[1].ID != late.ID {
		t.Fatalf("expected priority ordering scoped to workspace, got %+v", items)
	}
	if late.ConnectorType != "*" || late.ToolNamePattern != "*" || late.OperationType != "*" || late.RiskLevel != "*" || late.ResourcePattern != "*" {
		t.Fatalf("empty match fields must normalize to wildcard, got %+v", late)
	}

	fetched, err := st.GetPolicyRuleByID(ctx, workspace.ID, early.ID)
	if err != nil {
		t.Fatalf("get policy rule: %v", err)
	}
	if fetched.Name != early.Name || fetched.Effect != "deny" {
		t.Fatalf("unexpected fetched policy rule: %+v", fetched)
	}
	if _, err := st.GetPolicyRuleByID(ctx, otherWorkspace.ID, early.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace get must be not found, got %v", err)
	}

	disabled := false
	updatedPriority := 5
	updated, err := st.UpdatePolicyRule(ctx, workspace.ID, early.ID, model.UpdatePolicyRuleInput{
		Name:            "early rule updated",
		Description:     "updated",
		Enabled:         &disabled,
		Priority:        &updatedPriority,
		Effect:          "require_approval",
		ConnectorType:   "github",
		ToolNamePattern: "github.*",
		OperationType:   "create",
		RiskLevel:       "medium",
		ResourcePattern: "acme/*",
		Reason:          "updated reason",
	})
	if err != nil {
		t.Fatalf("update policy rule: %v", err)
	}
	if updated.Enabled || updated.Priority != 5 || updated.Effect != "require_approval" || updated.ToolNamePattern != "github.*" {
		t.Fatalf("unexpected updated policy rule: %+v", updated)
	}

	if err := st.DeletePolicyRule(ctx, workspace.ID, late.ID); err != nil {
		t.Fatalf("delete policy rule: %v", err)
	}
	items, err = st.ListPolicyRules(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(items) != 1 || items[0].ID != early.ID {
		t.Fatalf("unexpected list after delete: %+v", items)
	}
	if err := st.DeletePolicyRule(ctx, workspace.ID, late.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete should be not found, got %v", err)
	}
}

func assertToolCallExplanationEqual(t *testing.T, got, want *model.ToolCallExplanation) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected explanation: got=%+v want=%+v", got, want)
	}
}

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()

	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got value is not valid JSON: %s err=%v", got, err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want value is not valid JSON: %s err=%v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("unexpected JSON value: got=%s want=%s", got, want)
	}
}

func assertApprovalRequestMetadataPersists(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval Metadata Workspace " + suffix,
		Slug:                  "approval-metadata-" + suffix,
		ZitadelOrganizationID: "org-approval-metadata-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	input := model.CreateApprovalRequestInput{
		WorkspaceID:          workspace.ID,
		ToolKey:              "agent_guard.evaluate",
		ToolDisplayName:      "Agent Guard Evaluate",
		RequestedBy:          "requester",
		Reason:               "local action requires approval",
		Fingerprint:          "fingerprint-" + suffix,
		Adapter:              "codex",
		ActionType:           "write",
		Target:               `C:\Users\demo\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\run.ps1`,
		CanonicalTarget:      `c:\users\demo\appdata\roaming\microsoft\windows\start menu\programs\startup\run.ps1`,
		ContentEncoding:      "plain",
		ContentHash:          "content-hash-" + suffix,
		ScriptHash:           "script-hash-" + suffix,
		ResolvedFileIdentity: "file-id-" + suffix,
		ParentIdentity:       "parent-id-" + suffix,
		DecisionPayloadJSON:  json.RawMessage(`{"content":"secret","ticketId":"ticket-123"}`),
		TTL:                  time.Hour,
	}

	approval, err := st.CreateApprovalRequest(ctx, input)
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	if approval.Fingerprint != input.Fingerprint || approval.Adapter != input.Adapter || approval.ActionType != input.ActionType {
		t.Fatalf("unexpected approval metadata: %+v", approval)
	}
	if approval.Target != input.Target || approval.CanonicalTarget != input.CanonicalTarget {
		t.Fatalf("unexpected approval target metadata: %+v", approval)
	}
	if approval.ContentEncoding != input.ContentEncoding || approval.ContentHash != input.ContentHash || approval.ScriptHash != input.ScriptHash {
		t.Fatalf("unexpected approval content metadata: %+v", approval)
	}
	if approval.ResolvedFileIdentity != input.ResolvedFileIdentity || approval.ParentIdentity != input.ParentIdentity {
		t.Fatalf("unexpected approval identity metadata: %+v", approval)
	}
	if approval.DecisionPayloadJSON == nil {
		t.Fatalf("expected decision payload to persist, got nil")
	}
	assertJSONEqual(t, approval.DecisionPayloadJSON, `{"content":"secret","ticketId":"ticket-123"}`)
	if approval.Status != "pending" {
		t.Fatalf("expected new approval to be pending, got %+v", approval)
	}

	dupInput := input
	dupInput.Reason = "another request"
	if _, err := st.CreateApprovalRequest(ctx, dupInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate fingerprint conflict, got %v", err)
	}

	items, err := st.ListApprovalRequests(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 approval in list, got %d", len(items))
	}
	assertApprovalRequestMatches(t, items[0], input)

	got, err := st.GetApprovalRequestByID(ctx, workspace.ID, approval.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	assertApprovalRequestMatches(t, got, input)
}

func assertApprovalRequestFingerprintAllowsNewTicketAfterInactive(t *testing.T, st Store) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval Fingerprint Workspace " + suffix,
		Slug:                  "approval-fingerprint-" + suffix,
		ZitadelOrganizationID: "org-approval-fingerprint-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	input := model.CreateApprovalRequestInput{
		WorkspaceID:     workspace.ID,
		ToolKey:         "agent_guard.evaluate",
		ToolDisplayName: "Agent Guard Evaluate",
		RequestedBy:     "requester",
		Reason:          "local action requires approval",
		Fingerprint:     "fingerprint-" + suffix,
		TTL:             time.Hour,
	}

	first, err := st.CreateApprovalRequest(ctx, input)
	if err != nil {
		t.Fatalf("create first approval: %v", err)
	}
	if _, err := st.CreateApprovalRequest(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("active duplicate fingerprint must conflict, got %v", err)
	}
	if _, err := st.TransitionApprovalRequest(ctx, workspace.ID, first.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	}); err != nil {
		t.Fatalf("approve first approval: %v", err)
	}
	if _, err := st.CreateApprovalRequest(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved duplicate fingerprint must conflict while remembered, got %v", err)
	}
	if _, err := st.TransitionApprovalRequest(ctx, workspace.ID, first.ID, "approved", model.UpdateApprovalRequestInput{
		Status:     "consumed",
		ReviewedBy: "owner",
	}); err != nil {
		t.Fatalf("consume first approval: %v", err)
	}
	second, err := st.CreateApprovalRequest(ctx, input)
	if err != nil {
		t.Fatalf("inactive fingerprint should allow a fresh ticket: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected fresh approval id, got %q", second.ID)
	}
}

func assertApprovalRequestMatches(t *testing.T, approval model.ApprovalRequest, input model.CreateApprovalRequestInput) {
	t.Helper()

	if approval.WorkspaceID != input.WorkspaceID || approval.ToolKey != input.ToolKey || approval.ToolDisplayName != input.ToolDisplayName {
		t.Fatalf("unexpected approval core fields: %+v", approval)
	}
	if approval.Fingerprint != input.Fingerprint || approval.Adapter != input.Adapter || approval.ActionType != input.ActionType {
		t.Fatalf("unexpected approval metadata: %+v", approval)
	}
	if approval.Target != input.Target || approval.CanonicalTarget != input.CanonicalTarget {
		t.Fatalf("unexpected approval target metadata: %+v", approval)
	}
	if approval.ContentEncoding != input.ContentEncoding || approval.ContentHash != input.ContentHash || approval.ScriptHash != input.ScriptHash {
		t.Fatalf("unexpected approval content metadata: %+v", approval)
	}
	if approval.ResolvedFileIdentity != input.ResolvedFileIdentity || approval.ParentIdentity != input.ParentIdentity {
		t.Fatalf("unexpected approval identity metadata: %+v", approval)
	}
	assertJSONEqual(t, approval.DecisionPayloadJSON, `{"content":"secret","ticketId":"ticket-123"}`)
}

func boolPtr(value bool) *bool {
	return &value
}

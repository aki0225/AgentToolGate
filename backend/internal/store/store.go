package store

import (
	"context"
	"errors"

	"agenttoolgate/backend/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrExpired  = errors.New("expired")
)

type Store interface {
	Ping(ctx context.Context) error
	Bootstrap(ctx context.Context, input model.BootstrapInput) error

	ListWorkspaces(ctx context.Context) ([]model.Workspace, error)
	GetWorkspaceBySlug(ctx context.Context, slug string) (model.Workspace, error)
	GetWorkspaceByOrganizationID(ctx context.Context, organizationID string) (model.Workspace, error)
	CreateWorkspace(ctx context.Context, input model.CreateWorkspaceInput) (model.Workspace, error)

	UpsertUser(ctx context.Context, input model.UpsertUserInput) (model.User, error)

	ListTools(ctx context.Context, workspaceID string) ([]model.Tool, error)
	GetToolByID(ctx context.Context, workspaceID, toolID string) (model.Tool, error)
	GetToolByKey(ctx context.Context, workspaceID, key string) (model.Tool, error)
	CreateTool(ctx context.Context, input model.CreateToolInput) (model.Tool, error)
	UpdateTool(ctx context.Context, workspaceID, toolID string, input model.UpdateToolInput) (model.Tool, error)

	ListToolCalls(ctx context.Context, workspaceID string) ([]model.ToolCall, error)
	ListToolCallsPage(ctx context.Context, workspaceID string, query model.ToolCallQuery) (model.ToolCallPage, error)
	GetToolCallByID(ctx context.Context, workspaceID, callID string) (model.ToolCall, error)
	GetToolCallByApprovalID(ctx context.Context, workspaceID, approvalID string) (model.ToolCall, error)
	CreateToolCall(ctx context.Context, input model.CreateToolCallInput) (model.ToolCall, error)
	UpdateToolCall(ctx context.Context, workspaceID, callID string, input model.UpdateToolCallInput) (model.ToolCall, error)

	ListConnectors(ctx context.Context, workspaceID string) ([]model.Connector, error)
	GetConnectorByID(ctx context.Context, workspaceID, connectorID string) (model.Connector, error)
	CreateConnector(ctx context.Context, input model.CreateConnectorInput) (model.Connector, error)
	UpdateConnector(ctx context.Context, workspaceID, connectorID string, input model.UpdateConnectorInput) (model.Connector, error)

	ListSecrets(ctx context.Context, workspaceID string) ([]model.Secret, error)
	GetSecretByID(ctx context.Context, workspaceID, secretID string) (model.Secret, error)
	GetSecretByName(ctx context.Context, workspaceID, name string) (model.Secret, error)
	CreateSecret(ctx context.Context, input model.CreateSecretInput) (model.Secret, error)
	UpdateSecret(ctx context.Context, workspaceID, secretID string, input model.UpdateSecretInput) (model.Secret, error)
	DeleteSecret(ctx context.Context, workspaceID, secretID string) error

	ListPolicyRules(ctx context.Context, workspaceID string) ([]model.PolicyRule, error)
	GetPolicyRuleByID(ctx context.Context, workspaceID, ruleID string) (model.PolicyRule, error)
	CreatePolicyRule(ctx context.Context, input model.CreatePolicyRuleInput) (model.PolicyRule, error)
	UpdatePolicyRule(ctx context.Context, workspaceID, ruleID string, input model.UpdatePolicyRuleInput) (model.PolicyRule, error)
	DeletePolicyRule(ctx context.Context, workspaceID, ruleID string) error

	ListApprovalRequests(ctx context.Context, workspaceID string) ([]model.ApprovalRequest, error)
	GetApprovalRequestByID(ctx context.Context, workspaceID, approvalID string) (model.ApprovalRequest, error)
	CreateApprovalRequest(ctx context.Context, input model.CreateApprovalRequestInput) (model.ApprovalRequest, error)
	UpdateApprovalRequest(ctx context.Context, workspaceID, approvalID string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error)
	TransitionApprovalRequest(ctx context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error)
}

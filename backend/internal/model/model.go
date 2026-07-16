package model

import (
	"encoding/json"
	"time"
)

type Workspace struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Slug                  string    `json:"slug"`
	ZitadelOrganizationID string    `json:"zitadelOrganizationId"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type User struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspaceId"`
	ZitadelUserID string    `json:"zitadelUserId"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Tool struct {
	ID               string          `json:"id"`
	WorkspaceID      string          `json:"workspaceId"`
	Namespace        string          `json:"namespace"`
	Name             string          `json:"name"`
	DisplayName      string          `json:"displayName"`
	Description      string          `json:"description"`
	OperationType    string          `json:"operationType"`
	RiskLevel        string          `json:"riskLevel"`
	RequiresApproval bool            `json:"requiresApproval"`
	InputSchemaJSON  json.RawMessage `json:"inputSchemaJson"`
	OutputSchemaJSON json.RawMessage `json:"outputSchemaJson"`
	Enabled          bool            `json:"enabled"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

func (t Tool) Key() string {
	return t.Namespace + "." + t.Name
}

type ToolCall struct {
	ID                 string               `json:"id"`
	RequestID          string               `json:"requestId"`
	WorkspaceID        string               `json:"workspaceId"`
	ActorID            string               `json:"actorId,omitempty"`
	ActorSubject       string               `json:"actorSubject"`
	ActorEmail         string               `json:"actorEmail,omitempty"`
	ActorName          string               `json:"actorName,omitempty"`
	ToolID             string               `json:"toolId"`
	ToolKey            string               `json:"toolKey"`
	Status             string               `json:"status"`
	RiskLevel          string               `json:"riskLevel"`
	PolicyDecision     string               `json:"policyDecision"`
	ApprovalID         string               `json:"approvalId,omitempty"`
	ApprovalStatus     string               `json:"approvalStatus,omitempty"`
	DurationMs         int64                `json:"durationMs"`
	InputRedactedJSON  json.RawMessage      `json:"inputRedactedJson"`
	InputExecutionJSON json.RawMessage      `json:"-"`
	OutputRedactedJSON json.RawMessage      `json:"outputRedactedJson"`
	Explanation        *ToolCallExplanation `json:"explanation,omitempty"`
	ErrorMessage       string               `json:"errorMessage,omitempty"`
	TraceID            string               `json:"traceId"`
	CreatedAt          time.Time            `json:"createdAt"`
}

type ToolCallExplanation struct {
	TargetCategory string   `json:"targetCategory"`
	RiskLevel      string   `json:"riskLevel"`
	MatchedRule    string   `json:"matchedRule"`
	Signals        []string `json:"signals"`
}

type PolicyRule struct {
	ID              string    `json:"id"`
	WorkspaceID     string    `json:"workspaceId"`
	WorkspaceOrgID  string    `json:"workspaceOrgId,omitempty"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Enabled         bool      `json:"enabled"`
	Priority        int       `json:"priority"`
	Effect          string    `json:"effect"`
	ConnectorType   string    `json:"connectorType"`
	ToolNamePattern string    `json:"toolNamePattern"`
	OperationType   string    `json:"operationType"`
	RiskLevel       string    `json:"riskLevel"`
	ResourcePattern string    `json:"resourcePattern"`
	Reason          string    `json:"reason"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ToolCallPage struct {
	Items    []ToolCall `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type ToolCallQuery struct {
	Tool     string
	Statuses []string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}

type ApprovalRequest struct {
	ID                   string          `json:"id"`
	WorkspaceID          string          `json:"workspaceId"`
	ToolKey              string          `json:"toolKey"`
	ToolDisplayName      string          `json:"toolDisplayName"`
	Status               string          `json:"status"`
	RequestedBy          string          `json:"requestedBy"`
	ReviewedBy           string          `json:"reviewedBy,omitempty"`
	Reason               string          `json:"reason,omitempty"`
	Fingerprint          string          `json:"fingerprint,omitempty"`
	Adapter              string          `json:"adapter,omitempty"`
	ActionType           string          `json:"actionType,omitempty"`
	Target               string          `json:"target,omitempty"`
	CanonicalTarget      string          `json:"canonicalTarget,omitempty"`
	ContentEncoding      string          `json:"contentEncoding,omitempty"`
	ContentHash          string          `json:"contentHash,omitempty"`
	ScriptHash           string          `json:"scriptHash,omitempty"`
	ResolvedFileIdentity string          `json:"resolvedFileIdentity,omitempty"`
	ParentIdentity       string          `json:"parentIdentity,omitempty"`
	DecisionPayloadJSON  json.RawMessage `json:"decisionPayloadJson,omitempty"`
	ExpiresAt            time.Time       `json:"expiresAt"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

type Connector struct {
	ID          string          `json:"id"`
	WorkspaceID string          `json:"workspaceId"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	ConfigJSON  json.RawMessage `json:"configJson"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type Secret struct {
	ID             string          `json:"id"`
	WorkspaceID    string          `json:"workspaceId"`
	WorkspaceOrgID string          `json:"workspaceOrgId,omitempty"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Enabled        bool            `json:"enabled"`
	SecretType     string          `json:"secretType"`
	ValueSource    string          `json:"valueSource"`
	ValueRef       string          `json:"valueRef"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Bindings       []SecretBinding `json:"bindings,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

type SecretBinding struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Field  string `json:"field"`
}

type SecretUsage struct {
	Kind                 string `json:"kind"`
	ConnectorID          string `json:"connectorId,omitempty"`
	ConnectorType        string `json:"connectorType,omitempty"`
	ConnectorName        string `json:"connectorName,omitempty"`
	ConnectorDisplayName string `json:"connectorDisplayName,omitempty"`
	Field                string `json:"field"`
	Target               string `json:"target"`
}

type SecretUsageResponse struct {
	Secret              Secret        `json:"secret"`
	Usages              []SecretUsage `json:"usages"`
	CanDelete           bool          `json:"canDelete"`
	DeleteBlockedReason string        `json:"deleteBlockedReason,omitempty"`
}

type BootstrapConnectorInput struct {
	Type        string
	Name        string
	DisplayName string
	ConfigJSON  json.RawMessage
	Enabled     bool
}

type DashboardSummary struct {
	WorkspaceID          string           `json:"workspaceId"`
	TotalCalls           int64            `json:"totalCalls"`
	SuccessCalls         int64            `json:"successCalls"`
	FailedCalls          int64            `json:"failedCalls"`
	PendingApprovalCalls int64            `json:"pendingApprovalCalls"`
	AverageDurationMs    float64          `json:"averageDurationMs"`
	TopTools             []DashboardItem  `json:"topTools"`
	TopErrors            []DashboardError `json:"topErrors"`
}

type DashboardItem struct {
	ToolKey string `json:"toolKey"`
	Count   int64  `json:"count"`
}

type DashboardError struct {
	Message string `json:"message"`
	Count   int64  `json:"count"`
}

type BootstrapInput struct {
	WorkspaceName           string
	WorkspaceSlug           string
	WorkspaceOrganizationID string
	Connectors              []BootstrapConnectorInput
}

type CreateWorkspaceInput struct {
	Name                  string
	Slug                  string
	ZitadelOrganizationID string
}

type UpsertUserInput struct {
	WorkspaceID   string
	ZitadelUserID string
	Email         string
	Name          string
	Role          string
}

type CreateToolInput struct {
	WorkspaceID      string
	Namespace        string
	Name             string
	DisplayName      string
	Description      string
	OperationType    string
	RiskLevel        string
	RequiresApproval bool
	InputSchemaJSON  json.RawMessage
	OutputSchemaJSON json.RawMessage
	Enabled          bool
}

type UpdateToolInput struct {
	DisplayName      string
	Description      string
	OperationType    string
	RiskLevel        string
	RequiresApproval *bool
	InputSchemaJSON  json.RawMessage
	OutputSchemaJSON json.RawMessage
	Enabled          *bool
}

type CreatePolicyRuleInput struct {
	WorkspaceID     string
	Name            string
	Description     string
	Enabled         bool
	Priority        int
	Effect          string
	ConnectorType   string
	ToolNamePattern string
	OperationType   string
	RiskLevel       string
	ResourcePattern string
	Reason          string
}

type UpdatePolicyRuleInput struct {
	Name            string
	Description     string
	Enabled         *bool
	Priority        *int
	Effect          string
	ConnectorType   string
	ToolNamePattern string
	OperationType   string
	RiskLevel       string
	ResourcePattern string
	Reason          string
}

type CreateToolCallInput struct {
	WorkspaceID        string
	RequestID          string
	ActorID            string
	ActorSubject       string
	ActorEmail         string
	ActorName          string
	ToolID             string
	ToolKey            string
	Status             string
	RiskLevel          string
	PolicyDecision     string
	ApprovalID         string
	DurationMs         int64
	InputRedactedJSON  json.RawMessage
	InputExecutionJSON json.RawMessage
	OutputRedactedJSON json.RawMessage
	Explanation        *ToolCallExplanation
	ErrorMessage       string
	TraceID            string
}

type CreateConnectorInput struct {
	WorkspaceID string
	Type        string
	Name        string
	DisplayName string
	ConfigJSON  json.RawMessage
	Enabled     bool
}

type UpdateConnectorInput struct {
	DisplayName string
	ConfigJSON  json.RawMessage
	Enabled     *bool
}

type CreateSecretInput struct {
	WorkspaceID    string
	WorkspaceOrgID string
	Name           string
	Description    string
	Enabled        bool
	SecretType     string
	ValueSource    string
	ValueRef       string
	Metadata       json.RawMessage
}

type UpdateSecretInput struct {
	Name        string
	Description string
	Enabled     *bool
	SecretType  string
	ValueSource string
	ValueRef    string
	Metadata    json.RawMessage
}

type CreateApprovalRequestInput struct {
	WorkspaceID          string
	ToolKey              string
	ToolDisplayName      string
	RequestedBy          string
	Reason               string
	Fingerprint          string
	Adapter              string
	ActionType           string
	Target               string
	CanonicalTarget      string
	ContentEncoding      string
	ContentHash          string
	ScriptHash           string
	ResolvedFileIdentity string
	ParentIdentity       string
	DecisionPayloadJSON  json.RawMessage
	TTL                  time.Duration
}

type UpdateApprovalRequestInput struct {
	Status     string
	ReviewedBy string
	Reason     string
}

type UpdateToolCallInput struct {
	Status             string
	DurationMs         int64
	InputExecutionJSON json.RawMessage
	OutputRedactedJSON json.RawMessage
	ErrorMessage       string
	TraceID            string
}

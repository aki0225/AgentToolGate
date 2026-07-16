export interface Workspace {
  id: string;
  name: string;
  slug: string;
  zitadelOrganizationId: string;
  createdAt: string;
  updatedAt: string;
}

export interface User {
  id: string;
  workspaceId: string;
  zitadelUserId: string;
  email: string;
  name: string;
  role: string;
  createdAt: string;
  updatedAt: string;
}

export interface Tool {
  id: string;
  workspaceId: string;
  namespace: string;
  name: string;
  displayName: string;
  description: string;
  operationType: string;
  riskLevel: string;
  requiresApproval: boolean;
  inputSchemaJson: unknown;
  outputSchemaJson: unknown;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ToolCall {
  id: string;
  requestId: string;
  workspaceId: string;
  actorId: string;
  actorSubject: string;
  actorEmail: string;
  actorName: string;
  toolId: string;
  toolKey: string;
  status: string;
  riskLevel: string;
  policyDecision: string;
  approvalId?: string;
  approvalStatus?: string;
  durationMs: number;
  inputRedactedJson: unknown;
  outputRedactedJson: unknown;
  explanation?: ToolCallExplanation;
  errorMessage: string;
  traceId: string;
  createdAt: string;
}

export interface ToolCallExplanation {
  targetCategory?: string;
  riskLevel?: string;
  matchedRule?: string;
  signals?: string[];
}

export interface ToolCallPage {
  items: ToolCall[];
  total: number;
  page: number;
  pageSize: number;
}

export interface ApprovalRequest {
  id: string;
  workspaceId: string;
  toolKey: string;
  toolDisplayName: string;
  status: string;
  requestedBy: string;
  reviewedBy?: string;
  reason?: string;
  expiresAt: string;
  createdAt: string;
  updatedAt: string;
}

export interface ApprovalReviewInput {
  reason?: string;
}

export interface Connector {
  id: string;
  workspaceId: string;
  type: "database" | "github" | "http" | "mcp" | string;
  name: string;
  displayName: string;
  configJson: unknown;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface SecretBinding {
  kind: string;
  target: string;
  field: string;
}

export interface SecretUsage {
  kind: string;
  connectorId?: string;
  connectorType?: string;
  connectorName?: string;
  connectorDisplayName?: string;
  field: string;
  target: string;
}

export interface Secret {
  id: string;
  workspaceId: string;
  workspaceOrgId?: string;
  name: string;
  description: string;
  enabled: boolean;
  secretType: "env" | "text" | "token" | "api_key" | "oauth_like" | "generic" | string;
  valueSource: "env" | string;
  valueRef: string;
  metadata?: unknown;
  bindings?: SecretBinding[];
  createdAt: string;
  updatedAt: string;
}

export interface SecretUsageResponse {
  secret: Secret;
  usages: SecretUsage[];
  canDelete: boolean;
  deleteBlockedReason?: string;
}

export interface SecretInput {
  name: string;
  description: string;
  enabled: boolean;
  secretType: string;
  valueSource: string;
  valueRef: string;
  metadata: Record<string, unknown>;
}

export interface PolicyRule {
  id: string;
  workspaceId: string;
  workspaceOrgId?: string;
  name: string;
  description: string;
  enabled: boolean;
  priority: number;
  effect: "allow" | "deny" | "require_approval";
  connectorType: string;
  toolNamePattern: string;
  operationType: string;
  riskLevel: string;
  resourcePattern: string;
  reason: string;
  createdAt: string;
  updatedAt: string;
}

export interface PolicyRuleInput {
  name: string;
  description: string;
  enabled: boolean;
  priority: number;
  effect: PolicyRule["effect"];
  connectorType: string;
  toolNamePattern: string;
  operationType: string;
  riskLevel: string;
  resourcePattern: string;
  reason: string;
}

export interface PolicySimulationRequest {
  connectorType: string;
  toolName: string;
  operationType: string;
  riskLevel: string;
  resource: string;
  arguments?: unknown;
}

export interface PolicyEvaluationTrace {
  ruleId?: string;
  ruleName?: string;
  matched: boolean;
  decision?: string;
  reason: string;
}

export interface PolicySimulationResponse {
  decision: PolicyRule["effect"];
  matchedRuleId?: string;
  matchedRuleName?: string;
  explanation: string;
  defaulted: boolean;
  evaluationTrace: PolicyEvaluationTrace[];
}

export interface DashboardItem {
  toolKey: string;
  count: number;
}

export interface DashboardError {
  message: string;
  count: number;
}

export interface DashboardSummary {
  workspaceId: string;
  totalCalls: number;
  successCalls: number;
  failedCalls: number;
  pendingApprovalCalls: number;
  averageDurationMs: number;
  topTools: DashboardItem[];
  topErrors: DashboardError[];
}

export interface MeResponse {
  identity: {
    mode: string;
    token: string;
    subject: string;
    email: string;
    name: string;
    organizationID: string;
  };
  workspace: Workspace;
  user: User;
}

export interface ApiList<T> {
  items: T[];
}

export interface ApiPage<T> {
  items: T[];
  total: number;
  page: number;
  pageSize: number;
}

export interface ToolCallResult {
  status: string;
  result?: unknown;
  callId?: string;
  traceId?: string;
  message?: string;
  reason?: string;
  approvalId?: string;
  approvalStatus?: string;
}

export interface ApprovalActionResponse {
  approval: ApprovalRequest;
  toolCall: ToolCall;
  result?: unknown;
}

export interface DatabaseColumnSchema {
  name: string;
  dataType: string;
  masked: boolean;
}

export interface DatabaseTableSchema {
  schema: string;
  table: string;
  columns: DatabaseColumnSchema[];
}

export interface DatabaseSchemaResponse {
  datasource: string;
  tables: DatabaseTableSchema[];
  message?: string;
}

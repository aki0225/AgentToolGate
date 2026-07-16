package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

const policyRuleColumnList = `
	id, workspace_id, name, description, enabled, priority, effect, connector_type,
	tool_name_pattern, operation_type, risk_level, resource_pattern, reason, created_at, updated_at
`

const approvalRequestColumnList = `
	id, workspace_id, tool_key, tool_display_name, status, requested_by, reviewed_by, reason,
	fingerprint, adapter, action_type, target, canonical_target, content_encoding, content_hash,
	script_hash, resolved_file_identity, parent_identity, decision_payload_json, expires_at, created_at, updated_at
`

const postgresSchemaAdvisoryLockKey int64 = 41740320240609

var postgresSchemaInitMu sync.Mutex

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	store := &PostgresStore{pool: pool}
	if err := store.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) Bootstrap(ctx context.Context, input model.BootstrapInput) error {
	if err := s.ensureWorkspaceSeed(ctx, input); err != nil {
		return err
	}
	if err := s.ensureBuiltinConnectors(ctx, input.Connectors); err != nil {
		return err
	}
	return s.ensureBuiltinTools(ctx)
}

func (s *PostgresStore) ListWorkspaces(ctx context.Context) ([]model.Workspace, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, slug, zitadel_organization_id, created_at, updated_at
		FROM workspaces
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.Workspace, 0)
	for rows.Next() {
		var item model.Workspace
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.ZitadelOrganizationID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetWorkspaceBySlug(ctx context.Context, slug string) (model.Workspace, error) {
	return s.getWorkspace(ctx, `SELECT id, name, slug, zitadel_organization_id, created_at, updated_at FROM workspaces WHERE slug = $1`, strings.ToLower(strings.TrimSpace(slug)))
}

func (s *PostgresStore) GetWorkspaceByOrganizationID(ctx context.Context, organizationID string) (model.Workspace, error) {
	return s.getWorkspace(ctx, `SELECT id, name, slug, zitadel_organization_id, created_at, updated_at FROM workspaces WHERE zitadel_organization_id = $1`, strings.TrimSpace(organizationID))
}

func (s *PostgresStore) CreateWorkspace(ctx context.Context, input model.CreateWorkspaceInput) (model.Workspace, error) {
	now := time.Now().UTC()
	workspace := model.Workspace{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workspaces (id, name, slug, zitadel_organization_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, slug, zitadel_organization_id, created_at, updated_at
	`, ensureID("", "workspace"), strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug)), strings.TrimSpace(input.ZitadelOrganizationID), now, now).Scan(
		&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.ZitadelOrganizationID, &workspace.CreatedAt, &workspace.UpdatedAt,
	)
	if err != nil {
		return model.Workspace{}, mapPgErr(err)
	}
	return workspace, nil
}

func (s *PostgresStore) UpsertUser(ctx context.Context, input model.UpsertUserInput) (model.User, error) {
	now := time.Now().UTC()
	user := model.User{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (id, workspace_id, zitadel_user_id, email, name, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (workspace_id, zitadel_user_id)
		DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name, role = EXCLUDED.role, updated_at = EXCLUDED.updated_at
		RETURNING id, workspace_id, zitadel_user_id, email, name, role, created_at, updated_at
	`, ensureID("", "user"), input.WorkspaceID, input.ZitadelUserID, input.Email, input.Name, input.Role, now, now).Scan(
		&user.ID, &user.WorkspaceID, &user.ZitadelUserID, &user.Email, &user.Name, &user.Role, &user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}

func (s *PostgresStore) ListTools(ctx context.Context, workspaceID string) ([]model.Tool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		       requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		FROM tools
		WHERE workspace_id = $1 AND namespace <> 'agent_guard'
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.Tool, 0)
	for rows.Next() {
		item, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetToolByID(ctx context.Context, workspaceID, toolID string) (model.Tool, error) {
	return s.getTool(ctx, `
		SELECT id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		       requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		FROM tools
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, toolID)
}

func (s *PostgresStore) GetToolByKey(ctx context.Context, workspaceID, key string) (model.Tool, error) {
	namespace, name, ok := strings.Cut(strings.ToLower(strings.TrimSpace(key)), ".")
	if !ok {
		return model.Tool{}, ErrNotFound
	}
	return s.getTool(ctx, `
		SELECT id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		       requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		FROM tools
		WHERE workspace_id = $1 AND namespace = $2 AND name = $3
	`, workspaceID, namespace, name)
}

func (s *PostgresStore) CreateTool(ctx context.Context, input model.CreateToolInput) (model.Tool, error) {
	now := time.Now().UTC()
	tool := model.Tool{}
	enabled := input.Enabled
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = strings.TrimSpace(input.Namespace) + "." + strings.TrimSpace(input.Name)
	}
	if strings.TrimSpace(input.OperationType) == "" {
		input.OperationType = "mock"
	}
	if strings.TrimSpace(input.RiskLevel) == "" {
		input.RiskLevel = "low"
	}
	if len(input.InputSchemaJSON) == 0 {
		input.InputSchemaJSON = json.RawMessage(`{}`)
	}
	if len(input.OutputSchemaJSON) == 0 {
		input.OutputSchemaJSON = json.RawMessage(`{}`)
	}

	err := s.pool.QueryRow(ctx, `
		INSERT INTO tools (
			id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
			requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13,$14)
		RETURNING id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		          requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
	`, ensureID("", "tool"), input.WorkspaceID, strings.ToLower(strings.TrimSpace(input.Namespace)), strings.ToLower(strings.TrimSpace(input.Name)),
		input.DisplayName, input.Description, input.OperationType, input.RiskLevel, input.RequiresApproval,
		mustJSON(input.InputSchemaJSON), mustJSON(input.OutputSchemaJSON), enabled, now, now).Scan(
		&tool.ID, &tool.WorkspaceID, &tool.Namespace, &tool.Name, &tool.DisplayName, &tool.Description, &tool.OperationType, &tool.RiskLevel,
		&tool.RequiresApproval, &tool.InputSchemaJSON, &tool.OutputSchemaJSON, &tool.Enabled, &tool.CreatedAt, &tool.UpdatedAt,
	)
	if err != nil {
		return model.Tool{}, mapPgErr(err)
	}
	return tool, nil
}

func (s *PostgresStore) UpdateTool(ctx context.Context, workspaceID, toolID string, input model.UpdateToolInput) (model.Tool, error) {
	now := time.Now().UTC()
	tool := model.Tool{}
	inputSchemaJSON := strings.TrimSpace(string(input.InputSchemaJSON))
	outputSchemaJSON := strings.TrimSpace(string(input.OutputSchemaJSON))
	err := s.pool.QueryRow(ctx, `
		UPDATE tools
		SET display_name = CASE WHEN $3 = '' THEN display_name ELSE $3 END,
		    description = CASE WHEN $4 = '' THEN description ELSE $4 END,
		    operation_type = CASE WHEN $5 = '' THEN operation_type ELSE $5 END,
		    risk_level = CASE WHEN $6 = '' THEN risk_level ELSE $6 END,
		    requires_approval = COALESCE($7::boolean, requires_approval),
		    input_schema_json = CASE WHEN $8 = '' THEN input_schema_json ELSE $8::jsonb END,
		    output_schema_json = CASE WHEN $9 = '' THEN output_schema_json ELSE $9::jsonb END,
		    enabled = COALESCE($10::boolean, enabled),
		    updated_at = $11
		WHERE workspace_id = $1 AND id = $2
		RETURNING id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		          requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
	`, workspaceID, toolID, strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Description),
		strings.TrimSpace(input.OperationType), strings.TrimSpace(input.RiskLevel), input.RequiresApproval,
		inputSchemaJSON, outputSchemaJSON, input.Enabled, now).Scan(
		&tool.ID, &tool.WorkspaceID, &tool.Namespace, &tool.Name, &tool.DisplayName, &tool.Description, &tool.OperationType, &tool.RiskLevel,
		&tool.RequiresApproval, &tool.InputSchemaJSON, &tool.OutputSchemaJSON, &tool.Enabled, &tool.CreatedAt, &tool.UpdatedAt,
	)
	if err != nil {
		return model.Tool{}, mapPgErr(err)
	}
	return tool, nil
}

func (s *PostgresStore) ListToolCalls(ctx context.Context, workspaceID string) ([]model.ToolCall, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tc.id, tc.request_id, tc.workspace_id, tc.actor_id, tc.actor_subject, tc.actor_email, tc.actor_name, tc.tool_id, tc.tool_key,
		       tc.status, tc.risk_level, tc.policy_decision, tc.approval_id, tc.duration_ms, tc.input_redacted_json, tc.input_execution_json, tc.output_redacted_json,
		       tc.explanation_json, tc.error_message, tc.trace_id, tc.created_at,
		       COALESCE(ar.status, '') AS approval_status
		FROM tool_calls tc
		LEFT JOIN approval_requests ar
		       ON ar.id = tc.approval_id AND ar.workspace_id = tc.workspace_id
		WHERE tc.workspace_id = $1
		ORDER BY tc.created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.ToolCall, 0)
	for rows.Next() {
		item, err := scanToolCall(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) ListToolCallsPage(ctx context.Context, workspaceID string, query model.ToolCallQuery) (model.ToolCallPage, error) {
	page, pageSize := normalizeToolCallPage(query.Page, query.PageSize)
	whereClause, args, err := buildToolCallFilterClause(workspaceID, query)
	if err != nil {
		return model.ToolCallPage{}, err
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM tool_calls tc WHERE %s`, whereClause)
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return model.ToolCallPage{}, mapPgErr(err)
	}

	pagedArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT tc.id, tc.request_id, tc.workspace_id, tc.actor_id, tc.actor_subject, tc.actor_email, tc.actor_name, tc.tool_id, tc.tool_key,
		       tc.status, tc.risk_level, tc.policy_decision, tc.approval_id, tc.duration_ms, tc.input_redacted_json, tc.input_execution_json, tc.output_redacted_json,
		       tc.explanation_json, tc.error_message, tc.trace_id, tc.created_at,
		       COALESCE(ar.status, '') AS approval_status
		FROM tool_calls tc
		LEFT JOIN approval_requests ar
		       ON ar.id = tc.approval_id AND ar.workspace_id = tc.workspace_id
		WHERE %s
		ORDER BY tc.created_at DESC, tc.id DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, len(args)+1, len(args)+2), pagedArgs...)
	if err != nil {
		return model.ToolCallPage{}, err
	}
	defer rows.Close()

	items := make([]model.ToolCall, 0)
	for rows.Next() {
		item, err := scanToolCall(rows)
		if err != nil {
			return model.ToolCallPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return model.ToolCallPage{}, err
	}
	return model.ToolCallPage{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *PostgresStore) GetToolCallByID(ctx context.Context, workspaceID, callID string) (model.ToolCall, error) {
	return s.getToolCall(ctx, `
		SELECT tc.id, tc.request_id, tc.workspace_id, tc.actor_id, tc.actor_subject, tc.actor_email, tc.actor_name, tc.tool_id, tc.tool_key,
		       tc.status, tc.risk_level, tc.policy_decision, tc.approval_id, tc.duration_ms, tc.input_redacted_json, tc.input_execution_json, tc.output_redacted_json,
		       tc.explanation_json, tc.error_message, tc.trace_id, tc.created_at,
		       COALESCE(ar.status, '') AS approval_status
		FROM tool_calls tc
		LEFT JOIN approval_requests ar
		       ON ar.id = tc.approval_id AND ar.workspace_id = tc.workspace_id
		WHERE tc.workspace_id = $1 AND tc.id = $2
	`, workspaceID, callID)
}

func (s *PostgresStore) GetToolCallByApprovalID(ctx context.Context, workspaceID, approvalID string) (model.ToolCall, error) {
	return s.getToolCall(ctx, `
		SELECT tc.id, tc.request_id, tc.workspace_id, tc.actor_id, tc.actor_subject, tc.actor_email, tc.actor_name, tc.tool_id, tc.tool_key,
		       tc.status, tc.risk_level, tc.policy_decision, tc.approval_id, tc.duration_ms, tc.input_redacted_json, tc.input_execution_json, tc.output_redacted_json,
		       tc.explanation_json, tc.error_message, tc.trace_id, tc.created_at,
		       COALESCE(ar.status, '') AS approval_status
		FROM tool_calls tc
		LEFT JOIN approval_requests ar
		       ON ar.id = tc.approval_id AND ar.workspace_id = tc.workspace_id
		WHERE tc.workspace_id = $1 AND tc.approval_id = $2
		ORDER BY CASE WHEN tc.status = 'approval_required' THEN 0 ELSE 1 END, tc.created_at ASC, tc.id ASC
		LIMIT 1
	`, workspaceID, approvalID)
}

func (s *PostgresStore) CreateToolCall(ctx context.Context, input model.CreateToolCallInput) (model.ToolCall, error) {
	now := time.Now().UTC()
	call := model.ToolCall{}
	var explanationJSON []byte
	approvalID := input.ApprovalID
	if strings.TrimSpace(approvalID) == "" {
		approvalID = ""
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tool_calls (
			id, request_id, workspace_id, actor_id, actor_subject, actor_email, actor_name, tool_id, tool_key,
			status, risk_level, policy_decision, approval_id, duration_ms, input_redacted_json, input_execution_json, output_redacted_json,
			explanation_json, error_message, trace_id, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17::jsonb,$18::jsonb,$19,$20,$21)
		RETURNING id, request_id, workspace_id, actor_id, actor_subject, actor_email, actor_name, tool_id, tool_key,
		          status, risk_level, policy_decision, approval_id, duration_ms, input_redacted_json, input_execution_json, output_redacted_json,
		          explanation_json, error_message, trace_id, created_at,
		          COALESCE((SELECT status FROM approval_requests ar WHERE ar.id = approval_id AND ar.workspace_id = workspace_id), '')
	`, ensureID("", "call"), ensureID(input.RequestID, "req"), input.WorkspaceID, input.ActorID, input.ActorSubject,
		input.ActorEmail, input.ActorName, input.ToolID, input.ToolKey, input.Status, input.RiskLevel, input.PolicyDecision,
		approvalID, input.DurationMs, mustJSON(defaultJSON(input.InputRedactedJSON)), mustJSON(defaultJSON(input.InputExecutionJSON)),
		mustJSON(defaultJSON(input.OutputRedactedJSON)), mustJSON(defaultToolCallExplanationJSON(input.Explanation)), input.ErrorMessage, input.TraceID, now).Scan(
		&call.ID, &call.RequestID, &call.WorkspaceID, &call.ActorID, &call.ActorSubject, &call.ActorEmail, &call.ActorName,
		&call.ToolID, &call.ToolKey, &call.Status, &call.RiskLevel, &call.PolicyDecision, &call.ApprovalID, &call.DurationMs,
		&call.InputRedactedJSON, &call.InputExecutionJSON, &call.OutputRedactedJSON, &explanationJSON, &call.ErrorMessage, &call.TraceID, &call.CreatedAt, &call.ApprovalStatus,
	)
	if err != nil {
		return model.ToolCall{}, mapPgErr(err)
	}
	explanation, err := decodeToolCallExplanationJSON(explanationJSON)
	if err != nil {
		return model.ToolCall{}, err
	}
	call.Explanation = explanation
	return call, nil
}

func (s *PostgresStore) UpdateToolCall(ctx context.Context, workspaceID, callID string, input model.UpdateToolCallInput) (model.ToolCall, error) {
	call := model.ToolCall{}
	var explanationJSON []byte
	inputExecutionJSON := strings.TrimSpace(string(input.InputExecutionJSON))
	err := s.pool.QueryRow(ctx, `
		UPDATE tool_calls
		SET status = $3,
		    duration_ms = $4,
		    output_redacted_json = $5::jsonb,
		    error_message = $6,
		    trace_id = CASE WHEN $7 = '' THEN trace_id ELSE $7 END,
		    input_execution_json = CASE WHEN $8 = '' THEN input_execution_json ELSE $8::jsonb END
		WHERE workspace_id = $1 AND id = $2
		RETURNING id, request_id, workspace_id, actor_id, actor_subject, actor_email, actor_name, tool_id, tool_key,
		          status, risk_level, policy_decision, approval_id, duration_ms, input_redacted_json, input_execution_json, output_redacted_json,
		          explanation_json, error_message, trace_id, created_at,
		          COALESCE((SELECT status FROM approval_requests ar WHERE ar.id = approval_id AND ar.workspace_id = workspace_id), '')
	`, workspaceID, callID, input.Status, input.DurationMs, mustJSON(defaultJSON(input.OutputRedactedJSON)), input.ErrorMessage, input.TraceID, inputExecutionJSON).Scan(
		&call.ID, &call.RequestID, &call.WorkspaceID, &call.ActorID, &call.ActorSubject, &call.ActorEmail, &call.ActorName,
		&call.ToolID, &call.ToolKey, &call.Status, &call.RiskLevel, &call.PolicyDecision, &call.ApprovalID, &call.DurationMs,
		&call.InputRedactedJSON, &call.InputExecutionJSON, &call.OutputRedactedJSON, &explanationJSON, &call.ErrorMessage, &call.TraceID, &call.CreatedAt, &call.ApprovalStatus,
	)
	if err != nil {
		return model.ToolCall{}, mapPgErr(err)
	}
	explanation, err := decodeToolCallExplanationJSON(explanationJSON)
	if err != nil {
		return model.ToolCall{}, err
	}
	call.Explanation = explanation
	return call, nil
}

func (s *PostgresStore) ListConnectors(ctx context.Context, workspaceID string) ([]model.Connector, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
		FROM connectors
		WHERE workspace_id = $1
		ORDER BY created_at DESC, id DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.Connector, 0)
	for rows.Next() {
		item, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetConnectorByID(ctx context.Context, workspaceID, connectorID string) (model.Connector, error) {
	return s.getConnector(ctx, `
		SELECT id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
		FROM connectors
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, connectorID)
}

func (s *PostgresStore) CreateConnector(ctx context.Context, input model.CreateConnectorInput) (model.Connector, error) {
	now := time.Now().UTC()
	connector := model.Connector{}
	typeName := strings.ToLower(strings.TrimSpace(input.Type))
	name := strings.ToLower(strings.TrimSpace(input.Name))
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = typeName + "." + name
	}
	if len(input.ConfigJSON) == 0 {
		input.ConfigJSON = json.RawMessage(`{}`)
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO connectors (
			id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9)
		RETURNING id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
	`, ensureID("", "connector"), input.WorkspaceID, typeName, name, displayName, mustJSON(input.ConfigJSON), input.Enabled, now, now).Scan(
		&connector.ID, &connector.WorkspaceID, &connector.Type, &connector.Name, &connector.DisplayName, &connector.ConfigJSON, &connector.Enabled, &connector.CreatedAt, &connector.UpdatedAt,
	)
	if err != nil {
		return model.Connector{}, mapPgErr(err)
	}
	return connector, nil
}

func (s *PostgresStore) UpdateConnector(ctx context.Context, workspaceID, connectorID string, input model.UpdateConnectorInput) (model.Connector, error) {
	now := time.Now().UTC()
	connector := model.Connector{}
	configJSON := strings.TrimSpace(string(input.ConfigJSON))
	err := s.pool.QueryRow(ctx, `
		UPDATE connectors
		SET display_name = CASE WHEN $3 = '' THEN display_name ELSE $3 END,
		    config_json = CASE WHEN $4 = '' THEN config_json ELSE $4::jsonb END,
		    enabled = COALESCE($5::boolean, enabled),
		    updated_at = $6
		WHERE workspace_id = $1 AND id = $2
		RETURNING id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
	`, workspaceID, connectorID, strings.TrimSpace(input.DisplayName), configJSON, input.Enabled, now).Scan(
		&connector.ID, &connector.WorkspaceID, &connector.Type, &connector.Name, &connector.DisplayName, &connector.ConfigJSON, &connector.Enabled, &connector.CreatedAt, &connector.UpdatedAt,
	)
	if err != nil {
		return model.Connector{}, mapPgErr(err)
	}
	return connector, nil
}

func (s *PostgresStore) ListPolicyRules(ctx context.Context, workspaceID string) ([]model.PolicyRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+policyRuleColumnList+`
		FROM policy_rules
		WHERE workspace_id = $1
		ORDER BY priority ASC, created_at ASC, id ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.PolicyRule, 0)
	for rows.Next() {
		item, scanErr := scanPolicyRule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetPolicyRuleByID(ctx context.Context, workspaceID, ruleID string) (model.PolicyRule, error) {
	return s.getPolicyRule(ctx, `
		SELECT `+policyRuleColumnList+`
		FROM policy_rules
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, ruleID)
}

func (s *PostgresStore) CreatePolicyRule(ctx context.Context, input model.CreatePolicyRuleInput) (model.PolicyRule, error) {
	now := time.Now().UTC()
	rule := model.PolicyRule{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO policy_rules (
			id, workspace_id, name, description, enabled, priority, effect, connector_type,
			tool_name_pattern, operation_type, risk_level, resource_pattern, reason, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14)
		RETURNING `+policyRuleColumnList+`
	`, ensureID("", "policy_rule"), input.WorkspaceID, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.Enabled,
		input.Priority, strings.ToLower(strings.TrimSpace(input.Effect)), normalizePolicyWildcard(input.ConnectorType),
		normalizePolicyWildcard(input.ToolNamePattern), normalizePolicyWildcard(input.OperationType), normalizePolicyWildcard(input.RiskLevel),
		normalizePolicyWildcard(input.ResourcePattern), strings.TrimSpace(input.Reason), now).Scan(
		&rule.ID, &rule.WorkspaceID, &rule.Name, &rule.Description, &rule.Enabled, &rule.Priority, &rule.Effect,
		&rule.ConnectorType, &rule.ToolNamePattern, &rule.OperationType, &rule.RiskLevel, &rule.ResourcePattern,
		&rule.Reason, &rule.CreatedAt, &rule.UpdatedAt,
	)
	if err != nil {
		return model.PolicyRule{}, mapPgErr(err)
	}
	return rule, nil
}

func (s *PostgresStore) UpdatePolicyRule(ctx context.Context, workspaceID, ruleID string, input model.UpdatePolicyRuleInput) (model.PolicyRule, error) {
	now := time.Now().UTC()
	rule := model.PolicyRule{}
	err := s.pool.QueryRow(ctx, `
		UPDATE policy_rules
		SET name = $3,
		    description = $4,
		    enabled = COALESCE($5::boolean, enabled),
		    priority = COALESCE($6::integer, priority),
		    effect = $7,
		    connector_type = $8,
		    tool_name_pattern = $9,
		    operation_type = $10,
		    risk_level = $11,
		    resource_pattern = $12,
		    reason = $13,
		    updated_at = $14
		WHERE workspace_id = $1 AND id = $2
		RETURNING `+policyRuleColumnList+`
	`, workspaceID, ruleID, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.Enabled, input.Priority,
		strings.ToLower(strings.TrimSpace(input.Effect)), normalizePolicyWildcard(input.ConnectorType), normalizePolicyWildcard(input.ToolNamePattern),
		normalizePolicyWildcard(input.OperationType), normalizePolicyWildcard(input.RiskLevel), normalizePolicyWildcard(input.ResourcePattern),
		strings.TrimSpace(input.Reason), now).Scan(
		&rule.ID, &rule.WorkspaceID, &rule.Name, &rule.Description, &rule.Enabled, &rule.Priority, &rule.Effect,
		&rule.ConnectorType, &rule.ToolNamePattern, &rule.OperationType, &rule.RiskLevel, &rule.ResourcePattern,
		&rule.Reason, &rule.CreatedAt, &rule.UpdatedAt,
	)
	if err != nil {
		return model.PolicyRule{}, mapPgErr(err)
	}
	return rule, nil
}

func (s *PostgresStore) DeletePolicyRule(ctx context.Context, workspaceID, ruleID string) error {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM policy_rules
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, ruleID)
	if err != nil {
		return mapPgErr(err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListApprovalRequests(ctx context.Context, workspaceID string) ([]model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+approvalRequestColumnList+`
		FROM approval_requests
		WHERE workspace_id = $1
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.ApprovalRequest, 0)
	for rows.Next() {
		item, err := scanApprovalRequest(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) ensureBuiltinConnectors(ctx context.Context, inputs []model.BootstrapConnectorInput) error {
	if len(inputs) == 0 {
		return nil
	}
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		for _, input := range inputs {
			if _, err := s.CreateConnector(ctx, model.CreateConnectorInput{
				WorkspaceID: workspace.ID,
				Type:        input.Type,
				Name:        input.Name,
				DisplayName: input.DisplayName,
				ConfigJSON:  input.ConfigJSON,
				Enabled:     input.Enabled,
			}); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
	}
	return nil
}

func (s *PostgresStore) GetApprovalRequestByID(ctx context.Context, workspaceID, approvalID string) (model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	return s.getApprovalRequest(ctx, `
		SELECT `+approvalRequestColumnList+`
		FROM approval_requests
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, approvalID)
}

func (s *PostgresStore) CreateApprovalRequest(ctx context.Context, input model.CreateApprovalRequestInput) (model.ApprovalRequest, error) {
	now := time.Now().UTC()
	expiresAt := approvalExpiresAt(now, input.TTL)
	toolDisplayName := strings.TrimSpace(input.ToolDisplayName)
	if toolDisplayName == "" {
		toolDisplayName = strings.TrimSpace(input.ToolKey)
	}
	requestedBy := strings.TrimSpace(input.RequestedBy)
	if requestedBy == "" {
		requestedBy = "unknown"
	}
	approval := model.ApprovalRequest{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO approval_requests (
			id, workspace_id, tool_key, tool_display_name, status, requested_by, reviewed_by, reason,
			fingerprint, adapter, action_type, target, canonical_target, content_encoding, content_hash,
			script_hash, resolved_file_identity, parent_identity, decision_payload_json, expires_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,'pending',$5,'',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19)
		RETURNING `+approvalRequestColumnList+`
	`, ensureID("", "approval"), input.WorkspaceID, strings.TrimSpace(input.ToolKey), toolDisplayName, requestedBy, strings.TrimSpace(input.Reason),
		strings.TrimSpace(input.Fingerprint), strings.TrimSpace(input.Adapter), strings.TrimSpace(input.ActionType), strings.TrimSpace(input.Target),
		strings.TrimSpace(input.CanonicalTarget), strings.TrimSpace(input.ContentEncoding), strings.TrimSpace(input.ContentHash),
		strings.TrimSpace(input.ScriptHash), strings.TrimSpace(input.ResolvedFileIdentity), strings.TrimSpace(input.ParentIdentity),
		mustJSON(defaultJSON(input.DecisionPayloadJSON)), expiresAt, now).Scan(
		&approval.ID, &approval.WorkspaceID, &approval.ToolKey, &approval.ToolDisplayName, &approval.Status, &approval.RequestedBy,
		&approval.ReviewedBy, &approval.Reason, &approval.Fingerprint, &approval.Adapter, &approval.ActionType, &approval.Target,
		&approval.CanonicalTarget, &approval.ContentEncoding, &approval.ContentHash, &approval.ScriptHash, &approval.ResolvedFileIdentity,
		&approval.ParentIdentity, &approval.DecisionPayloadJSON, &approval.ExpiresAt, &approval.CreatedAt, &approval.UpdatedAt,
	)
	if err != nil {
		return model.ApprovalRequest{}, mapPgErr(err)
	}
	return approval, nil
}

func (s *PostgresStore) UpdateApprovalRequest(ctx context.Context, workspaceID, approvalID string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	now := time.Now().UTC()
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	current, err := s.getApprovalRequest(ctx, `
		SELECT `+approvalRequestColumnList+`
		FROM approval_requests
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, approvalID)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if strings.EqualFold(strings.TrimSpace(current.Status), "expired") {
		return model.ApprovalRequest{}, ErrExpired
	}
	approval := model.ApprovalRequest{}
	err = s.pool.QueryRow(ctx, `
		UPDATE approval_requests
		SET status = $3,
		    reviewed_by = $4,
		    reason = CASE WHEN $5 = '' THEN reason ELSE $5 END,
		    updated_at = $6
		WHERE workspace_id = $1 AND id = $2
		RETURNING `+approvalRequestColumnList+`
	`, workspaceID, approvalID, input.Status, strings.TrimSpace(input.ReviewedBy), strings.TrimSpace(input.Reason), now).Scan(
		&approval.ID, &approval.WorkspaceID, &approval.ToolKey, &approval.ToolDisplayName, &approval.Status, &approval.RequestedBy,
		&approval.ReviewedBy, &approval.Reason, &approval.Fingerprint, &approval.Adapter, &approval.ActionType, &approval.Target,
		&approval.CanonicalTarget, &approval.ContentEncoding, &approval.ContentHash, &approval.ScriptHash, &approval.ResolvedFileIdentity,
		&approval.ParentIdentity, &approval.DecisionPayloadJSON, &approval.ExpiresAt, &approval.CreatedAt, &approval.UpdatedAt,
	)
	if err != nil {
		mappedErr := mapPgErr(err)
		if !errors.Is(mappedErr, ErrNotFound) {
			return model.ApprovalRequest{}, mappedErr
		}
		refreshedCurrent, getErr := s.getApprovalRequest(ctx, `
			SELECT `+approvalRequestColumnList+`
			FROM approval_requests
			WHERE workspace_id = $1 AND id = $2
		`, workspaceID, approvalID)
		if getErr != nil {
			return model.ApprovalRequest{}, getErr
		}
		if strings.EqualFold(strings.TrimSpace(refreshedCurrent.Status), "expired") {
			return model.ApprovalRequest{}, ErrExpired
		}
		return model.ApprovalRequest{}, ErrConflict
	}
	return approval, nil
}

func (s *PostgresStore) TransitionApprovalRequest(ctx context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	now := time.Now().UTC()
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	approval := model.ApprovalRequest{}
	err := s.pool.QueryRow(ctx, `
		UPDATE approval_requests
		SET status = $4,
		    reviewed_by = $5,
		    reason = CASE WHEN $6 = '' THEN reason ELSE $6 END,
		    updated_at = $7
		WHERE workspace_id = $1 AND id = $2 AND status = $3
		RETURNING `+approvalRequestColumnList+`
	`, workspaceID, approvalID, strings.TrimSpace(fromStatus), strings.TrimSpace(input.Status), strings.TrimSpace(input.ReviewedBy), strings.TrimSpace(input.Reason), now).Scan(
		&approval.ID, &approval.WorkspaceID, &approval.ToolKey, &approval.ToolDisplayName, &approval.Status, &approval.RequestedBy,
		&approval.ReviewedBy, &approval.Reason, &approval.Fingerprint, &approval.Adapter, &approval.ActionType, &approval.Target,
		&approval.CanonicalTarget, &approval.ContentEncoding, &approval.ContentHash, &approval.ScriptHash, &approval.ResolvedFileIdentity,
		&approval.ParentIdentity, &approval.DecisionPayloadJSON, &approval.ExpiresAt, &approval.CreatedAt, &approval.UpdatedAt,
	)
	if err == nil {
		return approval, nil
	}
	mappedErr := mapPgErr(err)
	if !errors.Is(mappedErr, ErrNotFound) {
		return model.ApprovalRequest{}, mappedErr
	}
	current, getErr := s.getApprovalRequest(ctx, `
		SELECT `+approvalRequestColumnList+`
		FROM approval_requests
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, approvalID)
	if getErr != nil {
		return model.ApprovalRequest{}, getErr
	}
	if strings.EqualFold(strings.TrimSpace(current.Status), "expired") {
		return model.ApprovalRequest{}, ErrExpired
	}
	return model.ApprovalRequest{}, ErrConflict
}

func (s *PostgresStore) ensureSchema(ctx context.Context) (err error) {
	postgresSchemaInitMu.Lock()
	defer postgresSchemaInitMu.Unlock()

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire postgres schema connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, postgresSchemaAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire postgres schema advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, postgresSchemaAdvisoryLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("release postgres schema advisory lock: %w", unlockErr)
		}
	}()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			zitadel_organization_id TEXT NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			zitadel_user_id TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'owner',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (workspace_id, zitadel_user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS policy_rules (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			priority INTEGER NOT NULL DEFAULT 100,
			effect TEXT NOT NULL,
			connector_type TEXT NOT NULL DEFAULT '*',
			tool_name_pattern TEXT NOT NULL DEFAULT '*',
			operation_type TEXT NOT NULL DEFAULT '*',
			risk_level TEXT NOT NULL DEFAULT '*',
			resource_pattern TEXT NOT NULL DEFAULT '*',
			reason TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS policy_rules_workspace_order_idx
			ON policy_rules (workspace_id, priority, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS approval_requests (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			tool_key TEXT NOT NULL,
			tool_display_name TEXT NOT NULL,
			status TEXT NOT NULL,
			requested_by TEXT NOT NULL DEFAULT '',
			reviewed_by TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			fingerprint TEXT NOT NULL DEFAULT '',
			adapter TEXT NOT NULL DEFAULT '',
			action_type TEXT NOT NULL DEFAULT '',
			target TEXT NOT NULL DEFAULT '',
			canonical_target TEXT NOT NULL DEFAULT '',
			content_encoding TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			script_hash TEXT NOT NULL DEFAULT '',
			resolved_file_identity TEXT NOT NULL DEFAULT '',
			parent_identity TEXT NOT NULL DEFAULT '',
			decision_payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS adapter TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS action_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS target TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS canonical_target TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS content_encoding TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS script_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS resolved_file_identity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS parent_identity TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decision_payload_json JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`DROP INDEX IF EXISTS approval_requests_workspace_fingerprint_idx`,
		`CREATE UNIQUE INDEX IF NOT EXISTS approval_requests_workspace_active_fingerprint_idx
			ON approval_requests (workspace_id, fingerprint)
			WHERE fingerprint <> '' AND status IN ('pending', 'approved')`,
		`CREATE TABLE IF NOT EXISTS connectors (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			config_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (workspace_id, type, name)
		)`,
		`CREATE TABLE IF NOT EXISTS secrets (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			workspace_org_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			secret_type TEXT NOT NULL,
			value_source TEXT NOT NULL DEFAULT 'env',
			value_ref TEXT NOT NULL DEFAULT '',
			metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (workspace_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS tools (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			namespace TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			operation_type TEXT NOT NULL,
			risk_level TEXT NOT NULL,
			requires_approval BOOLEAN NOT NULL DEFAULT FALSE,
			input_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			output_schema_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (workspace_id, namespace, name)
		)`,
		`CREATE TABLE IF NOT EXISTS tool_calls (
			id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL UNIQUE,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			actor_id TEXT NOT NULL DEFAULT '',
			actor_subject TEXT NOT NULL DEFAULT '',
			actor_email TEXT NOT NULL DEFAULT '',
			actor_name TEXT NOT NULL DEFAULT '',
			tool_id TEXT NOT NULL REFERENCES tools(id) ON DELETE RESTRICT,
			tool_key TEXT NOT NULL,
			status TEXT NOT NULL,
			risk_level TEXT NOT NULL,
			policy_decision TEXT NOT NULL DEFAULT 'allow',
			approval_id TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			input_redacted_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			input_execution_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			output_redacted_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			explanation_json JSONB NOT NULL DEFAULT '{}'::jsonb,
			error_message TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`ALTER TABLE tool_calls ADD COLUMN IF NOT EXISTS input_execution_json JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE tool_calls ADD COLUMN IF NOT EXISTS explanation_json JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours')`,
	}

	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) ensureWorkspaceSeed(ctx context.Context, input model.BootstrapInput) error {
	count := 0
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if strings.TrimSpace(input.WorkspaceName) == "" {
		input.WorkspaceName = "Default Workspace"
	}
	if strings.TrimSpace(input.WorkspaceSlug) == "" {
		input.WorkspaceSlug = "default"
	}
	if strings.TrimSpace(input.WorkspaceOrganizationID) == "" {
		input.WorkspaceOrganizationID = "local-org"
	}

	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workspaces (id, name, slug, zitadel_organization_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, ensureID("", "workspace"), input.WorkspaceName, strings.ToLower(input.WorkspaceSlug), input.WorkspaceOrganizationID, now, now)
	return err
}

func (s *PostgresStore) ensureBuiltinTools(ctx context.Context) error {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		for _, input := range model.BuiltinToolInputs(workspace.ID) {
			if _, err := s.GetToolByKey(ctx, workspace.ID, input.Namespace+"."+input.Name); err == nil {
				continue
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
			if _, err := s.CreateTool(ctx, input); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
	}
	return nil
}

func (s *PostgresStore) getWorkspace(ctx context.Context, query string, args ...any) (model.Workspace, error) {
	var workspace model.Workspace
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.ZitadelOrganizationID, &workspace.CreatedAt, &workspace.UpdatedAt); err != nil {
		return model.Workspace{}, mapPgErr(err)
	}
	return workspace, nil
}

func (s *PostgresStore) getTool(ctx context.Context, query string, args ...any) (model.Tool, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	tool, err := scanTool(row)
	if err != nil {
		return model.Tool{}, mapPgErr(err)
	}
	return tool, nil
}

func (s *PostgresStore) getToolCall(ctx context.Context, query string, args ...any) (model.ToolCall, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	call, err := scanToolCall(row)
	if err != nil {
		return model.ToolCall{}, mapPgErr(err)
	}
	return call, nil
}

func (s *PostgresStore) getPolicyRule(ctx context.Context, query string, args ...any) (model.PolicyRule, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	rule, err := scanPolicyRule(row)
	if err != nil {
		return model.PolicyRule{}, mapPgErr(err)
	}
	return rule, nil
}

func (s *PostgresStore) getApprovalRequest(ctx context.Context, query string, args ...any) (model.ApprovalRequest, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	approval, err := scanApprovalRequest(row)
	if err != nil {
		return model.ApprovalRequest{}, mapPgErr(err)
	}
	return approval, nil
}

func scanTool(row interface{ Scan(...any) error }) (model.Tool, error) {
	var tool model.Tool
	var inputSchema, outputSchema []byte
	if err := row.Scan(&tool.ID, &tool.WorkspaceID, &tool.Namespace, &tool.Name, &tool.DisplayName, &tool.Description, &tool.OperationType, &tool.RiskLevel, &tool.RequiresApproval, &inputSchema, &outputSchema, &tool.Enabled, &tool.CreatedAt, &tool.UpdatedAt); err != nil {
		return model.Tool{}, err
	}
	tool.InputSchemaJSON = json.RawMessage(inputSchema)
	tool.OutputSchemaJSON = json.RawMessage(outputSchema)
	return tool, nil
}

func scanToolCall(row interface{ Scan(...any) error }) (model.ToolCall, error) {
	var call model.ToolCall
	var inputJSON, executionJSON, outputJSON, explanationJSON []byte
	if err := row.Scan(&call.ID, &call.RequestID, &call.WorkspaceID, &call.ActorID, &call.ActorSubject, &call.ActorEmail, &call.ActorName, &call.ToolID, &call.ToolKey, &call.Status, &call.RiskLevel, &call.PolicyDecision, &call.ApprovalID, &call.DurationMs, &inputJSON, &executionJSON, &outputJSON, &explanationJSON, &call.ErrorMessage, &call.TraceID, &call.CreatedAt, &call.ApprovalStatus); err != nil {
		return model.ToolCall{}, err
	}
	call.InputRedactedJSON = json.RawMessage(inputJSON)
	call.InputExecutionJSON = json.RawMessage(executionJSON)
	call.OutputRedactedJSON = json.RawMessage(outputJSON)
	explanation, err := decodeToolCallExplanationJSON(explanationJSON)
	if err != nil {
		return model.ToolCall{}, err
	}
	call.Explanation = explanation
	return call, nil
}

func decodeToolCallExplanationJSON(raw []byte) (*model.ToolCallExplanation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, nil
	}
	var explanation model.ToolCallExplanation
	if err := json.Unmarshal(raw, &explanation); err != nil {
		return nil, err
	}
	if strings.TrimSpace(explanation.TargetCategory) == "" &&
		strings.TrimSpace(explanation.RiskLevel) == "" &&
		strings.TrimSpace(explanation.MatchedRule) == "" &&
		len(explanation.Signals) == 0 {
		return nil, nil
	}
	return &explanation, nil
}

func scanPolicyRule(row interface{ Scan(...any) error }) (model.PolicyRule, error) {
	var rule model.PolicyRule
	if err := row.Scan(
		&rule.ID, &rule.WorkspaceID, &rule.Name, &rule.Description, &rule.Enabled, &rule.Priority, &rule.Effect,
		&rule.ConnectorType, &rule.ToolNamePattern, &rule.OperationType, &rule.RiskLevel, &rule.ResourcePattern,
		&rule.Reason, &rule.CreatedAt, &rule.UpdatedAt,
	); err != nil {
		return model.PolicyRule{}, err
	}
	return rule, nil
}

func scanConnector(row interface{ Scan(...any) error }) (model.Connector, error) {
	var connector model.Connector
	var configJSON []byte
	if err := row.Scan(&connector.ID, &connector.WorkspaceID, &connector.Type, &connector.Name, &connector.DisplayName, &configJSON, &connector.Enabled, &connector.CreatedAt, &connector.UpdatedAt); err != nil {
		return model.Connector{}, err
	}
	connector.ConfigJSON = json.RawMessage(configJSON)
	return connector, nil
}

func scanApprovalRequest(row interface{ Scan(...any) error }) (model.ApprovalRequest, error) {
	var approval model.ApprovalRequest
	var decisionPayload []byte
	if err := row.Scan(&approval.ID, &approval.WorkspaceID, &approval.ToolKey, &approval.ToolDisplayName, &approval.Status, &approval.RequestedBy, &approval.ReviewedBy, &approval.Reason, &approval.Fingerprint, &approval.Adapter, &approval.ActionType, &approval.Target, &approval.CanonicalTarget, &approval.ContentEncoding, &approval.ContentHash, &approval.ScriptHash, &approval.ResolvedFileIdentity, &approval.ParentIdentity, &decisionPayload, &approval.ExpiresAt, &approval.CreatedAt, &approval.UpdatedAt); err != nil {
		return model.ApprovalRequest{}, err
	}
	approval.DecisionPayloadJSON = json.RawMessage(decisionPayload)
	return approval, nil
}

func (s *PostgresStore) expirePendingApprovals(ctx context.Context, workspaceID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE approval_requests
		SET status = 'expired',
		    updated_at = NOW()
		WHERE workspace_id = $1
		  AND status = 'pending'
		  AND expires_at <= NOW()
	`, workspaceID)
	return err
}

func (s *PostgresStore) getConnector(ctx context.Context, query string, args ...any) (model.Connector, error) {
	row := s.pool.QueryRow(ctx, query, args...)
	connector, err := scanConnector(row)
	if err != nil {
		return model.Connector{}, mapPgErr(err)
	}
	return connector, nil
}

func buildToolCallFilterClause(workspaceID string, query model.ToolCallQuery) (string, []any, error) {
	clauses := []string{"tc.workspace_id = $1"}
	args := []any{workspaceID}

	if tool := strings.TrimSpace(query.Tool); tool != "" {
		args = append(args, tool)
		clauses = append(clauses, fmt.Sprintf("tc.tool_key = $%d", len(args)))
	}
	if len(query.Statuses) > 0 {
		statusPlaceholders := make([]string, 0, len(query.Statuses))
		for _, status := range query.Statuses {
			trimmed := strings.TrimSpace(status)
			if trimmed == "" {
				continue
			}
			args = append(args, trimmed)
			statusPlaceholders = append(statusPlaceholders, fmt.Sprintf("$%d", len(args)))
		}
		if len(statusPlaceholders) > 0 {
			clauses = append(clauses, "tc.status IN ("+strings.Join(statusPlaceholders, ",")+")")
		}
	}
	if query.From != nil {
		args = append(args, query.From.UTC())
		clauses = append(clauses, fmt.Sprintf("tc.created_at >= $%d", len(args)))
	}
	if query.To != nil {
		args = append(args, query.To.UTC())
		clauses = append(clauses, fmt.Sprintf("tc.created_at <= $%d", len(args)))
	}

	return strings.Join(clauses, " AND "), args, nil
}

func mapPgErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		}
	}
	if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
		return ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) || strings.Contains(err.Error(), "no rows in result set") {
		return ErrNotFound
	}
	return err
}

func mustJSON(value json.RawMessage) []byte {
	if len(value) == 0 {
		return []byte(`{}`)
	}
	return []byte(value)
}

func defaultToolCallExplanationJSON(explanation *model.ToolCallExplanation) json.RawMessage {
	if explanation == nil {
		return json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(explanation)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

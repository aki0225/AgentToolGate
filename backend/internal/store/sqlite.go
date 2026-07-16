package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db   *sql.DB
	path string
}

func NewSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite data dir: %w", err)
	}

	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// 本地单机模式优先稳定性：单连接避免 SQLite 写锁竞争导致审批重复执行或随机失败。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db, path: path}
	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() {
	_ = s.db.Close()
}

func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLiteStore) Bootstrap(ctx context.Context, input model.BootstrapInput) error {
	if err := s.ensureWorkspaceSeed(ctx, input); err != nil {
		return err
	}
	if err := s.ensureBuiltinConnectors(ctx, input.Connectors); err != nil {
		return err
	}
	return s.ensureBuiltinTools(ctx)
}

func (s *SQLiteStore) ListWorkspaces(ctx context.Context) ([]model.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		item, err := scanSQLiteWorkspace(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetWorkspaceBySlug(ctx context.Context, slug string) (model.Workspace, error) {
	return s.getWorkspace(ctx, `SELECT id, name, slug, zitadel_organization_id, created_at, updated_at FROM workspaces WHERE slug = ?`, strings.ToLower(strings.TrimSpace(slug)))
}

func (s *SQLiteStore) GetWorkspaceByOrganizationID(ctx context.Context, organizationID string) (model.Workspace, error) {
	return s.getWorkspace(ctx, `SELECT id, name, slug, zitadel_organization_id, created_at, updated_at FROM workspaces WHERE zitadel_organization_id = ?`, strings.TrimSpace(organizationID))
}

func (s *SQLiteStore) CreateWorkspace(ctx context.Context, input model.CreateWorkspaceInput) (model.Workspace, error) {
	now := time.Now().UTC()
	return s.insertWorkspace(ctx, model.Workspace{
		ID:                    ensureID("", "workspace"),
		Name:                  strings.TrimSpace(input.Name),
		Slug:                  strings.ToLower(strings.TrimSpace(input.Slug)),
		ZitadelOrganizationID: strings.TrimSpace(input.ZitadelOrganizationID),
		CreatedAt:             now,
		UpdatedAt:             now,
	})
}

func (s *SQLiteStore) UpsertUser(ctx context.Context, input model.UpsertUserInput) (model.User, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, workspace_id, zitadel_user_id, email, name, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, zitadel_user_id)
		DO UPDATE SET email = excluded.email, name = excluded.name, role = excluded.role, updated_at = excluded.updated_at
	`, ensureID("", "user"), input.WorkspaceID, input.ZitadelUserID, input.Email, input.Name, input.Role, sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.User{}, mapSQLiteErr(err)
	}
	return s.getUser(ctx, `
		SELECT id, workspace_id, zitadel_user_id, email, name, role, created_at, updated_at
		FROM users
		WHERE workspace_id = ? AND zitadel_user_id = ?
	`, input.WorkspaceID, input.ZitadelUserID)
}

func (s *SQLiteStore) ListTools(ctx context.Context, workspaceID string) ([]model.Tool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
		       requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		FROM tools
		WHERE workspace_id = ? AND namespace <> 'agent_guard'
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Tool, 0)
	for rows.Next() {
		item, err := scanSQLiteTool(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetToolByID(ctx context.Context, workspaceID, toolID string) (model.Tool, error) {
	return s.getTool(ctx, sqliteToolSelect()+` WHERE workspace_id = ? AND id = ?`, workspaceID, toolID)
}

func (s *SQLiteStore) GetToolByKey(ctx context.Context, workspaceID, key string) (model.Tool, error) {
	namespace, name, ok := strings.Cut(strings.ToLower(strings.TrimSpace(key)), ".")
	if !ok {
		return model.Tool{}, ErrNotFound
	}
	return s.getTool(ctx, sqliteToolSelect()+` WHERE workspace_id = ? AND namespace = ? AND name = ?`, workspaceID, namespace, name)
}

func (s *SQLiteStore) CreateTool(ctx context.Context, input model.CreateToolInput) (model.Tool, error) {
	now := time.Now().UTC()
	namespace := strings.ToLower(strings.TrimSpace(input.Namespace))
	name := strings.ToLower(strings.TrimSpace(input.Name))
	if strings.TrimSpace(input.DisplayName) == "" {
		input.DisplayName = namespace + "." + name
	}
	if strings.TrimSpace(input.OperationType) == "" {
		input.OperationType = "mock"
	}
	if strings.TrimSpace(input.RiskLevel) == "" {
		input.RiskLevel = "low"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tools (
			id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
			requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ensureID("", "tool"), input.WorkspaceID, namespace, name,
		strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Description), strings.TrimSpace(input.OperationType), strings.TrimSpace(input.RiskLevel),
		input.RequiresApproval, string(defaultJSON(input.InputSchemaJSON)), string(defaultJSON(input.OutputSchemaJSON)), input.Enabled,
		sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.Tool{}, mapSQLiteErr(err)
	}
	return s.GetToolByKey(ctx, input.WorkspaceID, namespace+"."+name)
}

func (s *SQLiteStore) UpdateTool(ctx context.Context, workspaceID, toolID string, input model.UpdateToolInput) (model.Tool, error) {
	now := time.Now().UTC()
	inputSchemaJSON := strings.TrimSpace(string(input.InputSchemaJSON))
	outputSchemaJSON := strings.TrimSpace(string(input.OutputSchemaJSON))
	result, err := s.db.ExecContext(ctx, `
		UPDATE tools
		SET display_name = CASE WHEN ? = '' THEN display_name ELSE ? END,
		    description = CASE WHEN ? = '' THEN description ELSE ? END,
		    operation_type = CASE WHEN ? = '' THEN operation_type ELSE ? END,
		    risk_level = CASE WHEN ? = '' THEN risk_level ELSE ? END,
		    requires_approval = COALESCE(?, requires_approval),
		    input_schema_json = CASE WHEN ? = '' THEN input_schema_json ELSE ? END,
		    output_schema_json = CASE WHEN ? = '' THEN output_schema_json ELSE ? END,
		    enabled = COALESCE(?, enabled),
		    updated_at = ?
		WHERE workspace_id = ? AND id = ?
	`, strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.Description), strings.TrimSpace(input.Description),
		strings.TrimSpace(input.OperationType), strings.TrimSpace(input.OperationType), strings.TrimSpace(input.RiskLevel), strings.TrimSpace(input.RiskLevel),
		input.RequiresApproval, inputSchemaJSON, inputSchemaJSON, outputSchemaJSON, outputSchemaJSON, input.Enabled, sqliteTimestamp(now), workspaceID, toolID)
	if err != nil {
		return model.Tool{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Tool{}, ErrNotFound
	}
	return s.GetToolByID(ctx, workspaceID, toolID)
}

func (s *SQLiteStore) ListToolCalls(ctx context.Context, workspaceID string) ([]model.ToolCall, error) {
	rows, err := s.db.QueryContext(ctx, sqliteToolCallSelect()+`
		WHERE tc.workspace_id = ?
		ORDER BY tc.created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.ToolCall, 0)
	for rows.Next() {
		item, err := scanSQLiteToolCall(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) ListToolCallsPage(ctx context.Context, workspaceID string, query model.ToolCallQuery) (model.ToolCallPage, error) {
	page, pageSize := normalizeToolCallPage(query.Page, query.PageSize)
	whereClause, args := buildSQLiteToolCallFilterClause(workspaceID, query)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls tc WHERE `+whereClause, args...).Scan(&total); err != nil {
		return model.ToolCallPage{}, mapSQLiteErr(err)
	}
	pagedArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, sqliteToolCallSelect()+`
		WHERE `+whereClause+`
		ORDER BY tc.created_at DESC, tc.id DESC
		LIMIT ? OFFSET ?
	`, pagedArgs...)
	if err != nil {
		return model.ToolCallPage{}, err
	}
	defer rows.Close()
	items := make([]model.ToolCall, 0)
	for rows.Next() {
		item, err := scanSQLiteToolCall(rows)
		if err != nil {
			return model.ToolCallPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return model.ToolCallPage{}, err
	}
	return model.ToolCallPage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (s *SQLiteStore) GetToolCallByID(ctx context.Context, workspaceID, callID string) (model.ToolCall, error) {
	return s.getToolCall(ctx, sqliteToolCallSelect()+`
		WHERE tc.workspace_id = ? AND tc.id = ?
	`, workspaceID, callID)
}

func (s *SQLiteStore) GetToolCallByApprovalID(ctx context.Context, workspaceID, approvalID string) (model.ToolCall, error) {
	return s.getToolCall(ctx, sqliteToolCallSelect()+`
		WHERE tc.workspace_id = ? AND tc.approval_id = ?
		ORDER BY CASE WHEN tc.status = 'approval_required' THEN 0 ELSE 1 END, tc.created_at ASC, tc.id ASC
		LIMIT 1
	`, workspaceID, approvalID)
}

func (s *SQLiteStore) CreateToolCall(ctx context.Context, input model.CreateToolCallInput) (model.ToolCall, error) {
	now := time.Now().UTC()
	approvalID := strings.TrimSpace(input.ApprovalID)
	id := ensureID("", "call")
	requestID := ensureID(input.RequestID, "req")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls (
			id, request_id, workspace_id, actor_id, actor_subject, actor_email, actor_name, tool_id, tool_key,
			status, risk_level, policy_decision, approval_id, duration_ms, input_redacted_json, input_execution_json, output_redacted_json,
			explanation_json, error_message, trace_id, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, requestID, input.WorkspaceID, input.ActorID, input.ActorSubject, input.ActorEmail, input.ActorName, input.ToolID, input.ToolKey,
		input.Status, input.RiskLevel, input.PolicyDecision, approvalID, input.DurationMs, string(defaultJSON(input.InputRedactedJSON)),
		string(defaultJSON(input.InputExecutionJSON)), string(defaultJSON(input.OutputRedactedJSON)), string(defaultToolCallExplanationJSON(input.Explanation)),
		input.ErrorMessage, input.TraceID, sqliteTimestamp(now))
	if err != nil {
		return model.ToolCall{}, mapSQLiteErr(err)
	}
	return s.GetToolCallByID(ctx, input.WorkspaceID, id)
}

func (s *SQLiteStore) UpdateToolCall(ctx context.Context, workspaceID, callID string, input model.UpdateToolCallInput) (model.ToolCall, error) {
	inputExecutionJSON := strings.TrimSpace(string(input.InputExecutionJSON))
	result, err := s.db.ExecContext(ctx, `
		UPDATE tool_calls
		SET status = ?,
		    duration_ms = ?,
		    output_redacted_json = ?,
		    error_message = ?,
		    trace_id = CASE WHEN ? = '' THEN trace_id ELSE ? END,
		    input_execution_json = CASE WHEN ? = '' THEN input_execution_json ELSE ? END
		WHERE workspace_id = ? AND id = ?
	`, input.Status, input.DurationMs, string(defaultJSON(input.OutputRedactedJSON)), input.ErrorMessage, input.TraceID, input.TraceID, inputExecutionJSON, inputExecutionJSON, workspaceID, callID)
	if err != nil {
		return model.ToolCall{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.ToolCall{}, ErrNotFound
	}
	return s.GetToolCallByID(ctx, workspaceID, callID)
}

func (s *SQLiteStore) ListConnectors(ctx context.Context, workspaceID string) ([]model.Connector, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
		FROM connectors
		WHERE workspace_id = ?
		ORDER BY created_at DESC, id DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Connector, 0)
	for rows.Next() {
		item, err := scanSQLiteConnector(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetConnectorByID(ctx context.Context, workspaceID, connectorID string) (model.Connector, error) {
	return s.getConnector(ctx, `
		SELECT id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at
		FROM connectors
		WHERE workspace_id = ? AND id = ?
	`, workspaceID, connectorID)
}

func (s *SQLiteStore) CreateConnector(ctx context.Context, input model.CreateConnectorInput) (model.Connector, error) {
	now := time.Now().UTC()
	typeName := strings.ToLower(strings.TrimSpace(input.Type))
	name := strings.ToLower(strings.TrimSpace(input.Name))
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = typeName + "." + name
	}
	id := ensureID("", "connector")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connectors (id, workspace_id, type, name, display_name, config_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input.WorkspaceID, typeName, name, displayName, string(defaultJSON(input.ConfigJSON)), input.Enabled, sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.Connector{}, mapSQLiteErr(err)
	}
	return s.GetConnectorByID(ctx, input.WorkspaceID, id)
}

func (s *SQLiteStore) UpdateConnector(ctx context.Context, workspaceID, connectorID string, input model.UpdateConnectorInput) (model.Connector, error) {
	now := time.Now().UTC()
	configJSON := strings.TrimSpace(string(input.ConfigJSON))
	result, err := s.db.ExecContext(ctx, `
		UPDATE connectors
		SET display_name = CASE WHEN ? = '' THEN display_name ELSE ? END,
		    config_json = CASE WHEN ? = '' THEN config_json ELSE ? END,
		    enabled = COALESCE(?, enabled),
		    updated_at = ?
		WHERE workspace_id = ? AND id = ?
	`, strings.TrimSpace(input.DisplayName), strings.TrimSpace(input.DisplayName), configJSON, configJSON, input.Enabled, sqliteTimestamp(now), workspaceID, connectorID)
	if err != nil {
		return model.Connector{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Connector{}, ErrNotFound
	}
	return s.GetConnectorByID(ctx, workspaceID, connectorID)
}

func (s *SQLiteStore) ListSecrets(ctx context.Context, workspaceID string) ([]model.Secret, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+secretColumnList+`
		FROM secrets
		WHERE workspace_id = ?
		ORDER BY created_at DESC, name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Secret, 0)
	for rows.Next() {
		item, err := scanSQLiteSecret(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetSecretByID(ctx context.Context, workspaceID, secretID string) (model.Secret, error) {
	return s.getSecret(ctx, `SELECT `+secretColumnList+` FROM secrets WHERE workspace_id = ? AND id = ?`, workspaceID, secretID)
}

func (s *SQLiteStore) GetSecretByName(ctx context.Context, workspaceID, name string) (model.Secret, error) {
	normalized, err := NormalizeSecretName(name)
	if err != nil {
		return model.Secret{}, ErrNotFound
	}
	return s.getSecret(ctx, `SELECT `+secretColumnList+` FROM secrets WHERE workspace_id = ? AND name = ?`, workspaceID, normalized)
}

func (s *SQLiteStore) CreateSecret(ctx context.Context, input model.CreateSecretInput) (model.Secret, error) {
	now := time.Now().UTC()
	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	secretType := NormalizeSecretType(input.SecretType)
	if err := ValidateSecretType(secretType); err != nil {
		return model.Secret{}, err
	}
	valueSource := NormalizeSecretValueSource(input.ValueSource)
	if err := ValidateSecretValueSource(valueSource); err != nil {
		return model.Secret{}, err
	}
	valueRef, err := NormalizeSecretValueRef(input.ValueRef)
	if err != nil {
		return model.Secret{}, err
	}
	metadata, err := NormalizeSecretMetadata(input.Metadata)
	if err != nil {
		return model.Secret{}, err
	}
	id := ensureID("", "secret")
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO secrets (id, workspace_id, workspace_org_id, name, description, enabled, secret_type, value_source, value_ref, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input.WorkspaceID, strings.TrimSpace(input.WorkspaceOrgID), name, strings.TrimSpace(input.Description), input.Enabled, secretType, valueSource, valueRef, string(metadata), sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.Secret{}, mapSQLiteErr(err)
	}
	return s.GetSecretByID(ctx, input.WorkspaceID, id)
}

func (s *SQLiteStore) UpdateSecret(ctx context.Context, workspaceID, secretID string, input model.UpdateSecretInput) (model.Secret, error) {
	now := time.Now().UTC()
	name, err := NormalizeSecretName(input.Name)
	if err != nil {
		return model.Secret{}, err
	}
	secretType := NormalizeSecretType(input.SecretType)
	if err := ValidateSecretType(secretType); err != nil {
		return model.Secret{}, err
	}
	valueSource := NormalizeSecretValueSource(input.ValueSource)
	if err := ValidateSecretValueSource(valueSource); err != nil {
		return model.Secret{}, err
	}
	valueRef, err := NormalizeSecretValueRef(input.ValueRef)
	if err != nil {
		return model.Secret{}, err
	}
	metadata, err := NormalizeSecretMetadata(input.Metadata)
	if err != nil {
		return model.Secret{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE secrets
		SET name = ?, description = ?, enabled = COALESCE(?, enabled), secret_type = ?, value_source = ?, value_ref = ?, metadata_json = ?, updated_at = ?
		WHERE workspace_id = ? AND id = ?
	`, name, strings.TrimSpace(input.Description), input.Enabled, secretType, valueSource, valueRef, string(metadata), sqliteTimestamp(now), workspaceID, secretID)
	if err != nil {
		return model.Secret{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.Secret{}, ErrNotFound
	}
	return s.GetSecretByID(ctx, workspaceID, secretID)
}

func (s *SQLiteStore) DeleteSecret(ctx context.Context, workspaceID, secretID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE workspace_id = ? AND id = ?`, workspaceID, secretID)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListPolicyRules(ctx context.Context, workspaceID string) ([]model.PolicyRule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+policyRuleColumnList+`
		FROM policy_rules
		WHERE workspace_id = ?
		ORDER BY priority ASC, created_at ASC, id ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.PolicyRule, 0)
	for rows.Next() {
		item, err := scanSQLitePolicyRule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetPolicyRuleByID(ctx context.Context, workspaceID, ruleID string) (model.PolicyRule, error) {
	return s.getPolicyRule(ctx, `SELECT `+policyRuleColumnList+` FROM policy_rules WHERE workspace_id = ? AND id = ?`, workspaceID, ruleID)
}

func (s *SQLiteStore) CreatePolicyRule(ctx context.Context, input model.CreatePolicyRuleInput) (model.PolicyRule, error) {
	now := time.Now().UTC()
	id := ensureID("", "policy_rule")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO policy_rules (id, workspace_id, name, description, enabled, priority, effect, connector_type, tool_name_pattern, operation_type, risk_level, resource_pattern, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input.WorkspaceID, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.Enabled, input.Priority,
		strings.ToLower(strings.TrimSpace(input.Effect)), normalizePolicyWildcard(input.ConnectorType), normalizePolicyWildcard(input.ToolNamePattern),
		normalizePolicyWildcard(input.OperationType), normalizePolicyWildcard(input.RiskLevel), normalizePolicyWildcard(input.ResourcePattern),
		strings.TrimSpace(input.Reason), sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.PolicyRule{}, mapSQLiteErr(err)
	}
	return s.GetPolicyRuleByID(ctx, input.WorkspaceID, id)
}

func (s *SQLiteStore) UpdatePolicyRule(ctx context.Context, workspaceID, ruleID string, input model.UpdatePolicyRuleInput) (model.PolicyRule, error) {
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE policy_rules
		SET name = ?, description = ?, enabled = COALESCE(?, enabled), priority = COALESCE(?, priority), effect = ?, connector_type = ?,
		    tool_name_pattern = ?, operation_type = ?, risk_level = ?, resource_pattern = ?, reason = ?, updated_at = ?
		WHERE workspace_id = ? AND id = ?
	`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.Enabled, input.Priority, strings.ToLower(strings.TrimSpace(input.Effect)),
		normalizePolicyWildcard(input.ConnectorType), normalizePolicyWildcard(input.ToolNamePattern), normalizePolicyWildcard(input.OperationType),
		normalizePolicyWildcard(input.RiskLevel), normalizePolicyWildcard(input.ResourcePattern), strings.TrimSpace(input.Reason), sqliteTimestamp(now), workspaceID, ruleID)
	if err != nil {
		return model.PolicyRule{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.PolicyRule{}, ErrNotFound
	}
	return s.GetPolicyRuleByID(ctx, workspaceID, ruleID)
}

func (s *SQLiteStore) DeletePolicyRule(ctx context.Context, workspaceID, ruleID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM policy_rules WHERE workspace_id = ? AND id = ?`, workspaceID, ruleID)
	if err != nil {
		return mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListApprovalRequests(ctx context.Context, workspaceID string) ([]model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+approvalRequestColumnList+`
		FROM approval_requests
		WHERE workspace_id = ?
		ORDER BY created_at DESC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.ApprovalRequest, 0)
	for rows.Next() {
		item, err := scanSQLiteApprovalRequest(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) GetApprovalRequestByID(ctx context.Context, workspaceID, approvalID string) (model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	return s.getApprovalRequest(ctx, `SELECT `+approvalRequestColumnList+` FROM approval_requests WHERE workspace_id = ? AND id = ?`, workspaceID, approvalID)
}

func (s *SQLiteStore) CreateApprovalRequest(ctx context.Context, input model.CreateApprovalRequestInput) (model.ApprovalRequest, error) {
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
	id := ensureID("", "approval")
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO approval_requests (
			id, workspace_id, tool_key, tool_display_name, status, requested_by, reviewed_by, reason,
			fingerprint, adapter, action_type, target, canonical_target, content_encoding, content_hash,
			script_hash, resolved_file_identity, parent_identity, decision_payload_json, expires_at, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, 'pending', ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, input.WorkspaceID, strings.TrimSpace(input.ToolKey), toolDisplayName, requestedBy, strings.TrimSpace(input.Reason),
		strings.TrimSpace(input.Fingerprint), strings.TrimSpace(input.Adapter), strings.TrimSpace(input.ActionType), strings.TrimSpace(input.Target),
		strings.TrimSpace(input.CanonicalTarget), strings.TrimSpace(input.ContentEncoding), strings.TrimSpace(input.ContentHash), strings.TrimSpace(input.ScriptHash),
		strings.TrimSpace(input.ResolvedFileIdentity), strings.TrimSpace(input.ParentIdentity), string(defaultJSON(input.DecisionPayloadJSON)),
		sqliteTimestamp(expiresAt), sqliteTimestamp(now), sqliteTimestamp(now))
	if err != nil {
		return model.ApprovalRequest{}, mapSQLiteErr(err)
	}
	return s.GetApprovalRequestByID(ctx, input.WorkspaceID, id)
}

func (s *SQLiteStore) UpdateApprovalRequest(ctx context.Context, workspaceID, approvalID string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	current, err := s.GetApprovalRequestByID(ctx, workspaceID, approvalID)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if strings.EqualFold(strings.TrimSpace(current.Status), "expired") {
		return model.ApprovalRequest{}, ErrExpired
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = ?, reviewed_by = ?, reason = CASE WHEN ? = '' THEN reason ELSE ? END, updated_at = ?
		WHERE workspace_id = ? AND id = ?
	`, strings.TrimSpace(input.Status), strings.TrimSpace(input.ReviewedBy), strings.TrimSpace(input.Reason), strings.TrimSpace(input.Reason), sqliteTimestamp(now), workspaceID, approvalID)
	if err != nil {
		return model.ApprovalRequest{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.ApprovalRequest{}, ErrNotFound
	}
	return s.GetApprovalRequestByID(ctx, workspaceID, approvalID)
}

func (s *SQLiteStore) TransitionApprovalRequest(ctx context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	if err := s.expirePendingApprovals(ctx, workspaceID); err != nil {
		return model.ApprovalRequest{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = ?, reviewed_by = ?, reason = CASE WHEN ? = '' THEN reason ELSE ? END, updated_at = ?
		WHERE workspace_id = ? AND id = ? AND status = ?
	`, strings.TrimSpace(input.Status), strings.TrimSpace(input.ReviewedBy), strings.TrimSpace(input.Reason), strings.TrimSpace(input.Reason), sqliteTimestamp(now), workspaceID, approvalID, strings.TrimSpace(fromStatus))
	if err != nil {
		return model.ApprovalRequest{}, mapSQLiteErr(err)
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		return s.GetApprovalRequestByID(ctx, workspaceID, approvalID)
	}
	current, getErr := s.getApprovalRequest(ctx, `SELECT `+approvalRequestColumnList+` FROM approval_requests WHERE workspace_id = ? AND id = ?`, workspaceID, approvalID)
	if getErr != nil {
		return model.ApprovalRequest{}, getErr
	}
	if strings.EqualFold(strings.TrimSpace(current.Status), "expired") {
		return model.ApprovalRequest{}, ErrExpired
	}
	return model.ApprovalRequest{}, ErrConflict
}

func (s *SQLiteStore) ensureSchema(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			slug TEXT NOT NULL UNIQUE,
			zitadel_organization_id TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			zitadel_user_id TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'owner',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
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
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS policy_rules_workspace_order_idx ON policy_rules (workspace_id, priority, created_at, id)`,
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
			decision_payload_json TEXT NOT NULL DEFAULT '{}',
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS approval_requests_workspace_active_fingerprint_idx
			ON approval_requests (workspace_id, fingerprint)
			WHERE fingerprint <> '' AND status IN ('pending', 'approved')`,
		`CREATE TABLE IF NOT EXISTS connectors (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
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
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
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
			input_schema_json TEXT NOT NULL DEFAULT '{}',
			output_schema_json TEXT NOT NULL DEFAULT '{}',
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
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
			input_redacted_json TEXT NOT NULL DEFAULT '{}',
			input_execution_json TEXT NOT NULL DEFAULT '{}',
			output_redacted_json TEXT NOT NULL DEFAULT '{}',
			explanation_json TEXT NOT NULL DEFAULT '{}',
			error_message TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("init sqlite schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) ensureWorkspaceSeed(ctx context.Context, input model.BootstrapInput) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces`).Scan(&count); err != nil {
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
	_, err := s.CreateWorkspace(ctx, model.CreateWorkspaceInput{Name: input.WorkspaceName, Slug: input.WorkspaceSlug, ZitadelOrganizationID: input.WorkspaceOrganizationID})
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}

func (s *SQLiteStore) ensureBuiltinTools(ctx context.Context) error {
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

func (s *SQLiteStore) ensureBuiltinConnectors(ctx context.Context, inputs []model.BootstrapConnectorInput) error {
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

func (s *SQLiteStore) expirePendingApprovals(ctx context.Context, workspaceID string) error {
	now := sqliteTimestamp(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'expired', updated_at = ?
		WHERE workspace_id = ? AND status = 'pending' AND expires_at <= ?
	`, now, workspaceID, now)
	return err
}

func (s *SQLiteStore) insertWorkspace(ctx context.Context, workspace model.Workspace) (model.Workspace, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workspaces (id, name, slug, zitadel_organization_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, workspace.ID, workspace.Name, workspace.Slug, workspace.ZitadelOrganizationID, sqliteTimestamp(workspace.CreatedAt), sqliteTimestamp(workspace.UpdatedAt))
	if err != nil {
		return model.Workspace{}, mapSQLiteErr(err)
	}
	return s.GetWorkspaceByOrganizationID(ctx, workspace.ZitadelOrganizationID)
}

func (s *SQLiteStore) getWorkspace(ctx context.Context, query string, args ...any) (model.Workspace, error) {
	item, err := scanSQLiteWorkspace(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.Workspace{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getUser(ctx context.Context, query string, args ...any) (model.User, error) {
	item, err := scanSQLiteUser(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.User{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getTool(ctx context.Context, query string, args ...any) (model.Tool, error) {
	item, err := scanSQLiteTool(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.Tool{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getToolCall(ctx context.Context, query string, args ...any) (model.ToolCall, error) {
	item, err := scanSQLiteToolCall(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.ToolCall{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getConnector(ctx context.Context, query string, args ...any) (model.Connector, error) {
	item, err := scanSQLiteConnector(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.Connector{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getSecret(ctx context.Context, query string, args ...any) (model.Secret, error) {
	item, err := scanSQLiteSecret(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.Secret{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getPolicyRule(ctx context.Context, query string, args ...any) (model.PolicyRule, error) {
	item, err := scanSQLitePolicyRule(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.PolicyRule{}, mapSQLiteErr(err)
	}
	return item, nil
}

func (s *SQLiteStore) getApprovalRequest(ctx context.Context, query string, args ...any) (model.ApprovalRequest, error) {
	item, err := scanSQLiteApprovalRequest(s.db.QueryRowContext(ctx, query, args...))
	if err != nil {
		return model.ApprovalRequest{}, mapSQLiteErr(err)
	}
	return item, nil
}

func sqliteToolSelect() string {
	return `SELECT id, workspace_id, namespace, name, display_name, description, operation_type, risk_level,
	       requires_approval, input_schema_json, output_schema_json, enabled, created_at, updated_at
	FROM tools`
}

func sqliteToolCallSelect() string {
	return `SELECT tc.id, tc.request_id, tc.workspace_id, tc.actor_id, tc.actor_subject, tc.actor_email, tc.actor_name, tc.tool_id, tc.tool_key,
	       tc.status, tc.risk_level, tc.policy_decision, tc.approval_id, tc.duration_ms, tc.input_redacted_json, tc.input_execution_json, tc.output_redacted_json,
	       tc.explanation_json, tc.error_message, tc.trace_id, tc.created_at,
	       COALESCE(ar.status, '') AS approval_status
	FROM tool_calls tc
	LEFT JOIN approval_requests ar ON ar.id = tc.approval_id AND ar.workspace_id = tc.workspace_id
	`
}

func buildSQLiteToolCallFilterClause(workspaceID string, query model.ToolCallQuery) (string, []any) {
	clauses := []string{"tc.workspace_id = ?"}
	args := []any{workspaceID}
	if tool := strings.TrimSpace(query.Tool); tool != "" {
		clauses = append(clauses, "tc.tool_key = ?")
		args = append(args, tool)
	}
	if len(query.Statuses) > 0 {
		placeholders := make([]string, 0, len(query.Statuses))
		for _, status := range query.Statuses {
			trimmed := strings.TrimSpace(status)
			if trimmed == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, trimmed)
		}
		if len(placeholders) > 0 {
			clauses = append(clauses, "tc.status IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if query.From != nil {
		clauses = append(clauses, "tc.created_at >= ?")
		args = append(args, sqliteTimestamp(query.From.UTC()))
	}
	if query.To != nil {
		clauses = append(clauses, "tc.created_at <= ?")
		args = append(args, sqliteTimestamp(query.To.UTC()))
	}
	return strings.Join(clauses, " AND "), args
}

type sqliteScanner interface{ Scan(...any) error }

func scanSQLiteWorkspace(row sqliteScanner) (model.Workspace, error) {
	var item model.Workspace
	var createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.Name, &item.Slug, &item.ZitadelOrganizationID, &createdAt, &updatedAt); err != nil {
		return model.Workspace{}, err
	}
	var err error
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.Workspace{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.Workspace{}, err
	}
	return item, nil
}

func scanSQLiteUser(row sqliteScanner) (model.User, error) {
	var item model.User
	var createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.ZitadelUserID, &item.Email, &item.Name, &item.Role, &createdAt, &updatedAt); err != nil {
		return model.User{}, err
	}
	var err error
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.User{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.User{}, err
	}
	return item, nil
}

func scanSQLiteTool(row sqliteScanner) (model.Tool, error) {
	var item model.Tool
	var requiresApproval, enabled, inputJSON, outputJSON, createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.Namespace, &item.Name, &item.DisplayName, &item.Description, &item.OperationType, &item.RiskLevel, &requiresApproval, &inputJSON, &outputJSON, &enabled, &createdAt, &updatedAt); err != nil {
		return model.Tool{}, err
	}
	var err error
	item.RequiresApproval, err = sqliteParseBool(requiresApproval)
	if err != nil {
		return model.Tool{}, err
	}
	item.Enabled, err = sqliteParseBool(enabled)
	if err != nil {
		return model.Tool{}, err
	}
	item.InputSchemaJSON = sqliteRawJSON(inputJSON)
	item.OutputSchemaJSON = sqliteRawJSON(outputJSON)
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.Tool{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.Tool{}, err
	}
	return item, nil
}

func scanSQLiteToolCall(row sqliteScanner) (model.ToolCall, error) {
	var item model.ToolCall
	var inputJSON, executionJSON, outputJSON, explanationJSON, createdAt any
	if err := row.Scan(&item.ID, &item.RequestID, &item.WorkspaceID, &item.ActorID, &item.ActorSubject, &item.ActorEmail, &item.ActorName,
		&item.ToolID, &item.ToolKey, &item.Status, &item.RiskLevel, &item.PolicyDecision, &item.ApprovalID, &item.DurationMs,
		&inputJSON, &executionJSON, &outputJSON, &explanationJSON, &item.ErrorMessage, &item.TraceID, &createdAt, &item.ApprovalStatus); err != nil {
		return model.ToolCall{}, err
	}
	var err error
	item.InputRedactedJSON = sqliteRawJSON(inputJSON)
	item.InputExecutionJSON = sqliteRawJSON(executionJSON)
	item.OutputRedactedJSON = sqliteRawJSON(outputJSON)
	item.Explanation, err = decodeToolCallExplanationJSON(sqliteRawJSON(explanationJSON))
	if err != nil {
		return model.ToolCall{}, err
	}
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.ToolCall{}, err
	}
	return item, nil
}

func scanSQLiteConnector(row sqliteScanner) (model.Connector, error) {
	var item model.Connector
	var configJSON, enabled, createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.Type, &item.Name, &item.DisplayName, &configJSON, &enabled, &createdAt, &updatedAt); err != nil {
		return model.Connector{}, err
	}
	var err error
	item.ConfigJSON = sqliteRawJSON(configJSON)
	item.Enabled, err = sqliteParseBool(enabled)
	if err != nil {
		return model.Connector{}, err
	}
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.Connector{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.Connector{}, err
	}
	return item, nil
}

func scanSQLiteSecret(row sqliteScanner) (model.Secret, error) {
	var item model.Secret
	var metadata, enabled, createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.WorkspaceOrgID, &item.Name, &item.Description, &enabled, &item.SecretType, &item.ValueSource, &item.ValueRef, &metadata, &createdAt, &updatedAt); err != nil {
		return model.Secret{}, err
	}
	var err error
	item.Enabled, err = sqliteParseBool(enabled)
	if err != nil {
		return model.Secret{}, err
	}
	item.Metadata = sqliteRawJSON(metadata)
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.Secret{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.Secret{}, err
	}
	return item, nil
}

func scanSQLitePolicyRule(row sqliteScanner) (model.PolicyRule, error) {
	var item model.PolicyRule
	var enabled, createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.Name, &item.Description, &enabled, &item.Priority, &item.Effect,
		&item.ConnectorType, &item.ToolNamePattern, &item.OperationType, &item.RiskLevel, &item.ResourcePattern,
		&item.Reason, &createdAt, &updatedAt); err != nil {
		return model.PolicyRule{}, err
	}
	var err error
	item.Enabled, err = sqliteParseBool(enabled)
	if err != nil {
		return model.PolicyRule{}, err
	}
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.PolicyRule{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.PolicyRule{}, err
	}
	return item, nil
}

func scanSQLiteApprovalRequest(row sqliteScanner) (model.ApprovalRequest, error) {
	var item model.ApprovalRequest
	var payloadJSON, expiresAt, createdAt, updatedAt any
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.ToolKey, &item.ToolDisplayName, &item.Status, &item.RequestedBy,
		&item.ReviewedBy, &item.Reason, &item.Fingerprint, &item.Adapter, &item.ActionType, &item.Target,
		&item.CanonicalTarget, &item.ContentEncoding, &item.ContentHash, &item.ScriptHash, &item.ResolvedFileIdentity,
		&item.ParentIdentity, &payloadJSON, &expiresAt, &createdAt, &updatedAt); err != nil {
		return model.ApprovalRequest{}, err
	}
	var err error
	item.DecisionPayloadJSON = sqliteRawJSON(payloadJSON)
	item.ExpiresAt, err = sqliteParseTime(expiresAt)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	item.CreatedAt, err = sqliteParseTime(createdAt)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	item.UpdatedAt, err = sqliteParseTime(updatedAt)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	return item, nil
}

func sqliteTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func sqliteParseTime(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		return parseSQLiteTimeString(v)
	case []byte:
		return parseSQLiteTimeString(string(v))
	default:
		return time.Time{}, fmt.Errorf("unsupported sqlite timestamp type %T", value)
	}
}

func parseSQLiteTimeString(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse sqlite timestamp %q", raw)
}

func sqliteParseBool(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case int64:
		return v != 0, nil
	case int:
		return v != 0, nil
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(v))
		return trimmed == "1" || trimmed == "true" || trimmed == "t" || trimmed == "yes", nil
	case []byte:
		return sqliteParseBool(string(v))
	default:
		return false, fmt.Errorf("unsupported sqlite bool type %T", value)
	}
}

func sqliteRawJSON(value any) json.RawMessage {
	switch v := value.(type) {
	case nil:
		return json.RawMessage(`{}`)
	case []byte:
		if len(v) == 0 {
			return json.RawMessage(`{}`)
		}
		return json.RawMessage(append([]byte(nil), v...))
	case string:
		if strings.TrimSpace(v) == "" {
			return json.RawMessage(`{}`)
		}
		return json.RawMessage([]byte(v))
	case json.RawMessage:
		return cloneJSON(defaultJSON(v))
	default:
		raw, err := json.Marshal(v)
		if err != nil || len(raw) == 0 {
			return json.RawMessage(`{}`)
		}
		return json.RawMessage(raw)
	}
}

func mapSQLiteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "unique constraint") || strings.Contains(lower, "constraint failed") || strings.Contains(lower, "sqlite_constraint") {
		return ErrConflict
	}
	return err
}

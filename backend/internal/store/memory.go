package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
)

type MemoryStore struct {
	mu                              sync.RWMutex
	workspaces                      map[string]model.Workspace
	workspacesBySlug                map[string]string
	workspacesByOrgID               map[string]string
	users                           map[string]model.User
	usersByWorkspaceUser            map[string]string
	tools                           map[string]model.Tool
	toolsByWorkspaceKey             map[string]string
	calls                           map[string]model.ToolCall
	callsByWorkspace                map[string][]string
	callsByApprovalID               map[string]string
	approvals                       map[string]model.ApprovalRequest
	approvalsByWorkspace            map[string][]string
	approvalsByWorkspaceFingerprint map[string]string
	connectors                      map[string]model.Connector
	connectorsByWorkspace           map[string][]string
	connectorsByWorkspaceKey        map[string]string
	secrets                         map[string]model.Secret
	secretsByWorkspace              map[string][]string
	secretsByWorkspaceName          map[string]string
	policyRules                     map[string]model.PolicyRule
	policyRulesByWorkspace          map[string][]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		workspaces:                      map[string]model.Workspace{},
		workspacesBySlug:                map[string]string{},
		workspacesByOrgID:               map[string]string{},
		users:                           map[string]model.User{},
		usersByWorkspaceUser:            map[string]string{},
		tools:                           map[string]model.Tool{},
		toolsByWorkspaceKey:             map[string]string{},
		calls:                           map[string]model.ToolCall{},
		callsByWorkspace:                map[string][]string{},
		callsByApprovalID:               map[string]string{},
		approvals:                       map[string]model.ApprovalRequest{},
		approvalsByWorkspace:            map[string][]string{},
		approvalsByWorkspaceFingerprint: map[string]string{},
		connectors:                      map[string]model.Connector{},
		connectorsByWorkspace:           map[string][]string{},
		connectorsByWorkspaceKey:        map[string]string{},
		secrets:                         map[string]model.Secret{},
		secretsByWorkspace:              map[string][]string{},
		secretsByWorkspaceName:          map[string]string{},
		policyRules:                     map[string]model.PolicyRule{},
		policyRulesByWorkspace:          map[string][]string{},
	}
}

func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

func (s *MemoryStore) Bootstrap(_ context.Context, input model.BootstrapInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.workspaces) == 0 {
		if strings.TrimSpace(input.WorkspaceName) == "" {
			input.WorkspaceName = "Default Workspace"
		}
		if strings.TrimSpace(input.WorkspaceSlug) == "" {
			input.WorkspaceSlug = "default"
		}
		if strings.TrimSpace(input.WorkspaceOrganizationID) == "" {
			input.WorkspaceOrganizationID = "local-org"
		}
		ws := s.newWorkspaceLocked(model.CreateWorkspaceInput{
			Name:                  input.WorkspaceName,
			Slug:                  input.WorkspaceSlug,
			ZitadelOrganizationID: input.WorkspaceOrganizationID,
		})
		s.putWorkspaceLocked(ws)
	}

	for _, workspace := range s.workspaces {
		for _, input := range model.BuiltinToolInputs(workspace.ID) {
			if _, ok := s.toolsByWorkspaceKey[s.toolIndexKey(workspace.ID, input.Namespace+"."+input.Name)]; ok {
				continue
			}
			tool := s.newToolLocked(input)
			s.putToolLocked(tool)
		}
		for _, connector := range input.Connectors {
			if _, ok := s.connectorsByWorkspaceKey[s.connectorIndexKey(workspace.ID, connector.Type, connector.Name)]; ok {
				continue
			}
			created := s.newConnectorLocked(workspace.ID, connector)
			s.putConnectorLocked(created)
		}
	}

	return nil
}

func (s *MemoryStore) ListWorkspaces(context.Context) ([]model.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]model.Workspace, 0, len(s.workspaces))
	for _, workspace := range s.workspaces {
		items = append(items, workspace)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) GetWorkspaceBySlug(_ context.Context, slug string) (model.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if id, ok := s.workspacesBySlug[strings.ToLower(strings.TrimSpace(slug))]; ok {
		return s.workspaces[id], nil
	}
	return model.Workspace{}, ErrNotFound
}

func (s *MemoryStore) GetWorkspaceByOrganizationID(_ context.Context, organizationID string) (model.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if id, ok := s.workspacesByOrgID[strings.TrimSpace(organizationID)]; ok {
		return s.workspaces[id], nil
	}
	return model.Workspace{}, ErrNotFound
}

func (s *MemoryStore) CreateWorkspace(_ context.Context, input model.CreateWorkspaceInput) (model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.workspacesBySlug[strings.ToLower(strings.TrimSpace(input.Slug))]; exists {
		return model.Workspace{}, ErrConflict
	}
	if _, exists := s.workspacesByOrgID[strings.TrimSpace(input.ZitadelOrganizationID)]; exists {
		return model.Workspace{}, ErrConflict
	}

	workspace := s.newWorkspaceLocked(input)
	s.putWorkspaceLocked(workspace)
	return workspace, nil
}

func (s *MemoryStore) UpsertUser(_ context.Context, input model.UpsertUserInput) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.userIndexKey(input.WorkspaceID, input.ZitadelUserID)
	if id, ok := s.usersByWorkspaceUser[key]; ok {
		user := s.users[id]
		user.Email = input.Email
		user.Name = input.Name
		user.Role = input.Role
		user.UpdatedAt = time.Now().UTC()
		s.users[id] = user
		return user, nil
	}

	now := time.Now().UTC()
	user := model.User{
		ID:            uuid.NewString(),
		WorkspaceID:   input.WorkspaceID,
		ZitadelUserID: input.ZitadelUserID,
		Email:         input.Email,
		Name:          input.Name,
		Role:          input.Role,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.users[user.ID] = user
	s.usersByWorkspaceUser[key] = user.ID
	return user, nil
}

func (s *MemoryStore) ListTools(_ context.Context, workspaceID string) ([]model.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]model.Tool, 0)
	for _, tool := range s.tools {
		if tool.WorkspaceID == workspaceID {
			if strings.EqualFold(strings.TrimSpace(tool.Namespace), "agent_guard") {
				continue
			}
			items = append(items, tool)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) ListApprovalRequests(_ context.Context, workspaceID string) ([]model.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	ids := append([]string(nil), s.approvalsByWorkspace[workspaceID]...)
	items := make([]model.ApprovalRequest, 0, len(ids))
	for _, id := range ids {
		approval := s.approvals[id]
		if approval.WorkspaceID != workspaceID {
			continue
		}
		if expireApprovalIfNeeded(&approval, now) {
			s.approvals[id] = approval
		}
		items = append(items, approval)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) GetApprovalRequestByID(_ context.Context, workspaceID, approvalID string) (model.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	approval, ok := s.approvals[approvalID]
	if !ok || approval.WorkspaceID != workspaceID {
		return model.ApprovalRequest{}, ErrNotFound
	}
	if expireApprovalIfNeeded(&approval, time.Now().UTC()) {
		s.approvals[approvalID] = approval
	}
	return approval, nil
}

func (s *MemoryStore) GetToolByID(_ context.Context, workspaceID, toolID string) (model.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tool, ok := s.tools[toolID]
	if !ok || tool.WorkspaceID != workspaceID {
		return model.Tool{}, ErrNotFound
	}
	return tool, nil
}

func (s *MemoryStore) GetToolByKey(_ context.Context, workspaceID, key string) (model.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if id, ok := s.toolsByWorkspaceKey[s.toolIndexKey(workspaceID, key)]; ok {
		return s.tools[id], nil
	}
	return model.Tool{}, ErrNotFound
}

func (s *MemoryStore) CreateTool(_ context.Context, input model.CreateToolInput) (model.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.toolIndexKey(input.WorkspaceID, input.Namespace+"."+input.Name)
	if _, exists := s.toolsByWorkspaceKey[key]; exists {
		return model.Tool{}, ErrConflict
	}

	tool := s.newToolLocked(input)
	s.putToolLocked(tool)
	return tool, nil
}

func (s *MemoryStore) UpdateTool(_ context.Context, workspaceID, toolID string, input model.UpdateToolInput) (model.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tool, ok := s.tools[toolID]
	if !ok || tool.WorkspaceID != workspaceID {
		return model.Tool{}, ErrNotFound
	}
	if strings.TrimSpace(input.DisplayName) != "" {
		tool.DisplayName = strings.TrimSpace(input.DisplayName)
	}
	if strings.TrimSpace(input.Description) != "" {
		tool.Description = strings.TrimSpace(input.Description)
	}
	if strings.TrimSpace(input.OperationType) != "" {
		tool.OperationType = strings.TrimSpace(input.OperationType)
	}
	if strings.TrimSpace(input.RiskLevel) != "" {
		tool.RiskLevel = strings.TrimSpace(input.RiskLevel)
	}
	if input.RequiresApproval != nil {
		tool.RequiresApproval = *input.RequiresApproval
	}
	if len(input.InputSchemaJSON) > 0 {
		tool.InputSchemaJSON = cloneJSON(defaultJSON(input.InputSchemaJSON))
	}
	if len(input.OutputSchemaJSON) > 0 {
		tool.OutputSchemaJSON = cloneJSON(defaultJSON(input.OutputSchemaJSON))
	}
	if input.Enabled != nil {
		tool.Enabled = *input.Enabled
	}
	tool.UpdatedAt = time.Now().UTC()
	s.tools[toolID] = tool
	return tool, nil
}

func (s *MemoryStore) ListToolCalls(_ context.Context, workspaceID string) ([]model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := append([]string(nil), s.callsByWorkspace[workspaceID]...)
	items := make([]model.ToolCall, 0, len(ids))
	for _, id := range ids {
		items = append(items, s.withApprovalStatusLocked(s.calls[id]))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) ListToolCallsPage(_ context.Context, workspaceID string, query model.ToolCallQuery) (model.ToolCallPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := s.filterToolCallsLocked(workspaceID, query)
	total := int64(len(items))
	page, pageSize := normalizeToolCallPage(query.Page, query.PageSize)
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	window := append([]model.ToolCall(nil), items[start:end]...)
	return model.ToolCallPage{
		Items:    window,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func (s *MemoryStore) GetToolCallByID(_ context.Context, workspaceID, callID string) (model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	call, ok := s.calls[callID]
	if !ok || call.WorkspaceID != workspaceID {
		return model.ToolCall{}, ErrNotFound
	}
	return s.withApprovalStatusLocked(call), nil
}

func (s *MemoryStore) GetToolCallByApprovalID(_ context.Context, workspaceID, approvalID string) (model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.callsByApprovalID[approvalID]
	if !ok {
		return model.ToolCall{}, ErrNotFound
	}
	call, ok := s.calls[id]
	if !ok || call.WorkspaceID != workspaceID {
		return model.ToolCall{}, ErrNotFound
	}
	return s.withApprovalStatusLocked(call), nil
}

func (s *MemoryStore) CreateToolCall(_ context.Context, input model.CreateToolCallInput) (model.ToolCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	call := model.ToolCall{
		ID:                 ensureID(input.RequestID, "call"),
		RequestID:          ensureID(input.RequestID, "req"),
		WorkspaceID:        input.WorkspaceID,
		ActorID:            input.ActorID,
		ActorSubject:       input.ActorSubject,
		ActorEmail:         input.ActorEmail,
		ActorName:          input.ActorName,
		ToolID:             input.ToolID,
		ToolKey:            input.ToolKey,
		Status:             input.Status,
		RiskLevel:          input.RiskLevel,
		PolicyDecision:     input.PolicyDecision,
		ApprovalID:         input.ApprovalID,
		DurationMs:         input.DurationMs,
		InputRedactedJSON:  cloneJSON(input.InputRedactedJSON),
		InputExecutionJSON: cloneJSON(defaultJSON(input.InputExecutionJSON)),
		OutputRedactedJSON: cloneJSON(input.OutputRedactedJSON),
		Explanation:        cloneToolCallExplanation(input.Explanation),
		ErrorMessage:       input.ErrorMessage,
		TraceID:            input.TraceID,
		CreatedAt:          now,
	}
	if call.ID == call.RequestID {
		call.ID = uuid.NewString()
	}
	if strings.TrimSpace(call.RequestID) == "" {
		call.RequestID = uuid.NewString()
	}

	s.calls[call.ID] = call
	s.callsByWorkspace[call.WorkspaceID] = append(s.callsByWorkspace[call.WorkspaceID], call.ID)
	if strings.TrimSpace(call.ApprovalID) != "" {
		if _, exists := s.callsByApprovalID[call.ApprovalID]; !exists {
			s.callsByApprovalID[call.ApprovalID] = call.ID
		}
	}
	return s.withApprovalStatusLocked(call), nil
}

func (s *MemoryStore) UpdateToolCall(_ context.Context, workspaceID, callID string, input model.UpdateToolCallInput) (model.ToolCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	call, ok := s.calls[callID]
	if !ok || call.WorkspaceID != workspaceID {
		return model.ToolCall{}, ErrNotFound
	}
	call.Status = input.Status
	call.DurationMs = input.DurationMs
	if len(input.InputExecutionJSON) > 0 {
		call.InputExecutionJSON = cloneJSON(defaultJSON(input.InputExecutionJSON))
	}
	call.OutputRedactedJSON = cloneJSON(defaultJSON(input.OutputRedactedJSON))
	call.ErrorMessage = input.ErrorMessage
	if strings.TrimSpace(input.TraceID) != "" {
		call.TraceID = input.TraceID
	}
	s.calls[callID] = call
	return s.withApprovalStatusLocked(call), nil
}

func cloneToolCallExplanation(input *model.ToolCallExplanation) *model.ToolCallExplanation {
	if input == nil {
		return nil
	}
	signals := append([]string(nil), input.Signals...)
	return &model.ToolCallExplanation{
		TargetCategory: input.TargetCategory,
		RiskLevel:      input.RiskLevel,
		MatchedRule:    input.MatchedRule,
		Signals:        signals,
	}
}

func (s *MemoryStore) ListConnectors(_ context.Context, workspaceID string) ([]model.Connector, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := append([]string(nil), s.connectorsByWorkspace[workspaceID]...)
	items := make([]model.Connector, 0, len(ids))
	for _, id := range ids {
		items = append(items, s.connectors[id])
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) GetConnectorByID(_ context.Context, workspaceID, connectorID string) (model.Connector, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	connector, ok := s.connectors[connectorID]
	if !ok || connector.WorkspaceID != workspaceID {
		return model.Connector{}, ErrNotFound
	}
	return connector, nil
}

func (s *MemoryStore) CreateConnector(_ context.Context, input model.CreateConnectorInput) (model.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.connectorIndexKey(input.WorkspaceID, input.Type, input.Name)
	if _, exists := s.connectorsByWorkspaceKey[key]; exists {
		return model.Connector{}, ErrConflict
	}
	connector := s.newConnectorLocked(input.WorkspaceID, model.BootstrapConnectorInput{
		Type:        input.Type,
		Name:        input.Name,
		DisplayName: input.DisplayName,
		ConfigJSON:  defaultJSON(input.ConfigJSON),
		Enabled:     input.Enabled,
	})
	s.putConnectorLocked(connector)
	return connector, nil
}

func (s *MemoryStore) UpdateConnector(_ context.Context, workspaceID, connectorID string, input model.UpdateConnectorInput) (model.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	connector, ok := s.connectors[connectorID]
	if !ok || connector.WorkspaceID != workspaceID {
		return model.Connector{}, ErrNotFound
	}
	if strings.TrimSpace(input.DisplayName) != "" {
		connector.DisplayName = strings.TrimSpace(input.DisplayName)
	}
	if len(input.ConfigJSON) > 0 {
		connector.ConfigJSON = cloneJSON(defaultJSON(input.ConfigJSON))
	}
	if input.Enabled != nil {
		connector.Enabled = *input.Enabled
	}
	connector.UpdatedAt = time.Now().UTC()
	s.connectors[connectorID] = connector
	return connector, nil
}

func (s *MemoryStore) ListPolicyRules(_ context.Context, workspaceID string) ([]model.PolicyRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := append([]string(nil), s.policyRulesByWorkspace[workspaceID]...)
	items := make([]model.PolicyRule, 0, len(ids))
	for _, id := range ids {
		rule, ok := s.policyRules[id]
		if ok && rule.WorkspaceID == workspaceID {
			items = append(items, rule)
		}
	}
	sortPolicyRules(items)
	return items, nil
}

func (s *MemoryStore) GetPolicyRuleByID(_ context.Context, workspaceID, ruleID string) (model.PolicyRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rule, ok := s.policyRules[ruleID]
	if !ok || rule.WorkspaceID != workspaceID {
		return model.PolicyRule{}, ErrNotFound
	}
	return rule, nil
}

func (s *MemoryStore) CreatePolicyRule(_ context.Context, input model.CreatePolicyRuleInput) (model.PolicyRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rule := newPolicyRule(input)
	s.policyRules[rule.ID] = rule
	s.policyRulesByWorkspace[rule.WorkspaceID] = append(s.policyRulesByWorkspace[rule.WorkspaceID], rule.ID)
	return rule, nil
}

func (s *MemoryStore) UpdatePolicyRule(_ context.Context, workspaceID, ruleID string, input model.UpdatePolicyRuleInput) (model.PolicyRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rule, ok := s.policyRules[ruleID]
	if !ok || rule.WorkspaceID != workspaceID {
		return model.PolicyRule{}, ErrNotFound
	}
	rule.Name = strings.TrimSpace(input.Name)
	rule.Description = strings.TrimSpace(input.Description)
	if input.Enabled != nil {
		rule.Enabled = *input.Enabled
	}
	if input.Priority != nil {
		rule.Priority = *input.Priority
	}
	rule.Effect = strings.ToLower(strings.TrimSpace(input.Effect))
	rule.ConnectorType = normalizePolicyWildcard(input.ConnectorType)
	rule.ToolNamePattern = normalizePolicyWildcard(input.ToolNamePattern)
	rule.OperationType = normalizePolicyWildcard(input.OperationType)
	rule.RiskLevel = normalizePolicyWildcard(input.RiskLevel)
	rule.ResourcePattern = normalizePolicyWildcard(input.ResourcePattern)
	rule.Reason = strings.TrimSpace(input.Reason)
	rule.UpdatedAt = time.Now().UTC()
	s.policyRules[ruleID] = rule
	return rule, nil
}

func (s *MemoryStore) DeletePolicyRule(_ context.Context, workspaceID, ruleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rule, ok := s.policyRules[ruleID]
	if !ok || rule.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	delete(s.policyRules, ruleID)
	ids := s.policyRulesByWorkspace[workspaceID]
	filtered := ids[:0]
	for _, id := range ids {
		if id != ruleID {
			filtered = append(filtered, id)
		}
	}
	s.policyRulesByWorkspace[workspaceID] = filtered
	return nil
}

func (s *MemoryStore) CreateApprovalRequest(_ context.Context, input model.CreateApprovalRequestInput) (model.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	fingerprint := strings.TrimSpace(input.Fingerprint)
	if fingerprint != "" {
		key := s.approvalFingerprintKey(input.WorkspaceID, fingerprint)
		if existingID, exists := s.approvalsByWorkspaceFingerprint[key]; exists {
			if existing, ok := s.approvals[existingID]; ok {
				if expireApprovalIfNeeded(&existing, now) {
					s.approvals[existingID] = existing
				}
				if approvalFingerprintIsActive(existing) {
					return model.ApprovalRequest{}, ErrConflict
				}
			}
			delete(s.approvalsByWorkspaceFingerprint, key)
		}
	}
	approval := model.ApprovalRequest{
		ID:                   ensureID("", "approval"),
		WorkspaceID:          input.WorkspaceID,
		ToolKey:              strings.TrimSpace(input.ToolKey),
		ToolDisplayName:      strings.TrimSpace(defaultString(input.ToolDisplayName, input.ToolKey)),
		Status:               "pending",
		RequestedBy:          strings.TrimSpace(defaultString(input.RequestedBy, "unknown")),
		Reason:               strings.TrimSpace(input.Reason),
		Fingerprint:          fingerprint,
		Adapter:              strings.TrimSpace(input.Adapter),
		ActionType:           strings.TrimSpace(input.ActionType),
		Target:               strings.TrimSpace(input.Target),
		CanonicalTarget:      strings.TrimSpace(input.CanonicalTarget),
		ContentEncoding:      strings.TrimSpace(input.ContentEncoding),
		ContentHash:          strings.TrimSpace(input.ContentHash),
		ScriptHash:           strings.TrimSpace(input.ScriptHash),
		ResolvedFileIdentity: strings.TrimSpace(input.ResolvedFileIdentity),
		ParentIdentity:       strings.TrimSpace(input.ParentIdentity),
		DecisionPayloadJSON:  cloneJSON(defaultJSON(input.DecisionPayloadJSON)),
		ExpiresAt:            approvalExpiresAt(now, input.TTL),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	s.approvals[approval.ID] = approval
	s.approvalsByWorkspace[approval.WorkspaceID] = append(s.approvalsByWorkspace[approval.WorkspaceID], approval.ID)
	if approval.Fingerprint != "" {
		s.approvalsByWorkspaceFingerprint[s.approvalFingerprintKey(approval.WorkspaceID, approval.Fingerprint)] = approval.ID
	}
	return approval, nil
}

func (s *MemoryStore) filterToolCallsLocked(workspaceID string, query model.ToolCallQuery) []model.ToolCall {
	ids := append([]string(nil), s.callsByWorkspace[workspaceID]...)
	items := make([]model.ToolCall, 0, len(ids))
	for _, id := range ids {
		call, ok := s.calls[id]
		if !ok || call.WorkspaceID != workspaceID {
			continue
		}
		if !toolCallMatchesFilter(call, query) {
			continue
		}
		items = append(items, s.withApprovalStatusLocked(call))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items
}

func (s *MemoryStore) UpdateApprovalRequest(_ context.Context, workspaceID, approvalID string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	approval, ok := s.approvals[approvalID]
	if !ok || approval.WorkspaceID != workspaceID {
		return model.ApprovalRequest{}, ErrNotFound
	}
	if expireApprovalIfNeeded(&approval, time.Now().UTC()) {
		s.approvals[approvalID] = approval
		return model.ApprovalRequest{}, ErrExpired
	}
	approval.Status = strings.TrimSpace(input.Status)
	approval.ReviewedBy = strings.TrimSpace(input.ReviewedBy)
	if strings.TrimSpace(input.Reason) != "" {
		approval.Reason = strings.TrimSpace(input.Reason)
	}
	approval.UpdatedAt = time.Now().UTC()
	s.approvals[approvalID] = approval
	return approval, nil
}

func (s *MemoryStore) TransitionApprovalRequest(_ context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	approval, ok := s.approvals[approvalID]
	if !ok || approval.WorkspaceID != workspaceID {
		return model.ApprovalRequest{}, ErrNotFound
	}
	if expireApprovalIfNeeded(&approval, time.Now().UTC()) {
		s.approvals[approvalID] = approval
		return model.ApprovalRequest{}, ErrExpired
	}
	if strings.EqualFold(strings.TrimSpace(approval.Status), "expired") {
		return model.ApprovalRequest{}, ErrExpired
	}
	if strings.ToLower(strings.TrimSpace(approval.Status)) != strings.ToLower(strings.TrimSpace(fromStatus)) {
		return model.ApprovalRequest{}, ErrConflict
	}
	approval.Status = strings.TrimSpace(input.Status)
	approval.ReviewedBy = strings.TrimSpace(input.ReviewedBy)
	if strings.TrimSpace(input.Reason) != "" {
		approval.Reason = strings.TrimSpace(input.Reason)
	}
	approval.UpdatedAt = time.Now().UTC()
	s.approvals[approvalID] = approval
	return approval, nil
}

func (s *MemoryStore) newWorkspaceLocked(input model.CreateWorkspaceInput) model.Workspace {
	now := time.Now().UTC()
	return model.Workspace{
		ID:                    uuid.NewString(),
		Name:                  strings.TrimSpace(input.Name),
		Slug:                  strings.ToLower(strings.TrimSpace(input.Slug)),
		ZitadelOrganizationID: strings.TrimSpace(input.ZitadelOrganizationID),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

func (s *MemoryStore) putWorkspaceLocked(workspace model.Workspace) {
	s.workspaces[workspace.ID] = workspace
	s.workspacesBySlug[strings.ToLower(workspace.Slug)] = workspace.ID
	s.workspacesByOrgID[workspace.ZitadelOrganizationID] = workspace.ID
}

func (s *MemoryStore) newToolLocked(input model.CreateToolInput) model.Tool {
	now := time.Now().UTC()
	namespace := strings.ToLower(strings.TrimSpace(input.Namespace))
	name := strings.ToLower(strings.TrimSpace(input.Name))
	return model.Tool{
		ID:               uuid.NewString(),
		WorkspaceID:      input.WorkspaceID,
		Namespace:        namespace,
		Name:             name,
		DisplayName:      strings.TrimSpace(defaultString(input.DisplayName, namespace+"."+name)),
		Description:      strings.TrimSpace(input.Description),
		OperationType:    strings.TrimSpace(defaultString(input.OperationType, "mock")),
		RiskLevel:        strings.TrimSpace(defaultString(input.RiskLevel, "low")),
		RequiresApproval: input.RequiresApproval,
		InputSchemaJSON:  cloneJSON(defaultJSON(input.InputSchemaJSON)),
		OutputSchemaJSON: cloneJSON(defaultJSON(input.OutputSchemaJSON)),
		Enabled:          input.Enabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func (s *MemoryStore) putToolLocked(tool model.Tool) {
	s.tools[tool.ID] = tool
	s.toolsByWorkspaceKey[s.toolIndexKey(tool.WorkspaceID, tool.Key())] = tool.ID
}

func (s *MemoryStore) newConnectorLocked(workspaceID string, input model.BootstrapConnectorInput) model.Connector {
	now := time.Now().UTC()
	typeName := strings.ToLower(strings.TrimSpace(input.Type))
	name := strings.ToLower(strings.TrimSpace(input.Name))
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = typeName + "." + name
	}
	return model.Connector{
		ID:          uuid.NewString(),
		WorkspaceID: workspaceID,
		Type:        typeName,
		Name:        name,
		DisplayName: displayName,
		ConfigJSON:  cloneJSON(defaultJSON(input.ConfigJSON)),
		Enabled:     input.Enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func (s *MemoryStore) putConnectorLocked(connector model.Connector) {
	s.connectors[connector.ID] = connector
	s.connectorsByWorkspace[connector.WorkspaceID] = append(s.connectorsByWorkspace[connector.WorkspaceID], connector.ID)
	s.connectorsByWorkspaceKey[s.connectorIndexKey(connector.WorkspaceID, connector.Type, connector.Name)] = connector.ID
}

func newPolicyRule(input model.CreatePolicyRuleInput) model.PolicyRule {
	now := time.Now().UTC()
	return model.PolicyRule{
		ID:              uuid.NewString(),
		WorkspaceID:     input.WorkspaceID,
		Name:            strings.TrimSpace(input.Name),
		Description:     strings.TrimSpace(input.Description),
		Enabled:         input.Enabled,
		Priority:        input.Priority,
		Effect:          strings.ToLower(strings.TrimSpace(input.Effect)),
		ConnectorType:   normalizePolicyWildcard(input.ConnectorType),
		ToolNamePattern: normalizePolicyWildcard(input.ToolNamePattern),
		OperationType:   normalizePolicyWildcard(input.OperationType),
		RiskLevel:       normalizePolicyWildcard(input.RiskLevel),
		ResourcePattern: normalizePolicyWildcard(input.ResourcePattern),
		Reason:          strings.TrimSpace(input.Reason),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func normalizePolicyWildcard(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return "*"
	}
	return trimmed
}

func sortPolicyRules(items []model.PolicyRule) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority == items[j].Priority {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].ID < items[j].ID
			}
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Priority < items[j].Priority
	})
}

func (s *MemoryStore) withApprovalStatusLocked(call model.ToolCall) model.ToolCall {
	call.ApprovalStatus = ""
	call.Explanation = cloneToolCallExplanation(call.Explanation)
	if strings.TrimSpace(call.ApprovalID) == "" {
		return call
	}
	if approval, ok := s.approvals[call.ApprovalID]; ok && approval.WorkspaceID == call.WorkspaceID {
		call.ApprovalStatus = approval.Status
	}
	return call
}

func (s *MemoryStore) connectorIndexKey(workspaceID, connectorType, name string) string {
	return workspaceID + "::" + strings.ToLower(strings.TrimSpace(connectorType)) + "." + strings.ToLower(strings.TrimSpace(name))
}

func (s *MemoryStore) toolIndexKey(workspaceID, toolKey string) string {
	return workspaceID + "::" + strings.ToLower(strings.TrimSpace(toolKey))
}

func (s *MemoryStore) userIndexKey(workspaceID, subject string) string {
	return workspaceID + "::" + strings.TrimSpace(subject)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *MemoryStore) approvalFingerprintKey(workspaceID, fingerprint string) string {
	return workspaceID + "::" + strings.TrimSpace(fingerprint)
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	out := make([]byte, len(value))
	copy(out, value)
	return json.RawMessage(out)
}

func normalizeToolCallPage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func toolCallMatchesFilter(call model.ToolCall, query model.ToolCallQuery) bool {
	if strings.TrimSpace(query.Tool) != "" && !strings.EqualFold(strings.TrimSpace(call.ToolKey), strings.TrimSpace(query.Tool)) {
		return false
	}
	if len(query.Statuses) > 0 {
		matched := false
		for _, status := range query.Statuses {
			if strings.EqualFold(strings.TrimSpace(call.Status), strings.TrimSpace(status)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if query.From != nil && call.CreatedAt.Before(query.From.UTC()) {
		return false
	}
	if query.To != nil && call.CreatedAt.After(query.To.UTC()) {
		return false
	}
	return true
}

func ensureID(value, prefix string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed
	}
	return prefix + "_" + uuid.NewString()
}

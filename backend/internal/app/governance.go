package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/policy"
	"agenttoolgate/backend/internal/store"
)

const (
	policyAllow           = "allow"
	policyDeny            = "deny"
	policyRequireApproval = "require_approval"

	localReviewerTokenEnv = "AGT_LOCAL_REVIEWER_TOKEN"
	localReviewerActorID  = "local-reviewer"
)

func (a *App) reloadPolicyEngine() {
	path := strings.TrimSpace(a.cfg.PolicyConfigPath)
	if path == "" {
		a.policies = policy.NewDefaultEngine()
		return
	}
	engine, err := policy.LoadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			a.logger.Warn("load policy file failed; using built-in defaults", "path", path, "error", err)
		}
		a.policies = policy.NewDefaultEngine()
		return
	}
	a.policies = engine
}

func (a *App) StartPolicyAutoReload(ctx context.Context) {
	if a.policies == nil {
		return
	}
	interval := time.Duration(a.cfg.PolicyReloadIntervalMs) * time.Millisecond
	a.policies.StartAutoReload(ctx, interval)
}

func (a *App) StartRateLimitEvicter(ctx context.Context) {
	evictInterval := time.Duration(a.cfg.RateLimitEvictIntervalSec) * time.Second
	idleTimeout := time.Duration(a.cfg.RateLimitIdleTimeoutSec) * time.Second
	if evictInterval <= 0 || idleTimeout <= 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(evictInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.reapExpiredWorkspaceRateLimiters(time.Now().UTC())
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (a *App) decidePolicy(tool model.Tool, userRole string) (string, string) {
	decision := a.decidePolicyDetailed(tool, userRole)
	return string(decision.Effect), decision.Reason
}

func (a *App) decidePolicyDetailed(tool model.Tool, userRole string) policy.Decision {
	return a.decidePolicyDetailedWithRisk(tool, userRole, tool.RiskLevel)
}

func (a *App) decidePolicyDetailedWithRisk(tool model.Tool, userRole, riskLevel string) policy.Decision {
	if a.policies == nil {
		a.policies = policy.NewDefaultEngine()
	}

	return a.policies.Evaluate(policy.Input{
		ToolNamespace:    tool.Namespace,
		ToolName:         tool.Name,
		OperationType:    tool.OperationType,
		UserRole:         userRole,
		RiskLevel:        normalizeRiskLevel(riskLevel),
		RequiresApproval: tool.RequiresApproval,
		ToolEnabled:      tool.Enabled,
		SupportedTool:    isExecutableTool(tool),
	})
}

func mapPolicyRuleResponse(rule policy.Rule) policyRuleResponse {
	response := policyRuleResponse{
		Name:     rule.Name,
		Priority: rule.Priority,
		Match: policyMatchResponse{
			ToolNamespace:    rule.Match.ToolNamespace,
			ToolName:         rule.Match.ToolName,
			OperationType:    rule.Match.OperationType,
			UserRole:         rule.Match.UserRole,
			RiskLevel:        rule.Match.RiskLevel,
			ActionType:       rule.Match.ActionType,
			TargetCategory:   rule.Match.TargetCategory,
			ContentSensitive: clonePolicyBool(rule.Match.ContentSensitive),
			RequiresApproval: clonePolicyBool(rule.Match.RequiresApproval),
			ToolEnabled:      clonePolicyBool(rule.Match.ToolEnabled),
			SupportedTool:    clonePolicyBool(rule.Match.SupportedTool),
		},
		Effect:  string(rule.Effect),
		Reason:  rule.Reason,
		Enabled: true,
	}
	if len(rule.Conditions.TimeWindow.DenyHours) > 0 {
		response.Conditions = policyConditionsResponse{
			DenyHours: append([]string(nil), rule.Conditions.TimeWindow.DenyHours...),
		}
	}
	return response
}

func clonePolicyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func isExecutableTool(tool model.Tool) bool {
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	if namespace == "mock" || (namespace == "database" && name == "query") {
		return true
	}
	if namespace == "github" {
		switch name {
		case "list_repos", "get_pull_request", "create_issue":
			return true
		}
	}
	if namespace == "http" && name == "request" {
		return true
	}
	if strings.HasPrefix(namespace, "mcp_") {
		return true
	}
	return false
}

func (a *App) executeTool(ctx context.Context, tool model.Tool, workspaceID string, decodedArgs any) (map[string]any, json.RawMessage, error) {
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	switch {
	case namespace == "mock":
		return executeMockTool(tool, workspaceID, decodedArgs)
	case namespace == "database" && name == "query":
		return a.executeDatabaseQuery(ctx, tool, workspaceID, decodedArgs)
	case namespace == "github":
		return a.executeGitHubTool(ctx, workspaceID, tool, decodedArgs)
	case namespace == "http" && name == "request":
		return a.executeHTTPRequest(ctx, workspaceID, decodedArgs)
	case strings.HasPrefix(namespace, "mcp_"):
		return a.executeMCPTool(ctx, tool, decodedArgs)
	default:
		return nil, nil, fmt.Errorf("tool %s is not supported in the current skeleton", tool.Key())
	}
}

func (a *App) deriveToolCallPolicy(tool model.Tool, decodedArgs any, currentDecision, currentReason string) (string, string) {
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	if namespace == "http" && name == "request" {
		return a.deriveHTTPPolicyDecision(decodedArgs, currentDecision, currentReason)
	}
	return currentDecision, currentReason
}

func (a *App) deriveToolCallRisk(tool model.Tool, decodedArgs any) string {
	risk := normalizeRiskLevel(tool.RiskLevel)
	if containsSensitiveJSONKey(decodedArgs) {
		risk = maxRiskLevel(risk, "high")
	}

	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))

	switch {
	case namespace == "database" && name == "query":
		if sql, ok := databaseQuerySQLFromArgs(decodedArgs); ok && bannedDatabaseQueryKeyword.MatchString(sql) {
			risk = maxRiskLevel(risk, "high")
		}
	case namespace == "http" && name == "request":
		args, err := parseHTTPRequestArgs(decodedArgs)
		if err != nil {
			return risk
		}
		if isHTTPWriteMethod(args.Method) {
			risk = maxRiskLevel(risk, "medium")
			if isExternalHTTPHost(args.URL.Hostname()) {
				risk = maxRiskLevel(risk, "high")
			}
		}
	}

	return risk
}

func executeMockTool(tool model.Tool, workspaceID string, decodedArgs any) (map[string]any, json.RawMessage, error) {
	if strings.ToLower(strings.TrimSpace(tool.Namespace)) != "mock" {
		return nil, nil, fmt.Errorf("tool %s is not supported in the current skeleton", tool.Key())
	}

	resultPayload := map[string]any{
		"echo":        decodedArgs,
		"tool":        tool.Key(),
		"workspaceId": workspaceID,
		"receivedAt":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	resultJSON, err := json.Marshal(resultPayload)
	if err != nil {
		return nil, nil, err
	}
	return resultPayload, resultJSON, nil
}

func approvalRequestedBy(identityText, subject string) string {
	if strings.TrimSpace(subject) != "" {
		return strings.TrimSpace(subject)
	}
	if strings.TrimSpace(identityText) != "" {
		return strings.TrimSpace(identityText)
	}
	return "unknown"
}

func approvalActorID(reqCtx RequestContext) string {
	for _, candidate := range []string{
		reqCtx.Identity.Subject,
		reqCtx.User.ZitadelUserID,
		reqCtx.User.ID,
		reqCtx.Identity.Email,
		reqCtx.User.Email,
	} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

func approvalSelfReviewForbidden(reqCtx RequestContext, requestedBy string) bool {
	requester := strings.TrimSpace(requestedBy)
	if requester == "" || strings.EqualFold(requester, "unknown") {
		return false
	}
	for _, candidate := range []string{
		reqCtx.Identity.Subject,
		reqCtx.User.ZitadelUserID,
		reqCtx.User.ID,
		reqCtx.Identity.Email,
		reqCtx.User.Email,
	} {
		if strings.EqualFold(requester, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (a *App) approvalReviewerForRequest(reqCtx RequestContext, requestedBy, reviewerToken string) (string, error) {
	if !approvalSelfReviewForbidden(reqCtx, requestedBy) {
		return approvalActorID(reqCtx), nil
	}
	if !strings.EqualFold(strings.TrimSpace(a.cfg.AuthMode), "local") {
		return "", errApprovalSelfReviewDenied
	}

	expected := strings.TrimSpace(os.Getenv(localReviewerTokenEnv))
	provided := strings.TrimSpace(reviewerToken)
	if len(expected) < 24 || len(provided) == 0 || subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return "", errApprovalSelfReviewDenied
	}
	return localReviewerActorID, nil
}

type approvalExecutionPlan struct {
	tool        model.Tool
	decodedArgs any
}

func freezeApprovalRequesterRole(payloadJSON json.RawMessage, requesterRole string) (json.RawMessage, error) {
	requesterRole, err := normalizeFrozenApprovalRequesterRole(requesterRole)
	if err != nil {
		return nil, err
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(defaultJSON(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("%w: invalid frozen approval payload", store.ErrConflict)
	}
	if payload == nil {
		payload = make(map[string]json.RawMessage)
	}
	encodedRole, err := json.Marshal(requesterRole)
	if err != nil {
		return nil, err
	}
	payload["requesterRole"] = encodedRole

	frozenPayloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return frozenPayloadJSON, nil
}

func frozenApprovalRequesterRole(approval model.ApprovalRequest) (string, error) {
	// requesterRole 是审批创建时冻结的策略输入，不代表查询了当前用户目录。
	var payload struct {
		RequesterRole string `json:"requesterRole"`
	}
	if err := json.Unmarshal(defaultJSON(approval.DecisionPayloadJSON), &payload); err != nil {
		return "", fmt.Errorf("%w: invalid frozen approval payload", store.ErrConflict)
	}
	return normalizeFrozenApprovalRequesterRole(payload.RequesterRole)
}

func normalizeFrozenApprovalRequesterRole(role string) (string, error) {
	normalized := normalizeRole(role)
	switch normalized {
	case roleOwner, roleAdmin, roleApprover, roleViewer, roleAgent, roleServiceAccount, "member":
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: approval requester role is missing or invalid", store.ErrConflict)
	}
}

func (a *App) revalidateApprovalExecution(ctx context.Context, workspaceID string, approval model.ApprovalRequest, call model.ToolCall) (approvalExecutionPlan, error) {
	requesterRole, err := frozenApprovalRequesterRole(approval)
	if err != nil {
		return approvalExecutionPlan{}, err
	}

	tool, err := a.store.GetToolByKey(ctx, workspaceID, call.ToolKey)
	if err != nil {
		return approvalExecutionPlan{}, err
	}

	if strings.EqualFold(strings.TrimSpace(call.ToolKey), agentGuardEvaluateToolKey) {
		if !tool.Enabled {
			return approvalExecutionPlan{}, fmt.Errorf("%w: tool is disabled", store.ErrConflict)
		}
		var payload struct {
			ActionType       string `json:"actionType"`
			TargetCategory   string `json:"targetCategory"`
			RiskLevel        string `json:"riskLevel"`
			ContentSensitive bool   `json:"contentSensitive"`
		}
		if err := json.Unmarshal(defaultJSON(call.InputExecutionJSON), &payload); err != nil {
			return approvalExecutionPlan{}, fmt.Errorf("%w: invalid frozen approval input", store.ErrConflict)
		}
		operationType := deriveAgentGuardOperationType(payload.ActionType, payload.RiskLevel)
		decision, _, _ := a.decideAgentGuardPolicy(requesterRole, tool.Enabled, operationType, payload.RiskLevel, payload.ActionType, payload.TargetCategory, payload.ContentSensitive)
		if decision == policyDeny {
			return approvalExecutionPlan{}, fmt.Errorf("%w: current policy denies approval", store.ErrConflict)
		}
		return approvalExecutionPlan{tool: tool}, nil
	}

	decodedArgs, err := decodeJSONValue(defaultJSON(call.InputExecutionJSON))
	if err != nil {
		return approvalExecutionPlan{}, fmt.Errorf("%w: invalid frozen approval input", store.ErrConflict)
	}
	effectiveRisk := a.deriveToolCallRisk(tool, decodedArgs)
	defaultDecision := a.decidePolicyDetailedWithRisk(tool, requesterRole, effectiveRisk)
	policyDecision := string(defaultDecision.Effect)
	policyReason := defaultDecision.Reason
	policyRuleName := defaultDecision.RuleName
	if policyDecision != policyDeny {
		if err := a.validateToolCallBeforePolicyResponse(ctx, workspaceID, tool, decodedArgs); err != nil {
			return approvalExecutionPlan{}, err
		}
		policyDecision, policyReason = a.deriveToolCallPolicy(tool, decodedArgs, policyDecision, policyReason)
		if policyReason != defaultDecision.Reason {
			policyRuleName = "derived-tool-policy"
		}
	}
	evaluation, err := a.evaluateManagedPolicyForTool(ctx, workspaceID, tool, decodedArgs, effectiveRisk, policyDecision, policyReason, policyRuleName)
	if err != nil {
		return approvalExecutionPlan{}, err
	}
	if evaluation.Decision == policyDeny {
		return approvalExecutionPlan{}, fmt.Errorf("%w: current policy denies approval", store.ErrConflict)
	}
	return approvalExecutionPlan{tool: tool, decodedArgs: decodedArgs}, nil
}

func normalizeRiskLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "critical":
		return "critical"
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func maxRiskLevel(current, candidate string) string {
	if riskLevelRank(candidate) > riskLevelRank(current) {
		return normalizeRiskLevel(candidate)
	}
	return normalizeRiskLevel(current)
}

func riskLevelRank(level string) int {
	switch normalizeRiskLevel(level) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return 1
	}
}

func containsSensitiveJSONKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if isSecretReferenceJSONKey(key) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(key), "headers") && containsSensitiveRiskHeader(item) {
				return true
			}
			if isSensitiveRiskJSONKey(key) {
				return true
			}
			if containsSensitiveJSONKey(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsSensitiveJSONKey(item) {
				return true
			}
		}
	}
	return false
}

func containsSensitiveRiskHeader(value any) bool {
	headers, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range headers {
		normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "_", "-")
		switch {
		case normalized == "authorization",
			normalized == "cookie",
			normalized == "proxy-authorization",
			strings.HasSuffix(normalized, "-token"),
			strings.HasSuffix(normalized, "-api-key"),
			strings.HasSuffix(normalized, "-secret"),
			strings.HasSuffix(normalized, "-signature"):
			return true
		}
	}
	return false
}

func isSensitiveRiskJSONKey(key string) bool {
	compact := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	if compact == "" {
		return false
	}
	switch compact {
	case "password", "passwd", "pwd",
		"secret", "clientsecret",
		"token", "accesstoken", "refreshtoken", "authtoken", "idtoken", "sessiontoken",
		"apikey", "accesskey", "privatekey",
		"authorization", "cookie", "session":
		return true
	}

	if hasSensitiveRiskKeySuffix(compact) {
		return true
	}
	return strings.HasSuffix(compact, "value") &&
		hasSensitiveRiskKeySuffix(strings.TrimSuffix(compact, "value"))
}

func hasSensitiveRiskKeySuffix(compact string) bool {
	for _, suffix := range []string{
		"password", "passwd", "pwd",
		"secret", "token",
		"apikey", "accesskey", "privatekey",
		"authorization", "cookie",
	} {
		if strings.HasSuffix(compact, suffix) {
			return true
		}
	}
	return false
}

func databaseQuerySQLFromArgs(decodedArgs any) (string, bool) {
	obj, ok := decodedArgs.(map[string]any)
	if !ok {
		return "", false
	}
	sql, ok := obj["sql"].(string)
	if !ok {
		return "", false
	}
	sql = strings.TrimSpace(sql)
	return sql, sql != ""
}

func isExternalHTTPHost(hostname string) bool {
	host := strings.ToLower(strings.Trim(strings.TrimSpace(hostname), "[]"))
	if host == "" || host == "localhost" {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
		return false
	}
	return true
}

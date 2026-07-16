package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/policy"

	"github.com/go-chi/chi/v5"
)

type createPolicyRuleRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	Enabled         *bool  `json:"enabled"`
	Priority        int    `json:"priority"`
	Effect          string `json:"effect"`
	ConnectorType   string `json:"connectorType"`
	ToolNamePattern string `json:"toolNamePattern"`
	OperationType   string `json:"operationType"`
	RiskLevel       string `json:"riskLevel"`
	ResourcePattern string `json:"resourcePattern"`
	Reason          string `json:"reason"`
}

type updatePolicyRuleRequest = createPolicyRuleRequest

type policySimulationRequest struct {
	WorkspaceOrgID string          `json:"workspaceOrgId,omitempty"`
	ConnectorType  string          `json:"connectorType"`
	ToolName       string          `json:"toolName"`
	OperationType  string          `json:"operationType"`
	RiskLevel      string          `json:"riskLevel"`
	Resource       string          `json:"resource"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
}

type policySimulationResponse struct {
	Decision        string                  `json:"decision"`
	MatchedRuleID   string                  `json:"matchedRuleId,omitempty"`
	MatchedRuleName string                  `json:"matchedRuleName,omitempty"`
	Explanation     string                  `json:"explanation"`
	Defaulted       bool                    `json:"defaulted"`
	EvaluationTrace []policyEvaluationTrace `json:"evaluationTrace"`
}

type policyEvaluationTrace struct {
	RuleID   string `json:"ruleId,omitempty"`
	RuleName string `json:"ruleName,omitempty"`
	Matched  bool   `json:"matched"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason"`
}

type managedPolicyEvaluation struct {
	Decision        string
	Reason          string
	MatchedRuleID   string
	MatchedRuleName string
	Defaulted       bool
	Trace           []policyEvaluationTrace
	ConnectorType   string
	Resource        string
}

type managedPolicyInput struct {
	WorkspaceID      string
	ConnectorType    string
	ToolName         string
	OperationType    string
	RiskLevel        string
	Resource         string
	FallbackDecision string
	FallbackReason   string
	FallbackRuleName string
}

func (a *App) handleCreatePolicyRule(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManagePolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req createPolicyRuleRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	input, err := normalizeCreatePolicyRuleRequest(reqCtx.Workspace.ID, req)
	if err != nil {
		a.respondError(w, err)
		return
	}
	rule, err := a.store.CreatePolicyRule(r.Context(), input)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (a *App) handleGetPolicyRule(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireViewPolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}
	rule, err := a.store.GetPolicyRuleByID(r.Context(), reqCtx.Workspace.ID, chi.URLParam(r, "id"))
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (a *App) handleUpdatePolicyRule(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManagePolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req updatePolicyRuleRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	input, err := normalizeUpdatePolicyRuleRequest(req)
	if err != nil {
		a.respondError(w, err)
		return
	}
	rule, err := a.store.UpdatePolicyRule(r.Context(), reqCtx.Workspace.ID, chi.URLParam(r, "id"), input)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func (a *App) handleDeletePolicyRule(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireManagePolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}
	if err := a.store.DeletePolicyRule(r.Context(), reqCtx.Workspace.ID, chi.URLParam(r, "id")); err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *App) handleSimulatePolicy(w http.ResponseWriter, r *http.Request) {
	reqCtx, ok := requestContextFrom(r.Context())
	if !ok {
		a.respondError(w, fmt.Errorf("missing request context"))
		return
	}
	if err := requireViewPolicies(reqCtx); err != nil {
		a.respondError(w, err)
		return
	}

	var req policySimulationRequest
	if err := readJSON(r, &req); err != nil {
		a.respondError(w, err)
		return
	}
	response, err := a.simulatePolicy(r.Context(), reqCtx.Workspace.ID, reqCtx.User.Role, req)
	if err != nil {
		a.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func normalizeCreatePolicyRuleRequest(workspaceID string, req createPolicyRuleRequest) (model.CreatePolicyRuleInput, error) {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := validatePolicyRuleFields(req.Name, req.Effect); err != nil {
		return model.CreatePolicyRuleInput{}, err
	}
	return model.CreatePolicyRuleInput{
		WorkspaceID:     workspaceID,
		Name:            strings.TrimSpace(req.Name),
		Description:     strings.TrimSpace(req.Description),
		Enabled:         enabled,
		Priority:        req.Priority,
		Effect:          normalizePolicyEffect(req.Effect),
		ConnectorType:   normalizePolicyField(req.ConnectorType),
		ToolNamePattern: normalizePolicyField(req.ToolNamePattern),
		OperationType:   normalizePolicyField(req.OperationType),
		RiskLevel:       normalizePolicyField(req.RiskLevel),
		ResourcePattern: normalizePolicyField(req.ResourcePattern),
		Reason:          strings.TrimSpace(req.Reason),
	}, nil
}

func normalizeUpdatePolicyRuleRequest(req updatePolicyRuleRequest) (model.UpdatePolicyRuleInput, error) {
	if err := validatePolicyRuleFields(req.Name, req.Effect); err != nil {
		return model.UpdatePolicyRuleInput{}, err
	}
	priority := req.Priority
	return model.UpdatePolicyRuleInput{
		Name:            strings.TrimSpace(req.Name),
		Description:     strings.TrimSpace(req.Description),
		Enabled:         req.Enabled,
		Priority:        &priority,
		Effect:          normalizePolicyEffect(req.Effect),
		ConnectorType:   normalizePolicyField(req.ConnectorType),
		ToolNamePattern: normalizePolicyField(req.ToolNamePattern),
		OperationType:   normalizePolicyField(req.OperationType),
		RiskLevel:       normalizePolicyField(req.RiskLevel),
		ResourcePattern: normalizePolicyField(req.ResourcePattern),
		Reason:          strings.TrimSpace(req.Reason),
	}, nil
}

func validatePolicyRuleFields(name, effect string) error {
	if strings.TrimSpace(name) == "" {
		return badRequest("Policy 名称必填")
	}
	switch normalizePolicyEffect(effect) {
	case policyAllow, policyDeny, policyRequireApproval:
		return nil
	default:
		return badRequest("Policy effect 必须是 allow、deny 或 require_approval")
	}
}

func normalizePolicyEffect(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizePolicyField(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return "*"
	}
	return trimmed
}

func (a *App) evaluateManagedPolicyForTool(ctx context.Context, workspaceID string, tool model.Tool, decodedArgs any, effectiveRisk, fallbackDecision, fallbackReason, fallbackRuleName string) (managedPolicyEvaluation, error) {
	resource := extractPolicyResource(tool, decodedArgs)
	input := managedPolicyInput{
		WorkspaceID:      workspaceID,
		ConnectorType:    policyConnectorTypeForTool(tool),
		ToolName:         tool.Key(),
		OperationType:    strings.ToLower(strings.TrimSpace(tool.OperationType)),
		RiskLevel:        strings.ToLower(strings.TrimSpace(effectiveRisk)),
		Resource:         resource,
		FallbackDecision: strings.ToLower(strings.TrimSpace(fallbackDecision)),
		FallbackReason:   strings.TrimSpace(fallbackReason),
		FallbackRuleName: strings.TrimSpace(fallbackRuleName),
	}
	return a.evaluateManagedPolicy(ctx, input)
}

func (a *App) simulatePolicy(ctx context.Context, workspaceID string, userRole string, req policySimulationRequest) (policySimulationResponse, error) {
	toolKey := strings.ToLower(strings.TrimSpace(req.ToolName))
	if toolKey == "" {
		return policySimulationResponse{}, badRequest("toolName 必填")
	}
	namespace, name, ok := strings.Cut(toolKey, ".")
	if !ok || namespace == "" || name == "" {
		return policySimulationResponse{}, badRequest("toolName 必须是 namespace.name 格式")
	}
	connectorType := strings.ToLower(strings.TrimSpace(req.ConnectorType))
	if connectorType == "" || connectorType == "*" {
		connectorType = policyConnectorTypeForNamespace(namespace)
	}
	operationType := normalizePolicyField(req.OperationType)
	riskLevel := normalizeRiskLevel(req.RiskLevel)

	if a.policies == nil {
		a.policies = policy.NewDefaultEngine()
	}
	fallback := a.policies.Evaluate(policy.Input{
		ToolNamespace:    namespace,
		ToolName:         name,
		OperationType:    operationType,
		UserRole:         strings.ToLower(strings.TrimSpace(userRole)),
		RiskLevel:        riskLevel,
		RequiresApproval: false,
		ToolEnabled:      true,
		SupportedTool:    isSimulatorSupportedTool(namespace, name),
		Now:              time.Now().UTC(),
	})

	evaluation, err := a.evaluateManagedPolicy(ctx, managedPolicyInput{
		WorkspaceID:      workspaceID,
		ConnectorType:    connectorType,
		ToolName:         toolKey,
		OperationType:    operationType,
		RiskLevel:        riskLevel,
		Resource:         strings.TrimSpace(req.Resource),
		FallbackDecision: string(fallback.Effect),
		FallbackReason:   fallback.Reason,
		FallbackRuleName: fallback.RuleName,
	})
	if err != nil {
		return policySimulationResponse{}, err
	}
	return policySimulationResponse{
		Decision:        evaluation.Decision,
		MatchedRuleID:   evaluation.MatchedRuleID,
		MatchedRuleName: evaluation.MatchedRuleName,
		Explanation:     evaluation.Reason,
		Defaulted:       evaluation.Defaulted,
		EvaluationTrace: evaluation.Trace,
	}, nil
}

func (a *App) evaluateManagedPolicy(ctx context.Context, input managedPolicyInput) (managedPolicyEvaluation, error) {
	fallbackDecision := normalizePolicyEffect(input.FallbackDecision)
	if fallbackDecision == "" {
		fallbackDecision = policyDeny
	}
	fallbackReason := strings.TrimSpace(input.FallbackReason)
	if fallbackReason == "" {
		fallbackReason = "默认策略决策"
	}

	rules, err := a.store.ListPolicyRules(ctx, input.WorkspaceID)
	if err != nil {
		return managedPolicyEvaluation{}, err
	}
	trace := []policyEvaluationTrace{
		{
			RuleName: strings.TrimSpace(input.FallbackRuleName),
			Matched:  true,
			Decision: fallbackDecision,
			Reason:   "默认兜底策略：" + safePolicyExplanation(fallbackReason),
		},
	}

	for _, rule := range rules {
		if !rule.Enabled {
			trace = append(trace, policyEvaluationTrace{
				RuleID:   rule.ID,
				RuleName: rule.Name,
				Matched:  false,
				Reason:   "规则已禁用",
			})
			continue
		}
		if reason := policyRuleMismatchReason(rule, input); reason != "" {
			trace = append(trace, policyEvaluationTrace{
				RuleID:   rule.ID,
				RuleName: rule.Name,
				Matched:  false,
				Reason:   reason,
			})
			continue
		}

		trace = append(trace, policyEvaluationTrace{
			RuleID:   rule.ID,
			RuleName: rule.Name,
			Matched:  true,
			Decision: rule.Effect,
			Reason:   "命中用户规则",
		})
		finalDecision := stricterPolicyDecision(fallbackDecision, rule.Effect)
		explanation := managedPolicyExplanation(rule, fallbackDecision, fallbackReason, finalDecision)
		return managedPolicyEvaluation{
			Decision:        finalDecision,
			Reason:          explanation,
			MatchedRuleID:   rule.ID,
			MatchedRuleName: rule.Name,
			Defaulted:       false,
			Trace:           trace,
			ConnectorType:   input.ConnectorType,
			Resource:        input.Resource,
		}, nil
	}

	return managedPolicyEvaluation{
		Decision:      fallbackDecision,
		Reason:        safePolicyExplanation(fallbackReason),
		Defaulted:     true,
		Trace:         trace,
		ConnectorType: input.ConnectorType,
		Resource:      input.Resource,
	}, nil
}

func policyRuleMismatchReason(rule model.PolicyRule, input managedPolicyInput) string {
	if !globMatch(rule.ConnectorType, input.ConnectorType) {
		return "connectorType 不匹配"
	}
	if !globMatch(rule.ToolNamePattern, input.ToolName) {
		return "toolNamePattern 不匹配"
	}
	if !globMatch(rule.OperationType, input.OperationType) {
		return "operationType 不匹配"
	}
	if !globMatch(rule.RiskLevel, input.RiskLevel) {
		return "riskLevel 不匹配"
	}
	if !globMatch(rule.ResourcePattern, input.Resource) {
		return "resourcePattern 不匹配"
	}
	return ""
}

func managedPolicyExplanation(rule model.PolicyRule, fallbackDecision, fallbackReason, finalDecision string) string {
	ruleReason := safePolicyExplanation(rule.Reason)
	if ruleReason == "" {
		ruleReason = "用户策略规则命中"
	}
	if finalDecision == normalizePolicyEffect(rule.Effect) {
		return ruleReason
	}
	return fmt.Sprintf("用户规则 %s 命中，但默认安全兜底 %s 更严格：%s", rule.Name, fallbackDecision, safePolicyExplanation(fallbackReason))
}

func stricterPolicyDecision(left, right string) string {
	if policyDecisionRank(right) > policyDecisionRank(left) {
		return normalizePolicyEffect(right)
	}
	return normalizePolicyEffect(left)
}

func policyDecisionRank(decision string) int {
	switch normalizePolicyEffect(decision) {
	case policyDeny:
		return 3
	case policyRequireApproval:
		return 2
	case policyAllow:
		return 1
	default:
		return 3
	}
}

func policyConnectorTypeForTool(tool model.Tool) string {
	return policyConnectorTypeForNamespace(tool.Namespace)
}

func policyConnectorTypeForNamespace(namespace string) string {
	normalized := strings.ToLower(strings.TrimSpace(namespace))
	if strings.HasPrefix(normalized, "mcp_") {
		return "mcp"
	}
	return normalized
}

func isSimulatorSupportedTool(namespace, name string) bool {
	return isExecutableTool(model.Tool{
		Namespace: strings.ToLower(strings.TrimSpace(namespace)),
		Name:      strings.ToLower(strings.TrimSpace(name)),
		Enabled:   true,
	})
}

func extractPolicyResource(tool model.Tool, decodedArgs any) string {
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	args, _ := decodedArgs.(map[string]any)
	switch {
	case namespace == "github":
		owner, _ := args["owner"].(string)
		repo, _ := args["repo"].(string)
		if strings.TrimSpace(owner) != "" && strings.TrimSpace(repo) != "" {
			return strings.ToLower(strings.TrimSpace(owner)) + "/" + strings.ToLower(strings.TrimSpace(repo))
		}
	case namespace == "http" && name == "request":
		rawURL, _ := args["url"].(string)
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err == nil {
			return strings.ToLower(strings.TrimSpace(parsed.Host))
		}
	case namespace == "database" && name == "query":
		datasource, _ := args["datasource"].(string)
		return strings.ToLower(strings.TrimSpace(datasource))
	case strings.HasPrefix(namespace, "mcp_"):
		return strings.TrimPrefix(namespace, "mcp_")
	}
	return ""
}

func globMatch(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" || pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}
	parts := strings.Split(pattern, "*")
	position := 0
	for index, part := range parts {
		if part == "" {
			continue
		}
		found := strings.Index(value[position:], part)
		if found < 0 {
			return false
		}
		if index == 0 && !strings.HasPrefix(pattern, "*") && found != 0 {
			return false
		}
		position += found + len(part)
	}
	last := parts[len(parts)-1]
	if last != "" && !strings.HasSuffix(pattern, "*") && !strings.HasSuffix(value, last) {
		return false
	}
	return true
}

func safePolicyExplanation(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range trimmed {
		if unicode.IsControl(r) && r != '\t' {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(r)
		if builder.Len() >= 240 {
			builder.WriteString("...")
			break
		}
	}
	return builder.String()
}

func policyExplanationForAudit(tool model.Tool, riskLevel string, evaluation managedPolicyEvaluation) *model.ToolCallExplanation {
	signals := []string{
		"policyDecision:" + strings.TrimSpace(evaluation.Decision),
		"defaulted:" + fmt.Sprintf("%t", evaluation.Defaulted),
	}
	if strings.TrimSpace(evaluation.Reason) != "" {
		signals = append(signals, "policyExplanation:"+safePolicyExplanation(evaluation.Reason))
	}
	if strings.TrimSpace(evaluation.MatchedRuleID) != "" {
		signals = append(signals, "matchedPolicyRuleId:"+strings.TrimSpace(evaluation.MatchedRuleID))
	}
	if strings.TrimSpace(evaluation.MatchedRuleName) != "" {
		signals = append(signals, "matchedPolicyRuleName:"+safePolicyExplanation(evaluation.MatchedRuleName))
	}
	if strings.TrimSpace(evaluation.Resource) != "" {
		signals = append(signals, "resource:"+safePolicyExplanation(evaluation.Resource))
	}
	namespace := strings.ToLower(strings.TrimSpace(tool.Namespace))
	if strings.HasPrefix(namespace, "mcp_") {
		connectorName := strings.TrimPrefix(namespace, "mcp_")
		signals = append(signals, "connector:"+connectorName, "remoteTool:"+strings.TrimSpace(tool.Name))
	}
	if strings.EqualFold(strings.TrimSpace(evaluation.Decision), policyRequireApproval) || tool.RequiresApproval {
		signals = append(signals, "approval:required")
	} else {
		signals = append(signals, "approval:not_required")
	}

	matchedRule := strings.TrimSpace(evaluation.MatchedRuleName)
	if matchedRule == "" {
		matchedRule = "default_policy"
	}
	targetCategory := strings.TrimSpace(evaluation.ConnectorType)
	if strings.HasPrefix(namespace, "mcp_") {
		targetCategory = "mcp_connector"
		if evaluation.MatchedRuleName == "" {
			matchedRule = "mcp_outbound_governance"
		}
	}
	return &model.ToolCallExplanation{
		TargetCategory: targetCategory,
		RiskLevel:      strings.TrimSpace(riskLevel),
		MatchedRule:    matchedRule,
		Signals:        signals,
	}
}

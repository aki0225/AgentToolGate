package app

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
)

const (
	approvalExecutionFailedMessage    = "connector execution failed"
	approvalRevalidationFailedMessage = "approval revalidation failed"
)

var (
	approvalURLPattern    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"']+`)
	approvalBearerPattern = regexp.MustCompile(
		`(?i)(\bbearer\s+)[^\s"'<>]+`,
	)
	approvalSensitiveAssignmentPattern = regexp.MustCompile(
		`(?i)(\b(?:access[-_ ]?token|refresh[-_ ]?token|api[-_ ]?key|token|secret|password|passwd|authorization|signature|credential|cookie|session)\b\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&]+)`,
	)
	approvalSensitiveFlagPattern = regexp.MustCompile(
		`(?i)(--(?:access[-_]?token|refresh[-_]?token|api[-_]?key|token|secret|password|passwd|authorization|signature|credential|cookie|session)\s+)(?:"[^"]*"|'[^']*'|[^\s,;&]+)`,
	)
	approvalQuotedSensitiveAssignmentPattern = regexp.MustCompile(
		`(?i)(["'](?:access[-_ ]?token|refresh[-_ ]?token|api[-_ ]?key|token|secret|password|passwd|authorization|signature|credential|cookie|session)["']\s*:\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}\]]+)`,
	)
)

type approvalResponse struct {
	ID                  string          `json:"id"`
	WorkspaceID         string          `json:"workspaceId"`
	ToolKey             string          `json:"toolKey"`
	ToolDisplayName     string          `json:"toolDisplayName"`
	Status              string          `json:"status"`
	RequestedBy         string          `json:"requestedBy"`
	ReviewedBy          string          `json:"reviewedBy,omitempty"`
	Reason              string          `json:"reason,omitempty"`
	Fingerprint         string          `json:"fingerprint,omitempty"`
	Adapter             string          `json:"adapter,omitempty"`
	ActionType          string          `json:"actionType,omitempty"`
	Target              string          `json:"target,omitempty"`
	CanonicalTarget     string          `json:"canonicalTarget,omitempty"`
	ContentEncoding     string          `json:"contentEncoding,omitempty"`
	ContentHash         string          `json:"contentHash,omitempty"`
	ScriptHash          string          `json:"scriptHash,omitempty"`
	DecisionPayloadJSON json.RawMessage `json:"decisionPayloadJson,omitempty"`
	ExpiresAt           time.Time       `json:"expiresAt"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type approvalActionResponse struct {
	Approval approvalResponse `json:"approval"`
	ToolCall model.ToolCall   `json:"toolCall"`
	Result   any              `json:"result,omitempty"`
}

func newApprovalResponses(approvals []model.ApprovalRequest) []approvalResponse {
	responses := make([]approvalResponse, 0, len(approvals))
	for _, approval := range approvals {
		responses = append(responses, newApprovalResponse(approval))
	}
	return responses
}

func newApprovalResponse(approval model.ApprovalRequest) approvalResponse {
	return approvalResponse{
		ID:                  approval.ID,
		WorkspaceID:         approval.WorkspaceID,
		ToolKey:             approval.ToolKey,
		ToolDisplayName:     approval.ToolDisplayName,
		Status:              approval.Status,
		RequestedBy:         approval.RequestedBy,
		ReviewedBy:          approval.ReviewedBy,
		Reason:              redactApprovalText(approval.Reason),
		Fingerprint:         approval.Fingerprint,
		Adapter:             approval.Adapter,
		ActionType:          approval.ActionType,
		Target:              redactApprovalTarget(approval.Target),
		CanonicalTarget:     redactApprovalTarget(approval.CanonicalTarget),
		ContentEncoding:     approval.ContentEncoding,
		ContentHash:         approval.ContentHash,
		ScriptHash:          approval.ScriptHash,
		DecisionPayloadJSON: approvalDecisionSummary(approval.DecisionPayloadJSON),
		ExpiresAt:           approval.ExpiresAt,
		CreatedAt:           approval.CreatedAt,
		UpdatedAt:           approval.UpdatedAt,
	}
}

func newApprovalActionResponse(approval model.ApprovalRequest, call model.ToolCall) approvalActionResponse {
	safeCall := call
	safeCall.InputRedactedJSON = redactApprovalJSON(safeCall.InputRedactedJSON)
	safeCall.OutputRedactedJSON = redactApprovalJSON(safeCall.OutputRedactedJSON)
	safeCall.Explanation = redactApprovalExplanation(safeCall.Explanation)
	if strings.EqualFold(strings.TrimSpace(safeCall.ToolKey), agentGuardEvaluateToolKey) {
		safeCall.InputRedactedJSON = json.RawMessage(`{}`)
	}
	var result any
	if strings.EqualFold(strings.TrimSpace(safeCall.Status), "failed") {
		if safeCall.ErrorMessage == approvalRevalidationFailedMessage {
			safeCall.ErrorMessage = approvalRevalidationFailedMessage
		} else {
			safeCall.ErrorMessage = approvalExecutionFailedMessage
		}
	} else if strings.EqualFold(strings.TrimSpace(safeCall.Status), "success") {
		_ = json.Unmarshal(defaultJSON(safeCall.OutputRedactedJSON), &result)
	}
	return approvalActionResponse{
		Approval: newApprovalResponse(approval),
		ToolCall: safeCall,
		Result:   result,
	}
}

func redactApprovalExplanation(explanation *model.ToolCallExplanation) *model.ToolCallExplanation {
	if explanation == nil {
		return nil
	}
	signals := make([]string, len(explanation.Signals))
	for index, signal := range explanation.Signals {
		signals[index] = redactApprovalText(signal)
	}
	return &model.ToolCallExplanation{
		TargetCategory: redactApprovalText(explanation.TargetCategory),
		RiskLevel:      redactApprovalText(explanation.RiskLevel),
		MatchedRule:    redactApprovalText(explanation.MatchedRule),
		Signals:        signals,
	}
}

func approvalDecisionSummary(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal(value, &payload); err != nil {
		return nil
	}

	summary := make(map[string]any)
	for _, key := range []string{
		"adapter",
		"tool",
		"actionType",
		"isScript",
		"contentEncoding",
		"contentHash",
		"scriptHash",
		"targetCategory",
		"contentSensitive",
		"riskLevel",
		"fingerprint",
	} {
		if safeValue, ok := approvalSummaryValue(payload[key]); ok {
			summary[key] = safeValue
		}
	}
	if len(summary) == 0 {
		return nil
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return nil
	}
	return encoded
}

func approvalSummaryValue(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
		return redactApprovalText(typed), true
	case bool, float64:
		return typed, true
	default:
		return nil, false
	}
}

func redactApprovalJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}

	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return json.RawMessage(`{}`)
	}
	raw, err := json.Marshal(redactApprovalJSONValue(decoded))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func redactApprovalJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveJSONKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = redactApprovalJSONValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, item := range typed {
			redacted[index] = redactApprovalJSONValue(item)
		}
		return redacted
	case string:
		return redactApprovalText(typed)
	default:
		return value
	}
}

func redactApprovalTarget(value string) string {
	target := strings.TrimSpace(value)
	if target == "" {
		return ""
	}
	if redacted, ok := redactApprovalURL(target); ok {
		return redacted
	}
	return redactApprovalText(target)
}

func redactApprovalText(value string) string {
	redacted := approvalURLPattern.ReplaceAllStringFunc(value, func(match string) string {
		if safeURL, ok := redactApprovalURL(match); ok {
			return safeURL
		}
		return match
	})
	redacted = approvalBearerPattern.ReplaceAllString(redacted, "${1}[REDACTED]")
	redacted = approvalQuotedSensitiveAssignmentPattern.ReplaceAllString(redacted, `${1}"[REDACTED]"`)
	redacted = approvalSensitiveFlagPattern.ReplaceAllString(redacted, "${1}[REDACTED]")
	return approvalSensitiveAssignmentPattern.ReplaceAllString(redacted, "${1}[REDACTED]")
}

func redactApprovalURL(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}

	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query := parsed.Query()
	for key := range query {
		if isSensitiveApprovalQueryKey(key) {
			query.Set(key, "[REDACTED]")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), true
}

func isSensitiveApprovalQueryKey(key string) bool {
	if isSensitiveJSONKey(key) {
		return true
	}

	compact := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch compact {
	case "auth", "code", "credential", "jwt", "key", "sig":
		return true
	}
	for _, token := range []string{"signature", "credential", "clientsecret", "refreshtoken", "authtoken"} {
		if strings.Contains(compact, token) {
			return true
		}
	}
	return false
}

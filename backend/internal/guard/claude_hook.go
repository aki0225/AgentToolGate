package guard

import (
	"errors"
	"strings"
)

const ClaudeHookModeEnforce = "enforce"

type ClaudeHookOutput struct {
	HookSpecificOutput ClaudeHookSpecificOutput `json:"hookSpecificOutput"`
}

type ClaudeHookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func EvaluateClaudeHookPayload(payload []byte) (ClaudeHookOutput, error) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return ClaudeHookOutput{}, errors.New("hook payload 不能为空")
	}
	action, err := AdaptClaudePayload(payload)
	if err != nil {
		return ClaudeHookOutput{}, err
	}
	decision := Evaluate(action)
	return ClaudeHookOutputFromDecision(decision), nil
}

func ClaudeHookOutputFromDecision(decision Decision) ClaudeHookOutput {
	permissionDecision := normalizeClaudePermissionDecision(decision.Decision)
	out := ClaudeHookOutput{
		HookSpecificOutput: ClaudeHookSpecificOutput{
			HookEventName:      "PreToolUse",
			PermissionDecision: permissionDecision,
		},
	}
	if permissionDecision != "allow" {
		out.HookSpecificOutput.PermissionDecisionReason = claudeHookReason(permissionDecision, decision)
	}
	return out
}

func normalizeClaudePermissionDecision(decision string) string {
	switch lowerTrim(decision) {
	case "allow":
		return "allow"
	case "deny":
		return "deny"
	case "ask":
		return "ask"
	default:
		return "ask"
	}
}

func claudeHookReason(permissionDecision string, decision Decision) string {
	shortReason := strings.TrimSpace(decision.Reason)
	if shortReason == "" {
		shortReason = "需要人工确认"
	}
	switch permissionDecision {
	case "deny":
		return "AgentToolGate 已阻止：" + shortReason
	case "ask":
		return "AgentToolGate 需要确认：" + shortReason
	default:
		return shortReason
	}
}

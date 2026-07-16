package guard

import (
	"errors"
	"strings"
)

const CodexHookModeEnforce = "enforce"

type CodexHookOutput struct {
	HookSpecificOutput *CodexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type CodexHookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func EvaluateCodexHookPayload(payload []byte) (CodexHookOutput, bool, error) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return CodexHookOutput{}, false, errors.New("hook payload 不能为空")
	}
	action, err := AdaptCodexPayload(payload)
	if err != nil {
		return CodexHookOutput{}, false, err
	}
	decision := Evaluate(action)
	out, emit := CodexHookOutputFromDecision(decision)
	return out, emit, nil
}

func CodexHookOutputFromDecision(decision Decision) (CodexHookOutput, bool) {
	permissionDecision := normalizeCodexPermissionDecision(decision.Decision)
	if permissionDecision == "allow" {
		return CodexHookOutput{}, false
	}
	out := CodexHookOutput{
		HookSpecificOutput: &CodexHookSpecificOutput{
			HookEventName:      "PreToolUse",
			PermissionDecision: permissionDecision,
		},
	}
	out.HookSpecificOutput.PermissionDecisionReason = codexHookReason(decision)
	return out, true
}

func normalizeCodexPermissionDecision(decision string) string {
	switch lowerTrim(decision) {
	case "allow":
		return "allow"
	case "deny", "ask":
		return "deny"
	default:
		return "deny"
	}
}

func codexHookReason(decision Decision) string {
	switch lowerTrim(decision.Decision) {
	case "ask":
		return "AgentToolGate 需要人工确认，当前 Codex Hook MVP 已保守阻断"
	case "deny":
		shortReason := strings.TrimSpace(decision.Reason)
		if shortReason == "" {
			shortReason = "命中高风险动作"
		}
		return "AgentToolGate 已阻止：" + shortReason
	default:
		return "AgentToolGate 已阻止：无法识别动作，当前 Codex Hook MVP 已保守阻断"
	}
}

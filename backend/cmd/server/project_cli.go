package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agenttoolgate/backend/internal/config"
)

const (
	projectInitModeAll    = "all"
	projectInitModeCodex  = "codex"
	projectInitModeClaude = "claude"
	projectHookModeOff    = "off"
	projectHookModeDryRun = "dry-run"
	projectHookModeLive   = "live"
)

type projectRunConfig struct {
	ProjectRoot string `json:"projectRoot,omitempty"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Workspace   struct {
		Name  string `json:"name"`
		Slug  string `json:"slug"`
		OrgID string `json:"orgId"`
	} `json:"workspace"`
	HookMode    string `json:"hookMode"`
	OpenBrowser bool   `json:"openBrowser"`
}

type projectInitReport struct {
	Root        string
	ConfigPath  string
	Protected   string
	ReadmePath  string
	PromptPath  string
	CodexFiles  []string
	ClaudeFiles []string
	Created     []string
	Skipped     []string
}

func runInitCommand(opts commandOptions, stdout, stderr io.Writer) int {
	if strings.TrimSpace(opts.Addr) != "" || strings.TrimSpace(opts.Port) != "" || opts.OpenBrowser {
		fmt.Fprintln(stderr, "init 仅支持 --dir 和 init codex|claude|all")
		return 2
	}
	root, err := resolveProjectRoot(opts.Dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	report, err := writeProjectInitFiles(root, strings.ToLower(strings.TrimSpace(opts.InitTarget)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "AgentToolGate init 完成")
	fmt.Fprintln(stdout, "项目目录: "+report.Root)
	fmt.Fprintln(stdout, "项目配置: "+report.ConfigPath)
	fmt.Fprintln(stdout, "保护策略: "+report.Protected)
	fmt.Fprintln(stdout, "项目说明: "+report.ReadmePath)
	fmt.Fprintln(stdout, "AI 提示: "+report.PromptPath)
	fmt.Fprintln(stdout, "默认 hook mode: dry-run")
	cmdName := currentAgentToolGateCommandName()
	fmt.Fprintf(stdout, "下一步: 运行 %s up --open，然后把 .agenttoolgate/clients/ 里的片段复制到 Codex / Claude / ccswitch。\n", cmdName)
	printInitPathList(stdout, "Codex 片段", report.CodexFiles)
	printInitPathList(stdout, "Claude 片段", report.ClaudeFiles)
	printInitPathList(stdout, "已生成", report.Created)
	printInitPathList(stdout, "已跳过", report.Skipped)
	return 0
}

func runUpCommand(opts commandOptions, stdout, stderr io.Writer) int {
	cfg, openBrowser, summary, hookControlPath, hookControlMode, err := prepareProjectUp(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stdout, summary)
	return startServer(cfg, openBrowser, stdout, stderr,
		func() error { return writeProjectHookControlAtPath(hookControlPath, hookControlMode) },
		func() error { return os.Remove(hookControlPath) },
	)
}

func prepareProjectUp(opts commandOptions) (config.Config, bool, string, string, string, error) {
	root, err := resolveProjectRoot(opts.Dir)
	if err != nil {
		return config.Config{}, false, "", "", "", err
	}
	projectCfg, loadedFromFile, configPath, err := loadProjectRunConfig(root)
	if err != nil {
		return config.Config{}, false, "", "", "", err
	}

	cfg := config.Load()
	applyProjectRunConfig(&cfg, projectCfg)
	if err := applyListenOptions(&cfg, commandOptions{Addr: opts.Addr, Port: opts.Port}); err != nil {
		return config.Config{}, false, "", "", "", err
	}
	openBrowser := opts.OpenBrowser || projectCfg.OpenBrowser
	summary := formatProjectUpSummary(root, configPath, projectCfg.HookMode, projectHookControlPath(root), loadedFromFile)
	return cfg, openBrowser, summary, projectHookControlPath(root), projectCfg.HookMode, nil
}

func resolveProjectRoot(dir string) (string, error) {
	root := strings.TrimSpace(dir)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("未找到项目目录：%s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("目标路径不是目录：%s", abs)
	}
	return abs, nil
}

func loadProjectRunConfig(root string) (projectRunConfig, bool, string, error) {
	cfg := defaultProjectRunConfig(root)
	path := projectConfigPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, path, nil
		}
		return projectRunConfig{}, false, path, fmt.Errorf("读取项目配置失败：%w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 JSON 无效：%w", err)
	}
	cfg.normalize(root)
	return cfg, true, path, nil
}

func defaultProjectRunConfig(root string) projectRunConfig {
	cfg := projectRunConfig{
		ProjectRoot: root,
		Host:        "127.0.0.1",
		Port:        8080,
		HookMode:    projectHookModeDryRun,
	}
	cfg.Workspace.Name = "Default Workspace"
	cfg.Workspace.Slug = "default"
	cfg.Workspace.OrgID = "local-org"
	return cfg
}

func (c *projectRunConfig) normalize(root string) {
	if c == nil {
		return
	}
	if strings.TrimSpace(c.ProjectRoot) == "" {
		c.ProjectRoot = root
	}
	if strings.TrimSpace(c.Host) == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 8080
	}
	c.HookMode = normalizeProjectHookMode(c.HookMode)
	if strings.TrimSpace(c.Workspace.Name) == "" {
		c.Workspace.Name = "Default Workspace"
	}
	if strings.TrimSpace(c.Workspace.Slug) == "" {
		c.Workspace.Slug = "default"
	}
	if strings.TrimSpace(c.Workspace.OrgID) == "" {
		c.Workspace.OrgID = "local-org"
	}
}

func normalizeProjectHookMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case projectHookModeOff:
		return projectHookModeOff
	case projectHookModeLive:
		return projectHookModeLive
	default:
		// 项目配置缺失或写错时必须回退 dry-run，避免用户无意中开启真实阻断。
		return projectHookModeDryRun
	}
}

func applyProjectRunConfig(cfg *config.Config, project projectRunConfig) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(project.Host) != "" {
		cfg.Host = strings.TrimSpace(project.Host)
	}
	if project.Port > 0 && project.Port <= 65535 {
		cfg.Port = fmt.Sprintf("%d", project.Port)
	}
	if strings.TrimSpace(project.Workspace.Name) != "" {
		cfg.DefaultWorkspaceName = strings.TrimSpace(project.Workspace.Name)
	}
	if strings.TrimSpace(project.Workspace.Slug) != "" {
		cfg.DefaultWorkspaceSlug = strings.TrimSpace(project.Workspace.Slug)
	}
	if strings.TrimSpace(project.Workspace.OrgID) != "" {
		cfg.DefaultWorkspaceOrgID = strings.TrimSpace(project.Workspace.OrgID)
	}
}

func writeProjectInitFiles(root, initTarget string) (projectInitReport, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return projectInitReport{}, fmt.Errorf("项目目录不能为空")
	}
	initTarget = normalizeInitTarget(initTarget)
	if initTarget == "" {
		return projectInitReport{}, fmt.Errorf("init 仅支持 all、codex 或 claude")
	}
	cfg := defaultProjectRunConfig(root)
	report := projectInitReport{
		Root:       root,
		ConfigPath: projectConfigPath(root),
		Protected:  projectProtectedPath(root),
		ReadmePath: projectReadmePath(root),
		PromptPath: projectPromptPath(root),
	}
	commonFiles := map[string]string{
		report.ConfigPath: renderProjectConfigFile(cfg),
		report.Protected:  renderProjectProtectedFile(cfg),
		report.ReadmePath: renderProjectReadmeFile(cfg),
		report.PromptPath: renderProjectPromptFile(cfg),
	}
	for path, content := range commonFiles {
		ok, err := writeFileIfMissing(path, []byte(content), 0o600)
		if err != nil {
			return projectInitReport{}, err
		}
		if ok {
			report.Created = append(report.Created, path)
		} else {
			report.Skipped = append(report.Skipped, path)
		}
	}
	if initTarget == projectInitModeAll || initTarget == projectInitModeCodex {
		files := map[string]string{
			projectCodexConfigSnippetPath(root): renderCodexConfigSnippet(cfg),
			projectCodexHooksPath(root):         renderCodexHooksSnippet(cfg),
		}
		for path, content := range files {
			ok, err := writeFileIfMissing(path, []byte(content), 0o600)
			if err != nil {
				return projectInitReport{}, err
			}
			if ok {
				report.Created = append(report.Created, path)
			} else {
				report.Skipped = append(report.Skipped, path)
			}
			report.CodexFiles = append(report.CodexFiles, path)
		}
	}
	if initTarget == projectInitModeAll || initTarget == projectInitModeClaude {
		files := map[string]string{
			projectClaudeMCPPath(root):      renderClaudeMCPSnippet(cfg),
			projectClaudeSettingsPath(root): renderClaudeSettingsSnippet(cfg),
		}
		for path, content := range files {
			ok, err := writeFileIfMissing(path, []byte(content), 0o600)
			if err != nil {
				return projectInitReport{}, err
			}
			if ok {
				report.Created = append(report.Created, path)
			} else {
				report.Skipped = append(report.Skipped, path)
			}
			report.ClaudeFiles = append(report.ClaudeFiles, path)
		}
	}
	sortPaths(&report.Created)
	sortPaths(&report.Skipped)
	sortPaths(&report.CodexFiles)
	sortPaths(&report.ClaudeFiles)
	return report, nil
}

func normalizeInitTarget(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", projectInitModeAll:
		return projectInitModeAll
	case projectInitModeCodex:
		return projectInitModeCodex
	case projectInitModeClaude:
		return projectInitModeClaude
	default:
		return ""
	}
}

func agentToolGateCommandName(goos string) string {
	if goos == "windows" {
		return "agenttoolgate.exe"
	}
	return "agenttoolgate"
}

func currentAgentToolGateCommandName() string {
	return agentToolGateCommandName(runtime.GOOS)
}

func projectConfigPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "config.json")
}

func projectProtectedPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "protected.json")
}

func projectReadmePath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "README.md")
}

func projectPromptPath(root string) string {
	return filepath.Join(root, "AGENTTOOLGATE.md")
}

func projectCodexConfigSnippetPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "clients", "codex.config.snippet.toml")
}

func projectCodexHooksPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "clients", "codex.hooks.json")
}

func projectClaudeMCPPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "clients", "claude.mcp.json")
}

func projectClaudeSettingsPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "clients", "claude.settings.snippet.json")
}

func projectHookControlPath(root string) string {
	return filepath.Join(root, ".tmp", "agenttoolgate", "hook-control.json")
}

func writeProjectHookControl(root, mode string) error {
	return writeProjectHookControlAtPath(projectHookControlPath(root), mode)
}

func writeProjectHookControlAtPath(path, mode string) error {
	doc := hookControlDocument{
		Mode:      normalizeProjectHookMode(mode),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:    "项目级 up",
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tempFile, err := os.CreateTemp(dir, "hook-control-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(payload); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func renderProjectConfigFile(cfg projectRunConfig) string {
	type localProjectRunConfig struct {
		ProjectRoot string `json:"projectRoot,omitempty"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Workspace   struct {
			Name  string `json:"name"`
			Slug  string `json:"slug"`
			OrgID string `json:"orgId"`
		} `json:"workspace"`
		HookMode    string `json:"hookMode"`
		OpenBrowser bool   `json:"openBrowser"`
	}
	doc := localProjectRunConfig{
		ProjectRoot: "<repo>",
		Host:        cfg.Host,
		Port:        cfg.Port,
		HookMode:    cfg.HookMode,
		OpenBrowser: cfg.OpenBrowser,
	}
	doc.Workspace = cfg.Workspace
	data, _ := json.MarshalIndent(doc, "", "  ")
	return string(append(data, '\n'))
}

func renderProjectProtectedFile(cfg projectRunConfig) string {
	type firewall struct {
		Enabled     bool     `json:"enabled"`
		DefaultMode string   `json:"defaultMode"`
		Notes       []string `json:"notes"`
	}
	doc := struct {
		Version             int                 `json:"version"`
		ProjectRoot         string              `json:"projectRoot"`
		Workspace           projectWorkspaceDoc `json:"workspace"`
		LocalActionFirewall firewall            `json:"localActionFirewall"`
	}{
		Version:     1,
		ProjectRoot: "<repo>",
		Workspace: projectWorkspaceDoc{
			Name:  cfg.Workspace.Name,
			Slug:  cfg.Workspace.Slug,
			OrgID: cfg.Workspace.OrgID,
		},
		LocalActionFirewall: firewall{
			Enabled:     true,
			DefaultMode: projectHookModeDryRun,
			Notes: []string{
				"项目级保护文件只保存安全元数据，不存放敏感凭据、密钥明文或连接串密码。",
				"后续可以作为 Guard Core 的项目上下文输入或扩展点。",
			},
		},
	}
	return mustJSONLine(doc)
}

type projectWorkspaceDoc struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	OrgID string `json:"orgId"`
}

func renderProjectReadmeFile(cfg projectRunConfig) string {
	var b strings.Builder
	commandName := currentAgentToolGateCommandName()
	b.WriteString("# AgentToolGate 项目级初始化说明\n\n")
	b.WriteString("本目录由 `" + commandName + " init` 生成，用于记录项目级安全偏好与客户端接入片段。\n\n")
	b.WriteString("## 文件说明\n\n")
	b.WriteString("- `config.json`：本项目的本地运行偏好，包含 host、port、workspace 与 hook mode。\n")
	b.WriteString("- `protected.json`：项目级保护策略占位文件，未来可接入更细的 Guard Core 项目上下文。\n")
	b.WriteString("- `clients/`：Codex / Claude Code 可复制的配置片段。\n")
	b.WriteString("- `AGENTTOOLGATE.md`：给 AI 客户端和人类读者的最小安全提示。\n\n")
	b.WriteString("`clients/*.json` 根部只保留客户端可消费字段；复制说明只写在本文档，避免把无关 `note` 字段带进客户端配置。\n\n")
	b.WriteString("## 默认值\n\n")
	b.WriteString("- host: `" + cfg.Host + "`\n")
	b.WriteString("- port: `" + fmt.Sprintf("%d", cfg.Port) + "`\n")
	b.WriteString("- workspace: `" + cfg.Workspace.OrgID + " / " + cfg.Workspace.Slug + "`\n")
	b.WriteString("- hook mode: `" + cfg.HookMode + "`\n\n")
	b.WriteString("## 使用方式\n\n")
	b.WriteString("1. 运行 `" + commandName + " up` 启动本项目的本地防火墙。\n")
	b.WriteString("2. 需要切换模式时，编辑 `config.json` 中的 `hookMode`。\n")
	b.WriteString("3. 不要在这些文件里写入敏感凭据、密钥明文或连接串密码。\n")
	return b.String()
}

func renderProjectPromptFile(cfg projectRunConfig) string {
	var b strings.Builder
	commandName := currentAgentToolGateCommandName()
	b.WriteString("# AgentToolGate 项目提示\n\n")
	b.WriteString("当前项目已启用 AgentToolGate。\n\n")
	b.WriteString("## 当前安全摘要\n\n")
	b.WriteString("- 项目根目录: `<repo>`\n")
	b.WriteString("- 访问地址: `http://" + cfg.Host + ":" + fmt.Sprintf("%d", cfg.Port) + "`\n")
	b.WriteString("- Workspace: `" + cfg.Workspace.OrgID + " / " + cfg.Workspace.Slug + "`\n")
	b.WriteString("- Hook mode: `" + cfg.HookMode + "`\n\n")
	b.WriteString("## 规则\n\n")
	b.WriteString("- `dry-run` 只预览，不真正阻断。\n")
	b.WriteString("- `live` 才是实际拦截，但仍然是 guardrail，不是 OS sandbox。\n")
	b.WriteString("- `approval_required` / `deny` 都不是普通失败，请先看 UI 或审计信息。\n")
	b.WriteString("- 不要把敏感凭据、密钥明文、`.env` 内容或连接串密码写入 prompt、日志或配置文件。\n\n")
	b.WriteString("## 下一步\n\n")
	b.WriteString("- 运行 `" + commandName + " up`。\n")
	b.WriteString("- 需要 Codex / Claude Code 配置片段时，复制 `.agenttoolgate/clients/` 下的文件。\n")
	return b.String()
}

func renderCodexConfigSnippet(cfg projectRunConfig) string {
	var b strings.Builder
	commandName := currentAgentToolGateCommandName()
	b.WriteString("# 复制到 ~/.codex/config.toml 或交给 ccswitch 管理的项目级片段\n")
	b.WriteString("# 下方项目路径请由本机实际仓库根目录替换；示例里统一使用 <repo>\n")
	b.WriteString("[projects.\"<repo>\"]\n")
	b.WriteString("trust_level = \"trusted\"\n\n")
	b.WriteString("[features]\n")
	b.WriteString("hooks = true\n\n")
	b.WriteString("[mcp_servers.agenttoolgate]\n")
	b.WriteString("url = \"http://127.0.0.1:")
	b.WriteString(fmt.Sprintf("%d", cfg.Port))
	b.WriteString("/mcp\"\n\n")
	b.WriteString("# 可选命令等价参考：codex mcp add agenttoolgate --url http://127.0.0.1:")
	b.WriteString(fmt.Sprintf("%d", cfg.Port))
	b.WriteString("/mcp\n")
	b.WriteString("# 如果需要手动桥接项目级 hook，可指向：" + commandName + " guard hook codex --input -\n")
	return b.String()
}

func renderCodexHooksSnippet(cfg projectRunConfig) string {
	commandName := currentAgentToolGateCommandName()
	doc := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": commandName + " guard hook codex --input -",
							"timeout": 30,
						},
					},
				},
			},
		},
	}
	return mustJSONLine(doc)
}

func renderClaudeMCPSnippet(cfg projectRunConfig) string {
	doc := map[string]any{
		"mcpServers": map[string]any{
			"agenttoolgate": map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("http://127.0.0.1:%d/mcp", cfg.Port),
				"headers": map[string]any{
					"X-Workspace-Org-Id": cfg.Workspace.OrgID,
				},
			},
		},
	}
	return mustJSONLine(doc)
}

func renderClaudeSettingsSnippet(cfg projectRunConfig) string {
	commandName := currentAgentToolGateCommandName()
	doc := map[string]any{
		"env": map[string]any{
			"CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR": "1",
		},
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": commandName + " guard hook claude --input -",
							"timeout": 30,
						},
					},
				},
			},
		},
	}
	return mustJSONLine(doc)
}

func mustJSONLine(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.String()
}

func writeFileIfMissing(path string, data []byte, perm os.FileMode) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	// init 只创建缺失文件：用户手工改过的项目配置不能被静默覆盖。
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return false, err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, err
	}
	cleanup = false
	return true, nil
}

func sortPaths(paths *[]string) {
	if paths == nil {
		return
	}
	s := *paths
	if len(s) < 2 {
		return
	}
	for i := 0; i < len(s)-1; i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func printInitPathList(w io.Writer, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w, label+":")
	for _, item := range items {
		fmt.Fprintln(w, "  - "+item)
	}
}

func formatProjectUpSummary(root, configPath, hookMode, hookControlPath string, loadedFromFile bool) string {
	var b strings.Builder
	commandName := currentAgentToolGateCommandName()
	b.WriteString("AgentToolGate up 已读取项目配置\n")
	b.WriteString("===============================\n")
	b.WriteString("项目目录: " + root + "\n")
	if loadedFromFile {
		b.WriteString("项目配置: " + configPath + "\n")
	} else {
		b.WriteString("项目配置: " + configPath + "（未找到，使用默认值）\n")
		b.WriteString("提示: 建议先按客户端运行 " + commandName + " init codex 或 " + commandName + " init claude 生成项目级配置。\n")
	}
	b.WriteString("Hook mode: " + hookMode + "\n")
	b.WriteString("Hook control: " + hookControlPath + "\n")
	b.WriteString("启动后 UI: 查看随后启动摘要里的“访问地址”。\n")
	b.WriteString("MCP: Codex / Claude Code 默认使用 /mcp；/mcp/sse 仅作为旧客户端 fallback。\n")
	b.WriteString("客户端片段: .agenttoolgate/clients/ 可复制到 Codex / Claude / ccswitch。\n")
	b.WriteString("本地诊断: " + commandName + " doctor\n\n")
	return b.String()
}

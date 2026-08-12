package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/guard"
	"agenttoolgate/backend/internal/hookassets"
)

const (
	projectInitModeAll    = "all"
	projectInitModeCodex  = "codex"
	projectInitModeClaude = "claude"
	projectHookModeOff    = "off"
	projectHookModeDryRun = "dry-run"
	projectHookModeLive   = "live"
	projectRuntimeExclude = "/.tmp/agenttoolgate/"
	codexUnixHookCommand  = `python3 "$(git rev-parse --show-toplevel)/.codex/hooks/agent-guard-pretool.py"`
	codexWinHookCommand   = `python "$(git rev-parse --show-toplevel)/.codex/hooks/agent-guard-pretool.py"`
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
	Refreshed   []string
	Backups     []string
}

func runInitCommand(opts commandOptions, stdout, stderr io.Writer) int {
	if strings.TrimSpace(opts.Addr) != "" || strings.TrimSpace(opts.Port) != "" || opts.OpenBrowser {
		fmt.Fprintln(stderr, "init 仅支持 --dir、--refresh-hooks 和 init codex|claude|all")
		return 2
	}
	initTarget := normalizeInitTarget(opts.InitTarget)
	if opts.RefreshHooks && initTarget != projectInitModeCodex {
		fmt.Fprintln(stderr, "--refresh-hooks 仅适用于 init codex")
		return 2
	}
	root, err := resolveProjectRoot(opts.Dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if initTarget == projectInitModeAll || initTarget == projectInitModeCodex {
		if err := validateCodexProjectGitRoot(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if err := ensureProjectRuntimeGitExclude(root); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	report, err := writeProjectInitFilesWithOptions(root, initTarget, opts.RefreshHooks)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if opts.RefreshHooks {
		fmt.Fprintln(stdout, "AgentToolGate Codex Hook 运行文件处理完成")
		fmt.Fprintln(stdout, "项目目录: "+report.Root)
		printInitPathList(stdout, "Codex Hook 文件", report.CodexFiles)
		printInitPathList(stdout, "已生成", report.Created)
		printInitPathList(stdout, "已跳过", report.Skipped)
		printInitPathList(stdout, "已刷新", report.Refreshed)
		printInitPathList(stdout, "恢复备份", report.Backups)
		fmt.Fprintln(stdout, "项目 TOML、用户级配置和 Hook 信任未修改；请重新运行 up，再用 doctor 和 Codex /hooks 核对。")
		return 0
	}
	fmt.Fprintln(stdout, "AgentToolGate init 文件处理完成")
	fmt.Fprintln(stdout, "项目目录: "+report.Root)
	fmt.Fprintln(stdout, "项目配置: "+report.ConfigPath)
	fmt.Fprintln(stdout, "保护策略: "+report.Protected)
	fmt.Fprintln(stdout, "项目说明: "+report.ReadmePath)
	fmt.Fprintln(stdout, "AI 提示: "+report.PromptPath)
	fmt.Fprintln(stdout, "默认 hook mode: dry-run")
	cmdName := currentAgentToolGateCommandName()
	fmt.Fprintf(stdout, "下一步: 运行 %s up --open；Codex 用户还需按键合并 .agenttoolgate/clients/codex.config.snippet.toml，并在 /hooks 中信任 Hook。\n", cmdName)
	printInitPathList(stdout, "Codex 文件", report.CodexFiles)
	printInitPathList(stdout, "Claude 片段", report.ClaudeFiles)
	printInitPathList(stdout, "已生成", report.Created)
	printInitPathList(stdout, "已跳过", report.Skipped)
	printInitPathList(stdout, "已刷新", report.Refreshed)
	printInitPathList(stdout, "恢复备份", report.Backups)
	if initTarget == projectInitModeAll || initTarget == projectInitModeCodex {
		status := codexProjectConfigStatus(projectCodexProjectConfigPath(root))
		fmt.Fprintln(stdout, "Codex 项目配置状态: "+status)
		if status != "configured" {
			fmt.Fprintln(stdout, "Codex 接入待处理: ATG 未覆盖已有 .codex/config.toml；请按键合并 .agenttoolgate/clients/codex.project-hook.snippet.toml。")
		}
	}
	return 0
}

func runUpCommand(opts commandOptions, stdout, stderr io.Writer) int {
	cfg, openBrowser, summary, hookControlPath, hookControlMode, err := prepareProjectUp(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stdout, summary)
	activation, err := newProjectHookControlActivation(cfg, hookControlPath, hookControlMode)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return startServer(cfg, openBrowser, stdout, stderr,
		activation.publish,
		activation.rollback,
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
	projectCfg.ProjectRoot = root
	if projectCfg.HookMode != projectHookModeOff {
		if _, err := guard.LoadProjectProtection(root); err != nil {
			return config.Config{}, false, "", "", "", fmt.Errorf("项目保护策略无效：%w", err)
		}
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
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("解析项目目录失败：%w", err)
	}
	return resolved, nil
}

func validateCodexProjectGitRoot(root string) error {
	command := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("Codex 项目 Hook 需要目标目录本身是 Git 仓库根目录")
	}
	gitRoot := strings.TrimSpace(string(output))
	resolved, err := filepath.EvalSymlinks(gitRoot)
	if err != nil || !sameHookPath(root, resolved) {
		return fmt.Errorf("Codex 项目 Hook 必须在目标 Git 仓库根目录初始化")
	}
	return nil
}

func ensureProjectRuntimeGitExclude(root string) error {
	path, err := projectGitExcludePath(root)
	if err != nil {
		if !isGitWorkTree(root) {
			// 部分内部调用和单元测试只使用仓库形状的临时目录。只有真实 Git
			// worktree 才存在 status 污染问题；真实仓库定位失败仍必须显式报错。
			return nil
		}
		return err
	}
	if err := validateProjectGitExcludePath(path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 Git 本地 exclude 失败：%w", err)
	}
	if gitExcludeContainsPattern(raw, projectRuntimeExclude) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建 Git 本地 exclude 目录失败：%w", err)
	}
	if err := validateProjectGitExcludePath(path); err != nil {
		return err
	}
	updated := append([]byte(nil), raw...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(projectRuntimeExclude+"\n")...)
	if err := writeFileAtomicallyOutsideProject(path, updated, 0o600); err != nil {
		return fmt.Errorf("写入 Git 本地 exclude 失败：%w", err)
	}
	return nil
}

func isGitWorkTree(root string) bool {
	command := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	output, err := command.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

func projectGitExcludePath(root string) (string, error) {
	command := exec.Command("git", "-C", root, "rev-parse", "--git-common-dir")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("无法定位 Git 本地 exclude：%w", err)
	}
	gitDir := strings.TrimSpace(string(output))
	if gitDir == "" {
		return "", fmt.Errorf("Git 本地 exclude 路径为空")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	return filepath.Clean(filepath.Join(gitDir, "info", "exclude")), nil
}

func validateProjectGitExcludePath(path string) error {
	parent := filepath.Dir(path)
	current := filepath.VolumeName(parent) + string(filepath.Separator)
	relative := strings.TrimPrefix(parent, current)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return fmt.Errorf("检查 Git 本地 exclude 路径失败：%w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("拒绝沿非普通目录写入 Git 本地 exclude：%s", current)
		}
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("检查 Git 本地 exclude 失败：%w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("拒绝写入非普通 Git 本地 exclude：%s", path)
	}
	return nil
}

func gitExcludeContainsPattern(raw []byte, pattern string) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

func writeFileAtomicallyOutsideProject(path string, data []byte, perm os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
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
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := validateProjectGitExcludePath(path); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
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
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 JSON 无效：%w", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 JSON 无效：%w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 JSON 无效：%w", err)
	}
	rawHookMode, ok := fields["hookMode"]
	if !ok {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 hookMode 缺失")
	}
	var hookMode string
	if err := json.Unmarshal(rawHookMode, &hookMode); err != nil {
		return projectRunConfig{}, false, path, fmt.Errorf("项目配置 hookMode 无效")
	}
	normalizedMode, err := parseProjectHookMode(hookMode)
	if err != nil {
		return projectRunConfig{}, false, path, err
	}
	cfg.HookMode = normalizedMode
	cfg.normalize(root)
	return cfg, true, path, nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON 只能包含一个文档")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("JSON 无效")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("JSON 无效")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON 无效")
			}
			normalizedKey := strings.ToLower(key)
			if _, exists := seen[normalizedKey]; exists {
				return errors.New("JSON 存在重复字段")
			}
			seen[normalizedKey] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("JSON 无效")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("JSON 无效")
		}
	default:
		return errors.New("JSON 无效")
	}
	return nil
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
	c.HookMode = strings.ToLower(strings.TrimSpace(c.HookMode))
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

func parseProjectHookMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case projectHookModeOff:
		return projectHookModeOff, nil
	case projectHookModeDryRun:
		return projectHookModeDryRun, nil
	case projectHookModeLive:
		return projectHookModeLive, nil
	default:
		return "", fmt.Errorf("项目配置 hookMode 只支持 off、dry-run 或 live")
	}
}

func applyProjectRunConfig(cfg *config.Config, project projectRunConfig) {
	if cfg == nil {
		return
	}
	if projectRoot := strings.TrimSpace(project.ProjectRoot); projectRoot != "" {
		cfg.ProjectRoot = filepath.Clean(projectRoot)
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
	return writeProjectInitFilesWithOptions(root, initTarget, false)
}

func writeProjectInitFilesWithOptions(root, initTarget string, refreshHooks bool) (projectInitReport, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return projectInitReport{}, fmt.Errorf("项目目录不能为空")
	}
	initTarget = normalizeInitTarget(initTarget)
	if initTarget == "" {
		return projectInitReport{}, fmt.Errorf("init 仅支持 all、codex 或 claude")
	}
	if refreshHooks && initTarget != projectInitModeCodex {
		return projectInitReport{}, fmt.Errorf("--refresh-hooks 仅适用于 init codex")
	}
	cfg := defaultProjectRunConfig(root)
	report := projectInitReport{
		Root:       root,
		ConfigPath: projectConfigPath(root),
		Protected:  projectProtectedPath(root),
		ReadmePath: projectReadmePath(root),
		PromptPath: projectPromptPath(root),
	}
	if refreshHooks {
		if err := writeCodexRuntimeFiles(root, &report, true); err != nil {
			return projectInitReport{}, err
		}
		sortPaths(&report.Created)
		sortPaths(&report.Skipped)
		sortPaths(&report.CodexFiles)
		sortPaths(&report.Refreshed)
		sortPaths(&report.Backups)
		return report, nil
	}
	if initTarget == projectInitModeAll || initTarget == projectInitModeCodex {
		if err := rejectCodexHookSourceConflict(root); err != nil {
			return projectInitReport{}, err
		}
		if err := validateCodexRuntimeFiles(root); err != nil {
			return projectInitReport{}, err
		}
	}
	commonFiles := map[string]string{
		report.ConfigPath: renderProjectConfigFile(cfg),
		report.Protected:  renderProjectProtectedFile(cfg),
		report.ReadmePath: renderProjectReadmeFile(cfg),
		report.PromptPath: renderProjectPromptFile(cfg),
	}
	for path, content := range commonFiles {
		ok, err := writeFileIfMissing(root, path, []byte(content), 0o600)
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
		projectHookConfig := renderCodexProjectHookConfig()
		for _, file := range []struct {
			path    string
			content []byte
		}{
			{path: projectCodexConfigSnippetPath(root), content: []byte(renderCodexConfigSnippet(cfg))},
			{path: projectCodexProjectSnippetPath(root), content: []byte(projectHookConfig)},
		} {
			ok, err := writeFileIfMissing(root, file.path, file.content, 0o600)
			if err != nil {
				return projectInitReport{}, err
			}
			if ok {
				report.Created = append(report.Created, file.path)
			} else {
				report.Skipped = append(report.Skipped, file.path)
			}
			report.CodexFiles = append(report.CodexFiles, file.path)
		}
		if err := rejectCodexHookSourceConflict(root); err != nil {
			return projectInitReport{}, err
		}
		configPath := projectCodexProjectConfigPath(root)
		configCreated, err := writeFileIfMissing(root, configPath, []byte(projectHookConfig), 0o600)
		if err != nil {
			return projectInitReport{}, err
		}
		if configCreated {
			report.Created = append(report.Created, configPath)
		} else {
			report.Skipped = append(report.Skipped, configPath)
		}
		report.CodexFiles = append(report.CodexFiles, configPath)
		if err := writeCodexRuntimeFiles(root, &report, false); err != nil {
			return projectInitReport{}, err
		}
		if err := rejectCodexHookSourceConflict(root); err != nil {
			if configCreated {
				if rollbackErr := removeFileIfUnchanged(root, configPath, []byte(projectHookConfig)); rollbackErr != nil {
					return projectInitReport{}, fmt.Errorf("%v；回滚 ATG 项目 Hook 失败：%w", err, rollbackErr)
				}
			}
			return projectInitReport{}, err
		}
	}
	if initTarget == projectInitModeAll || initTarget == projectInitModeClaude {
		files := map[string]string{
			projectClaudeMCPPath(root):      renderClaudeMCPSnippet(cfg),
			projectClaudeSettingsPath(root): renderClaudeSettingsSnippet(cfg),
		}
		for path, content := range files {
			ok, err := writeFileIfMissing(root, path, []byte(content), 0o600)
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
	sortPaths(&report.Refreshed)
	sortPaths(&report.Backups)
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

func projectCodexProjectSnippetPath(root string) string {
	return filepath.Join(root, ".agenttoolgate", "clients", "codex.project-hook.snippet.toml")
}

func projectCodexProjectConfigPath(root string) string {
	return filepath.Join(root, ".codex", "config.toml")
}

func projectCodexHooksJSONPath(root string) string {
	return filepath.Join(root, ".codex", "hooks.json")
}

func projectCodexHookAdapterPath(root string) string {
	return filepath.Join(root, ".codex", "hooks", "agent-guard-pretool.py")
}

func projectCodexHookCorePath(root string) string {
	return filepath.Join(root, ".codex", "hooks", "_guard_core.py")
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
	return writeProjectHookControlAtPath(root, projectHookControlPath(root), mode)
}

func writeProjectHookControlAtPath(root, path, mode string) error {
	payload, err := marshalProjectHookControlPayload(mode, "", "")
	if err != nil {
		return err
	}
	return writeProjectHookControlPayload(root, path, payload)
}

func marshalProjectHookControlPayload(mode, endpoint, executable string) ([]byte, error) {
	normalizedMode, err := parseProjectHookMode(mode)
	if err != nil {
		return nil, err
	}
	normalizedEndpoint, err := normalizeHookControlEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	normalizedExecutable, err := normalizeHookControlExecutable(executable)
	if err != nil {
		return nil, err
	}
	doc := hookControlDocument{
		Mode:       normalizedMode,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Reason:     "项目级 up",
		Endpoint:   normalizedEndpoint,
		Executable: normalizedExecutable,
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	return payload, nil
}

func writeProjectHookControlPayload(root, path string, payload []byte) error {
	if err := ensureProjectRuntimeGitExclude(root); err != nil {
		return err
	}
	return writeProjectFileAtomically(root, path, payload, 0o600)
}

type projectHookControlActivation struct {
	root             string
	path             string
	mode             string
	endpoint         string
	executable       string
	previous         []byte
	publishedPayload []byte
	hadPrevious      bool
	published        bool
}

func newProjectHookControlActivation(cfg config.Config, path, mode string) (projectHookControlActivation, error) {
	activation := projectHookControlActivation{
		root: cfg.ProjectRoot,
		path: path,
		mode: mode,
	}
	if !codexProjectRuntimeMetadataSupported(cfg.ProjectRoot) {
		return activation, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return projectHookControlActivation{}, fmt.Errorf("无法定位当前 AgentToolGate 二进制")
	}
	executable, err = normalizeHookControlExecutable(executable)
	if err != nil {
		return projectHookControlActivation{}, fmt.Errorf("无法验证当前 AgentToolGate 二进制")
	}
	activation.endpoint = publicListenURL(cfg)
	activation.executable = executable
	return activation, nil
}

func (activation *projectHookControlActivation) publish() error {
	activation.previous = nil
	activation.publishedPayload = nil
	activation.hadPrevious = false
	activation.published = false
	if err := validateProjectFileTarget(activation.root, activation.path); err != nil {
		return err
	}
	previous, err := os.ReadFile(activation.path)
	if err == nil {
		activation.previous = append([]byte(nil), previous...)
		activation.hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	payload, err := marshalProjectHookControlPayload(activation.mode, activation.endpoint, activation.executable)
	if err != nil {
		return err
	}
	if err := writeProjectHookControlPayload(activation.root, activation.path, payload); err != nil {
		return err
	}
	activation.publishedPayload = append([]byte(nil), payload...)
	activation.published = true
	return nil
}

func (activation *projectHookControlActivation) rollback() error {
	if !activation.published {
		return nil
	}
	if err := validateProjectFileTarget(activation.root, activation.path); err != nil {
		return err
	}
	current, err := os.ReadFile(activation.path)
	if err != nil {
		if os.IsNotExist(err) {
			activation.published = false
			return nil
		}
		return err
	}
	if !bytes.Equal(current, activation.publishedPayload) {
		activation.published = false
		return nil
	}
	if activation.hadPrevious {
		if err := writeProjectHookControlPayload(activation.root, activation.path, activation.previous); err != nil {
			return err
		}
		activation.published = false
		return nil
	}
	if err := os.Remove(activation.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	activation.published = false
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
	type protectedPath struct {
		Pattern string `json:"pattern"`
		Read    string `json:"read,omitempty"`
		Write   string `json:"write,omitempty"`
		Delete  string `json:"delete,omitempty"`
		Exec    string `json:"exec,omitempty"`
		Reason  string `json:"reason,omitempty"`
	}
	type egress struct {
		Enabled       bool     `json:"enabled"`
		AllowedHosts  []string `json:"allowedHosts"`
		UnlistedWrite string   `json:"unlistedWrite"`
	}
	type firewall struct {
		Enabled        bool            `json:"enabled"`
		DefaultMode    string          `json:"defaultMode"`
		ProtectedPaths []protectedPath `json:"protectedPaths"`
		Egress         egress          `json:"egress"`
		Notes          []string        `json:"notes"`
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
			Enabled:        true,
			DefaultMode:    projectHookModeDryRun,
			ProtectedPaths: []protectedPath{},
			Egress: egress{
				Enabled:       false,
				AllowedHosts:  []string{},
				UnlistedWrite: "require_approval",
			},
			Notes: []string{
				"项目级保护文件只保存安全元数据，不存放敏感凭据、密钥明文或连接串密码。",
				"protectedPaths 仅支持仓库内相对路径；规则只能要求审批或拒绝，不能放松 Guard Core。",
				"egress 只治理 Hook 可见的网络写入，不提供完整数据血缘或 OS 网络隔离。",
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
	b.WriteString("本目录由 `" + commandName + " init` 生成，用于记录项目级安全偏好与客户端接入信息。\n\n")
	b.WriteString("## 文件说明\n\n")
	b.WriteString("- `config.json`：本项目的本地运行偏好，包含 host、port、workspace 与 hook mode。\n")
	b.WriteString("- `protected.json`：项目级受保护路径和外发规则；只允许收紧 Guard Core。\n")
	b.WriteString("- `clients/`：Codex 用户级配置、已有项目配置的 Hook 合并片段，以及 Claude Code 可复制片段。\n")
	b.WriteString("- `../.codex/config.toml` 与 `../.codex/hooks/`：Codex 实际读取的项目 Hook 配置和自包含运行文件。\n")
	b.WriteString("- `AGENTTOOLGATE.md`：给 AI 客户端和人类读者的最小安全提示。\n\n")
	b.WriteString("Codex 项目信任和 Hook 内容信任都由用户在 Codex 中确认；AgentToolGate 不会自动写入用户级配置或信任 Hash。\n\n")
	b.WriteString("## 默认值\n\n")
	b.WriteString("- host: `" + cfg.Host + "`\n")
	b.WriteString("- port: `" + fmt.Sprintf("%d", cfg.Port) + "`\n")
	b.WriteString("- workspace: `" + cfg.Workspace.OrgID + " / " + cfg.Workspace.Slug + "`\n")
	b.WriteString("- hook mode: `" + cfg.HookMode + "`\n\n")
	b.WriteString("## 使用方式\n\n")
	b.WriteString("1. 运行 `" + commandName + " up` 启动本项目的本地防火墙。\n")
	b.WriteString("2. 需要切换模式时，编辑 `config.json` 中的 `hookMode`。\n")
	b.WriteString("3. 需要保护核心目录时，在 `protected.json` 的 `protectedPaths` 中添加仓库相对路径规则。\n")
	b.WriteString("4. 不要在这些文件里写入敏感凭据、密钥明文或连接串密码。\n")
	b.WriteString("5. `doctor` 显示 adapter/Core 为 `modified` 时先审查差异；确认使用当前发行版覆盖后，运行 `" + commandName + " init codex --refresh-hooks`，再重新运行 `up`。\n")
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
	b.WriteString("- Codex 用户按 `.agenttoolgate/clients/codex.config.snippet.toml` 建立项目信任，再在 Codex `/hooks` 中核对并信任 Hook。\n")
	b.WriteString("- 如果 `.codex/config.toml` 已存在，按键合并 `.agenttoolgate/clients/codex.project-hook.snippet.toml`，不要重复追加 TOML 表。\n")
	b.WriteString("- Claude Code 用户复制 `.agenttoolgate/clients/` 下对应片段。\n")
	return b.String()
}

func renderCodexConfigSnippet(cfg projectRunConfig) string {
	var b strings.Builder
	b.WriteString("# 按键合并到 ~/.codex/config.toml 或 ccswitch 管理的用户级配置；不要重复追加同名 TOML 表\n")
	b.WriteString("# 下方项目路径请由本机实际仓库根目录替换；示例里统一使用 <repo>\n")
	b.WriteString("# Windows 使用 Codex 规范化后的小写绝对路径；TOML 单引号会原样保留反斜杠\n")
	b.WriteString("# 项目信任不等于 Hook 内容信任\n")
	b.WriteString("[projects.'<repo>']\n")
	b.WriteString("trust_level = \"trusted\"\n\n")
	b.WriteString("[features]\n")
	b.WriteString("hooks = true\n\n")
	b.WriteString("[mcp_servers.agenttoolgate]\n")
	b.WriteString("url = \"http://127.0.0.1:")
	b.WriteString(fmt.Sprintf("%d", cfg.Port))
	b.WriteString("/mcp\"\n")
	b.WriteString("default_tools_approval_mode = \"approve\"\n\n")
	b.WriteString("# 可选命令等价参考：codex mcp add agenttoolgate --url http://127.0.0.1:")
	b.WriteString(fmt.Sprintf("%d", cfg.Port))
	b.WriteString("/mcp\n")
	b.WriteString("# 启动 Codex 后在 /hooks 中核对命令和 Hash，再由用户显式信任\n")
	return b.String()
}

func renderCodexProjectHookConfig() string {
	var b strings.Builder
	b.WriteString("# AgentToolGate 项目级 Codex Hook；用户仍需在 Codex 中信任项目和 Hook 内容\n")
	b.WriteString("[features]\n")
	b.WriteString("hooks = true\n\n")
	b.WriteString("[hooks]\n\n")
	b.WriteString("[[hooks.PreToolUse]]\n")
	b.WriteString("matcher = " + fmt.Sprintf("%q", localActionHookMatcher) + "\n\n")
	b.WriteString("[[hooks.PreToolUse.hooks]]\n")
	b.WriteString("type = \"command\"\n")
	b.WriteString("command = '" + codexUnixHookCommand + "'\n")
	b.WriteString("commandWindows = '" + codexWinHookCommand + "'\n")
	b.WriteString("timeout = 30\n")
	b.WriteString("statusMessage = \"AgentToolGate 正在检查工具调用\"\n")
	return b.String()
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
					"matcher": localActionHookMatcher,
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

const localActionHookMatcher = "^(Bash|Read|Grep|Glob|Write|Edit|MultiEdit|NotebookEdit|WebFetch|WebSearch|shell|command|powershell|pwsh|apply_patch|http[.]request|network[.]request|mcp__.*)$"

func mustJSONLine(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return buf.String()
}

var (
	linkProjectFile              = os.Link
	renameProjectFileNoReplace   = moveProjectFileNoReplace
	replaceProjectFileWithBackup = replaceProjectFileKeepingBackup
)

func writeFileIfMissing(root, path string, data []byte, perm os.FileMode) (bool, error) {
	if err := validateProjectWritePath(root, path); err != nil {
		return false, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("拒绝写入符号链接：%s", path)
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	// init 只创建缺失文件：用户手工改过的项目配置不能被静默覆盖。
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	if err := validateProjectWritePath(root, path); err != nil {
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
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := linkProjectFile(tempPath, path); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		if validateErr := validateProjectWritePath(root, path); validateErr != nil {
			return false, validateErr
		}
		if renameErr := renameProjectFileNoReplace(tempPath, path); renameErr != nil {
			if os.IsExist(renameErr) {
				return false, nil
			}
			return false, fmt.Errorf("无法原子创建项目文件：%w", renameErr)
		}
		cleanup = false
	}
	return true, nil
}

func rejectCodexHookSourceConflict(root string) error {
	path := projectCodexHooksJSONPath(root)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("检查 Codex Hook 来源失败：%w", err)
	}
	return fmt.Errorf("检测到 %s；Codex 会合并同层 hooks.json 与 config.toml Hook。请先人工保留一种来源：迁移到 ATG 默认 TOML 时备份并移除 hooks.json 后重试，继续使用 JSON 时仅用 init codex --refresh-hooks 更新运行文件", path)
}

var replaceCodexRuntimeFile = replaceCodexRuntimeFileFromSnapshot

type codexRuntimeFileSpec struct {
	path    string
	content []byte
}

type codexRuntimeFileSnapshot struct {
	existed bool
	content []byte
	perm    os.FileMode
	info    os.FileInfo
}

type codexRuntimeFileOutcome struct {
	spec      codexRuntimeFileSpec
	created   bool
	refreshed bool
	backup    string
	previous  codexRuntimeFileSnapshot
	installed codexRuntimeFileSnapshot
}

func writeCodexRuntimeFile(root string, spec codexRuntimeFileSpec, refresh bool, snapshot codexRuntimeFileSnapshot) (codexRuntimeFileOutcome, error) {
	outcome := codexRuntimeFileOutcome{spec: spec, previous: snapshot}
	if err := ensureCodexRuntimeFileMatchesSnapshot(spec.path, snapshot); err != nil {
		return outcome, err
	}
	if !snapshot.existed {
		created, err := writeFileIfMissing(root, spec.path, spec.content, 0o600)
		if err != nil {
			return outcome, err
		}
		if !created {
			return outcome, fmt.Errorf("Codex Hook 文件已被并发创建：%s", spec.path)
		}
		installed, err := snapshotCodexRuntimeFile(spec.path)
		if err != nil {
			return outcome, err
		}
		if !installed.existed || !bytes.Equal(installed.content, spec.content) {
			return outcome, fmt.Errorf("Codex Hook 文件创建后被并发修改：%s", spec.path)
		}
		outcome.created = true
		outcome.installed = installed
		return outcome, nil
	}
	if bytes.Equal(snapshot.content, spec.content) || !refresh {
		return outcome, nil
	}
	backup, installed, err := replaceCodexRuntimeFile(root, spec.path, spec.content, 0o600, snapshot)
	if err != nil {
		return outcome, err
	}
	outcome.refreshed = true
	outcome.backup = backup
	outcome.installed = installed
	return outcome, nil
}

func writeCodexRuntimeFiles(root string, report *projectInitReport, refresh bool) error {
	if report == nil {
		return fmt.Errorf("Codex init report 不能为空")
	}
	if err := validateCodexRuntimeFiles(root); err != nil {
		return err
	}
	bundle := hookassets.Codex()
	files := []codexRuntimeFileSpec{
		{path: projectCodexHookCorePath(root), content: bundle.Core},
		{path: projectCodexHookAdapterPath(root), content: bundle.Adapter},
	}
	snapshots := make(map[string]codexRuntimeFileSnapshot, len(files))
	for _, file := range files {
		snapshot, err := snapshotCodexRuntimeFile(file.path)
		if err != nil {
			return err
		}
		snapshots[file.path] = snapshot
	}

	outcomes := make([]codexRuntimeFileOutcome, 0, len(files))
	for _, file := range files {
		outcome, err := writeCodexRuntimeFile(root, file, refresh, snapshots[file.path])
		if err != nil {
			rollbackErr := rollbackCodexRuntimeFiles(root, outcomes)
			if rollbackErr != nil {
				return fmt.Errorf("更新 Codex Hook bundle 失败：%w；回滚失败：%v", err, rollbackErr)
			}
			return fmt.Errorf("更新 Codex Hook bundle 失败：%w", err)
		}
		outcomes = append(outcomes, outcome)
	}
	backups, err := archiveCodexRuntimeBackups(root, outcomes)
	if err != nil {
		return fmt.Errorf("Codex Hook bundle 已更新，但旧文件备份保留失败：%w", err)
	}
	report.Backups = append(report.Backups, backups...)

	for _, outcome := range outcomes {
		switch {
		case outcome.created:
			report.Created = append(report.Created, outcome.spec.path)
		case outcome.refreshed:
			report.Refreshed = append(report.Refreshed, outcome.spec.path)
		default:
			report.Skipped = append(report.Skipped, outcome.spec.path)
		}
		report.CodexFiles = append(report.CodexFiles, outcome.spec.path)
	}
	return nil
}

func snapshotCodexRuntimeFile(path string) (codexRuntimeFileSnapshot, error) {
	content, info, err := readCodexRuntimeFile(path)
	if os.IsNotExist(err) {
		return codexRuntimeFileSnapshot{}, nil
	}
	if err != nil {
		return codexRuntimeFileSnapshot{}, err
	}
	return codexRuntimeFileSnapshot{existed: true, content: content, perm: info.Mode().Perm(), info: info}, nil
}

func readCodexRuntimeFile(path string) ([]byte, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("拒绝刷新非普通 Codex Hook 文件：%s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(pathInfo, openedInfo) {
		return nil, nil, fmt.Errorf("Codex Hook 文件已被并发替换：%s", path)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	currentPathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(openedInfo, afterInfo) || !os.SameFile(afterInfo, currentPathInfo) ||
		openedInfo.Size() != afterInfo.Size() || !openedInfo.ModTime().Equal(afterInfo.ModTime()) || openedInfo.Mode() != afterInfo.Mode() {
		return nil, nil, fmt.Errorf("Codex Hook 文件读取期间被并发修改：%s", path)
	}
	return content, afterInfo, nil
}

func ensureCodexRuntimeFileMatchesSnapshot(path string, snapshot codexRuntimeFileSnapshot) error {
	if !snapshot.existed {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		return fmt.Errorf("Codex Hook 文件已被并发创建：%s", path)
	}
	content, info, err := readCodexRuntimeFile(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("Codex Hook 文件已被并发删除：%s", path)
	}
	if err != nil {
		return err
	}
	if snapshot.info == nil || !os.SameFile(snapshot.info, info) || snapshot.perm != info.Mode().Perm() || !bytes.Equal(snapshot.content, content) {
		return fmt.Errorf("Codex Hook 文件已被并发修改：%s", path)
	}
	return nil
}

func replaceCodexRuntimeFileFromSnapshot(root, path string, data []byte, perm os.FileMode, snapshot codexRuntimeFileSnapshot) (string, codexRuntimeFileSnapshot, error) {
	tempPath, err := stageProjectFile(root, path, data, perm)
	if err != nil {
		return "", codexRuntimeFileSnapshot{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	replacement, err := snapshotCodexRuntimeFile(tempPath)
	if err != nil {
		return "", codexRuntimeFileSnapshot{}, err
	}
	if err := ensureCodexRuntimeFileMatchesSnapshot(path, snapshot); err != nil {
		return "", codexRuntimeFileSnapshot{}, err
	}
	backupPath, err := replaceProjectFileWithBackup(path, tempPath)
	if err != nil {
		return "", codexRuntimeFileSnapshot{}, err
	}
	cleanup = false
	backup, err := snapshotCodexRuntimeFile(backupPath)
	if err != nil || !codexRuntimeSnapshotsEqual(snapshot, backup) {
		restoreErr := restoreCodexRuntimeBackup(root, path, backupPath, replacement)
		if restoreErr != nil {
			return "", codexRuntimeFileSnapshot{}, fmt.Errorf("Codex Hook 文件已被并发修改：%s；恢复失败：%v", path, restoreErr)
		}
		return "", codexRuntimeFileSnapshot{}, fmt.Errorf("Codex Hook 文件已被并发修改：%s", path)
	}
	installed, err := snapshotCodexRuntimeFile(path)
	if err != nil || !codexRuntimeSnapshotsEqual(replacement, installed) {
		restoreErr := restoreCodexRuntimeBackup(root, path, backupPath, replacement)
		if restoreErr != nil {
			return "", codexRuntimeFileSnapshot{}, fmt.Errorf("Codex Hook 新文件发布后被并发修改：%s；原文件保留在 %s；恢复失败：%v", path, backupPath, restoreErr)
		}
		return "", codexRuntimeFileSnapshot{}, fmt.Errorf("Codex Hook 新文件发布后被并发修改：%s", path)
	}
	return backupPath, installed, nil
}

func codexRuntimeSnapshotsEqual(expected, actual codexRuntimeFileSnapshot) bool {
	return expected.existed && actual.existed && expected.info != nil && actual.info != nil &&
		os.SameFile(expected.info, actual.info) && expected.perm == actual.perm && bytes.Equal(expected.content, actual.content)
}

func restoreCodexRuntimeBackup(root, targetPath, backupPath string, expectedDisplaced codexRuntimeFileSnapshot) error {
	if err := validateProjectWritePath(root, targetPath); err != nil {
		return err
	}
	if err := validateProjectWritePath(root, backupPath); err != nil {
		return err
	}
	displacedPath, err := replaceProjectFileWithBackup(targetPath, backupPath)
	if err != nil {
		return fmt.Errorf("原文件仍保留在 %s：%w", backupPath, err)
	}
	displaced, readErr := snapshotCodexRuntimeFile(displacedPath)
	if readErr == nil && codexRuntimeSnapshotsEqual(expectedDisplaced, displaced) {
		if _, err := archiveCodexRecoveryFile(root, displacedPath, "displaced"); err != nil {
			return fmt.Errorf("原文件已恢复；替换文件保留在 %s：%w", displacedPath, err)
		}
		return nil
	}

	restoredBackup, restoreErr := replaceProjectFileWithBackup(targetPath, displacedPath)
	if restoreErr != nil {
		return fmt.Errorf("原文件已恢复，但并发版本保留在 %s 且无法换回：%v", displacedPath, restoreErr)
	}
	archivedBackup, archiveErr := archiveCodexRecoveryFile(root, restoredBackup, "rollback")
	if archiveErr != nil {
		return fmt.Errorf("并发版本已换回，原文件保留在 %s；备份归档失败：%v", restoredBackup, archiveErr)
	}
	if readErr != nil {
		return fmt.Errorf("并发版本已换回，原文件保留在 %s；无法校验并发版本：%v", archivedBackup, readErr)
	}
	return fmt.Errorf("并发版本已换回，原文件保留在 %s", archivedBackup)
}

func rollbackCodexRuntimeFiles(root string, outcomes []codexRuntimeFileOutcome) error {
	var rollbackErrors []error
	for index := len(outcomes) - 1; index >= 0; index-- {
		outcome := outcomes[index]
		if !outcome.created && !outcome.refreshed {
			continue
		}
		if outcome.created {
			if _, err := os.Lstat(outcome.spec.path); os.IsNotExist(err) {
				continue
			}
			if err := removeCodexRuntimeFileFromSnapshot(root, outcome.spec.path, outcome.installed); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
			continue
		}
		if err := restoreCodexRuntimeBackup(root, outcome.spec.path, outcome.backup, outcome.installed); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func archiveCodexRuntimeBackups(root string, outcomes []codexRuntimeFileOutcome) ([]string, error) {
	var archiveErrors []error
	var archivedPaths []string
	for _, outcome := range outcomes {
		if !outcome.refreshed || strings.TrimSpace(outcome.backup) == "" {
			continue
		}
		// 原子交换后，其他进程仍可能通过交换前已打开的句柄继续写 displaced
		// 文件。这里必须绑定文件身份而不是旧内容，否则会把应当保留到 recovery
		// 的真实并发写入误判成刷新失败。
		backupInfo, err := codexRuntimeRegularFileInfo(outcome.backup)
		if err != nil {
			archiveErrors = append(archiveErrors, err)
			continue
		}
		if outcome.previous.info == nil || !os.SameFile(outcome.previous.info, backupInfo) {
			archiveErrors = append(archiveErrors, fmt.Errorf("Codex Hook 备份已被并发替换：%s", outcome.backup))
			continue
		}
		archivedPath, err := archiveCodexRecoveryFile(root, outcome.backup, "refresh")
		if err != nil {
			archiveErrors = append(archiveErrors, err)
			continue
		}
		archivedInfo, err := codexRuntimeRegularFileInfo(archivedPath)
		if err != nil || !os.SameFile(backupInfo, archivedInfo) {
			if err == nil {
				err = fmt.Errorf("Codex Hook 备份归档后文件身份不一致：%s", archivedPath)
			}
			archiveErrors = append(archiveErrors, err)
			continue
		}
		archivedPaths = append(archivedPaths, archivedPath)
	}
	return archivedPaths, errors.Join(archiveErrors...)
}

func codexRuntimeRegularFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝归档非普通 Codex Hook 备份：%s", path)
	}
	return info, nil
}

func validateCodexRuntimeFiles(root string) error {
	for _, path := range []string{projectCodexHookCorePath(root), projectCodexHookAdapterPath(root)} {
		if err := validateProjectWritePath(root, path); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("拒绝刷新非普通 Codex Hook 文件：%s", path)
		}
	}
	return nil
}

func replaceProjectFile(root, path string, data []byte, perm os.FileMode) error {
	return writeProjectFileAtomically(root, path, data, perm)
}

func writeProjectFileAtomically(root, path string, data []byte, perm os.FileMode) error {
	tempPath, err := stageProjectFile(root, path, data, perm)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := validateProjectWritePath(root, path); err != nil {
		return err
	}
	if err := validateProjectFileTarget(root, path); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func stageProjectFile(root, path string, data []byte, perm os.FileMode) (string, error) {
	if err := validateProjectWritePath(root, path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := validateProjectWritePath(root, path); err != nil {
		return "", err
	}
	if err := validateProjectFileTarget(root, path); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return "", err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}

func removeCodexRuntimeFileFromSnapshot(root, path string, snapshot codexRuntimeFileSnapshot) error {
	if err := validateProjectFileTarget(root, path); err != nil {
		return err
	}
	if err := ensureCodexRuntimeFileMatchesSnapshot(path, snapshot); err != nil {
		return err
	}
	quarantinePath, err := unusedSiblingPath(path, "remove")
	if err != nil {
		return err
	}
	if err := renameProjectFileNoReplace(path, quarantinePath); err != nil {
		return err
	}
	quarantined, readErr := snapshotCodexRuntimeFile(quarantinePath)
	if readErr == nil && codexRuntimeSnapshotsEqual(snapshot, quarantined) {
		if _, err := archiveCodexRecoveryFile(root, quarantinePath, "removed"); err != nil {
			return fmt.Errorf("待删除文件保留在 %s：%w", quarantinePath, err)
		}
		return nil
	}
	if restoreErr := renameProjectFileNoReplace(quarantinePath, path); restoreErr != nil {
		return fmt.Errorf("文件已被并发修改并保留在 %s；恢复失败：%v", quarantinePath, restoreErr)
	}
	if readErr != nil {
		return fmt.Errorf("文件已恢复；无法校验隔离文件：%w", readErr)
	}
	return fmt.Errorf("文件已被并发修改，未删除：%s", path)
}

func unusedSiblingPath(path, purpose string) (string, error) {
	marker, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"."+purpose+".*.tmp")
	if err != nil {
		return "", err
	}
	markerPath := marker.Name()
	if err := marker.Close(); err != nil {
		_ = os.Remove(markerPath)
		return "", err
	}
	if err := os.Remove(markerPath); err != nil {
		return "", err
	}
	return markerPath, nil
}

func archiveCodexRecoveryFile(root, path, purpose string) (string, error) {
	if err := validateProjectFileTarget(root, path); err != nil {
		return "", err
	}
	if err := ensureProjectRuntimeGitExclude(root); err != nil {
		return "", err
	}
	recoveryDir := filepath.Join(root, ".tmp", "agenttoolgate", "recovery")
	archiveProbe := filepath.Join(recoveryDir, "archive")
	if err := validateProjectWritePath(root, archiveProbe); err != nil {
		return "", err
	}
	if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
		return "", err
	}
	if err := validateProjectWritePath(root, archiveProbe); err != nil {
		return "", err
	}
	marker, err := os.CreateTemp(recoveryDir, filepath.Base(path)+"."+purpose+".*.bak")
	if err != nil {
		return "", err
	}
	archivePath := marker.Name()
	if err := marker.Close(); err != nil {
		_ = os.Remove(archivePath)
		return "", err
	}
	if err := os.Remove(archivePath); err != nil {
		return "", err
	}
	if err := renameProjectFileNoReplace(path, archivePath); err != nil {
		return "", err
	}
	return archivePath, nil
}

func removeFileIfUnchanged(root, path string, expected []byte) error {
	current, err := snapshotCodexRuntimeFile(path)
	if err != nil {
		return err
	}
	if !current.existed || !bytes.Equal(current.content, expected) {
		return fmt.Errorf("文件已被并发修改：%s", path)
	}
	return removeCodexRuntimeFileFromSnapshot(root, path, current)
}

func validateProjectFileTarget(root, path string) error {
	if err := validateProjectWritePath(root, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("拒绝访问非普通项目文件：%s", path)
	}
	return nil
}

func validateProjectWritePath(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return fmt.Errorf("解析项目目录失败：%w", err)
	}
	targetAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !pathWithinProjectRoot(rootAbs, targetAbs) {
		return fmt.Errorf("拒绝写入项目目录外路径：%s", targetAbs)
	}

	parent := filepath.Dir(targetAbs)
	relativeParent, err := filepath.Rel(rootAbs, parent)
	if err != nil {
		return err
	}
	current := rootAbs
	if relativeParent != "." {
		for _, part := range strings.Split(relativeParent, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, statErr := os.Lstat(current)
			if os.IsNotExist(statErr) {
				break
			}
			if statErr != nil {
				return statErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("拒绝沿符号链接写入项目文件：%s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("项目文件父路径不是目录：%s", current)
			}
		}
	}

	if _, err := os.Stat(parent); err == nil {
		resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr != nil {
			return resolveErr
		}
		if !pathWithinProjectRoot(resolvedRoot, resolvedParent) {
			return fmt.Errorf("拒绝沿项目外目录写入：%s", parent)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func pathWithinProjectRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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
	b.WriteString("Codex 项目 Hook: .codex/config.toml；首次使用需在 Codex 中完成项目和 Hook 信任。Hook 命令依赖 Git 与 Python 3。\n")
	if codexProjectRuntimeMetadataSupported(root) {
		b.WriteString("Hook runtime: 服务启动后把回环 endpoint 和当前 executable 写入 repo-local control。\n")
	} else {
		b.WriteString("Hook runtime: Codex adapter 未确认为 current，control 保持旧版兼容字段；请先用 doctor 核对，必要时用 init codex --refresh-hooks 更新后重启 up。\n")
	}
	b.WriteString("客户端片段: .agenttoolgate/clients/ 提供用户级 Codex / Claude / ccswitch 配置。\n")
	b.WriteString("本地诊断: " + commandName + " doctor --dir <project>\n\n")
	return b.String()
}

func formatProjectCodexDiagnostics(root string) string {
	bundle := hookassets.Codex()
	configStatus := codexProjectConfigStatus(projectCodexProjectConfigPath(root))
	hooksJSONStatus := projectFilePresenceStatus(projectCodexHooksJSONPath(root))
	adapterStatus := embeddedFileStatus(projectCodexHookAdapterPath(root), bundle.Adapter)
	coreStatus := embeddedFileStatus(projectCodexHookCorePath(root), bundle.Core)
	var b strings.Builder
	b.WriteString("Codex 项目接入诊断\n")
	b.WriteString("====================\n")
	b.WriteString("项目目录: " + root + "\n")
	b.WriteString("Codex 项目配置: " + configStatus + "\n")
	b.WriteString("Codex hooks.json: " + hooksJSONStatus + "\n")
	b.WriteString("Codex Hook adapter: " + adapterStatus + "\n")
	b.WriteString("Codex Hook core: " + coreStatus + "\n")
	b.WriteString("Codex Git: " + commandAvailabilityStatus("git") + "\n")
	b.WriteString("Codex Python 3: " + codexPythonStatus() + "\n")
	b.WriteString("ATG Hook mode: " + projectHookControlStatus(root) + "\n")
	b.WriteString("ATG Hook endpoint: " + projectHookEndpointStatus(root) + "\n")
	b.WriteString("Codex 项目信任: 需在用户 config.toml 中显式确认\n")
	b.WriteString("Codex Hook 信任: 需在 Codex /hooks 中确认 trusted\n")
	b.WriteString("说明: ATG 不会自动写入或信任用户级 Codex 配置。\n")
	if configStatus == "configured" && hooksJSONStatus != "missing" {
		b.WriteString("警告: config.toml 与 hooks.json 同层并存，Codex 会合并执行；请人工保留一种 Hook 来源。\n")
	} else if configStatus != "missing" && hooksJSONStatus != "missing" {
		b.WriteString("检查: hooks.json 已存在，config.toml 无法确认是否也声明 Hook；请人工核对并只保留一种来源。\n")
	}
	if adapterStatus != "current" || coreStatus != "current" {
		b.WriteString("Hook 文件更新: 审查自定义内容后，可用 init codex --refresh-hooks 仅覆盖 adapter/Core；随后重新运行 up。\n")
	}
	return b.String()
}

func projectFilePresenceStatus(path string) string {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unreadable"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "present (symlink)"
	}
	if !info.Mode().IsRegular() {
		return "present (not a file)"
	}
	return "present"
}

func codexProjectConfigStatus(path string) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unreadable"
	}
	if codexProjectHookConfigured(string(raw)) {
		return "configured"
	}
	return "custom"
}

func codexProjectHookConfigured(text string) bool {
	section := ""
	featureHooksSeen := false
	featureHooksEnabled := true
	matcherMatches := false
	handler := map[string]string{}
	configured := false

	finishHandler := func() {
		if matcherMatches && handler["type"] == "command" &&
			handler["command"] == codexUnixHookCommand &&
			(handler["commandWindows"] == codexWinHookCommand || handler["command_windows"] == codexWinHookCommand) {
			configured = true
		}
		handler = map[string]string{}
	}

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(stripTOMLComment(rawLine))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if section == "hooks.PreToolUse.hooks" {
				finishHandler()
			}
			switch line {
			case "[features]":
				section = "features"
				matcherMatches = false
			case "[[hooks.PreToolUse]]":
				section = "hooks.PreToolUse"
				matcherMatches = false
			case "[[hooks.PreToolUse.hooks]]":
				section = "hooks.PreToolUse.hooks"
				handler = map[string]string{}
			default:
				section = "other"
				matcherMatches = false
			}
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if section == "" && key == "features.hooks" {
			if featureHooksSeen {
				return false
			}
			featureHooksSeen = true
			if value == "true" {
				featureHooksEnabled = true
			} else if value == "false" {
				featureHooksEnabled = false
			} else {
				return false
			}
			continue
		}
		switch section {
		case "features":
			if key != "hooks" {
				continue
			}
			if featureHooksSeen {
				return false
			}
			featureHooksSeen = true
			if value == "true" {
				featureHooksEnabled = true
			} else if value == "false" {
				featureHooksEnabled = false
			} else {
				return false
			}
		case "hooks.PreToolUse":
			if key == "matcher" {
				matcher, valid := parseTOMLString(value)
				matcherMatches = valid && matcher == localActionHookMatcher
			}
		case "hooks.PreToolUse.hooks":
			if key == "type" || key == "command" || key == "commandWindows" || key == "command_windows" {
				parsed, valid := parseTOMLString(value)
				if !valid {
					return false
				}
				handler[key] = parsed
			}
		}
	}
	if section == "hooks.PreToolUse.hooks" {
		finishHandler()
	}
	return featureHooksEnabled && configured
}

func stripTOMLComment(line string) string {
	var quote rune
	escaped := false
	for index, char := range line {
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			continue
		}
		if char == '#' {
			return line[:index]
		}
	}
	return line
}

func parseTOMLString(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	if value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], !strings.Contains(value[1:len(value)-1], "'")
	}
	if value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	parsed, err := strconv.Unquote(value)
	return parsed, err == nil
}

func codexPythonStatus() string {
	command := "python3"
	if runtime.GOOS == "windows" {
		command = "python"
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "missing (" + command + ")"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, path, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)")
	if err := probe.Run(); err != nil {
		return "unusable (" + command + ", requires Python 3.10+)"
	}
	return "available (" + command + ", Python 3.10+)"
}

func commandAvailabilityStatus(command string) string {
	if _, err := exec.LookPath(command); err != nil {
		return "missing (" + command + ")"
	}
	return "available (" + command + ")"
}

func projectHookControlStatus(root string) string {
	doc, err := readHookControlDocument(root)
	if err != nil {
		return "invalid"
	}
	return doc.Mode
}

func projectHookEndpointStatus(root string) string {
	doc, err := readHookControlDocument(root)
	if err != nil {
		return "invalid"
	}
	if doc.Endpoint == "" {
		return "missing"
	}
	return doc.Endpoint
}

func embeddedFileStatus(path string, expected []byte) string {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unreadable"
	}
	if bytes.Equal(raw, expected) {
		return "current"
	}
	return "modified"
}

func codexProjectRuntimeMetadataSupported(root string) bool {
	bundle := hookassets.Codex()
	return embeddedFileStatus(projectCodexHookAdapterPath(root), bundle.Adapter) == "current"
}

package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

type ActionInput struct {
	Client         string `json:"client"`
	ToolName       string `json:"toolName"`
	ActionType     string `json:"actionType"`
	CWD            string `json:"cwd"`
	ProjectRoot    string `json:"projectRoot"`
	Command        string `json:"command"`
	Target         string `json:"target"`
	ContentPreview string `json:"contentPreview"`
	NetworkMethod  string `json:"networkMethod"`
	NetworkURL     string `json:"networkUrl"`
}

type Decision struct {
	Decision  string   `json:"decision"`
	RiskLevel string   `json:"riskLevel"`
	Silent    bool     `json:"silent"`
	Reason    string   `json:"reason"`
	Signals   []string `json:"signals,omitempty"`
	Category  string   `json:"category"`
}

func Evaluate(input ActionInput) Decision {
	cwd := normalizePathCandidate(input.CWD)
	projectRoot := normalizePathCandidate(input.ProjectRoot)
	target := normalizePathCandidate(resolveTarget(input.Target, cwd, projectRoot))
	command := lowerTrim(input.Command)
	commandText := normalizedPathText(input.Command)
	content := lowerTrim(input.ContentPreview)
	toolName := lowerTrim(input.ToolName)
	actionType := lowerTrim(input.ActionType)
	method := strings.ToUpper(strings.TrimSpace(input.NetworkMethod))
	networkURL := strings.TrimSpace(input.NetworkURL)
	host, scheme := parseNetworkURL(networkURL)
	workspaceRoot := firstNonEmpty(projectRoot, cwd)

	if d, ok := detectSensitiveRead(command, commandText, toolName, actionType, target, workspaceRoot); ok {
		return d
	}
	if d, ok := detectDeleteRisk(command, commandText, toolName, actionType, target, workspaceRoot, cwd); ok {
		return d
	}
	if d, ok := detectPersistentWrite(command, commandText, target); ok {
		return d
	}
	if d, ok := detectSelfTamper(target, commandText, command, toolName, actionType); ok {
		return d
	}
	if d, ok := detectSensitiveWrite(target, commandText, command, content, actionType, toolName); ok {
		return d
	}
	if d, ok := detectDownloadExecute(command, content); ok {
		return d
	}
	if d, ok := detectNetworkWrite(method, host, scheme, target, content); ok {
		return d
	}
	if d, ok := detectNetworkRead(method, host, scheme, networkURL); ok {
		return d
	}
	if d, ok := detectLowRiskAllow(command, toolName, actionType, target, workspaceRoot); ok {
		return d
	}

	return newDecision("ask", "medium", false, "需要确认", "unknown_action", "unknown", "需要人工确认")
}

func detectLowRiskAllow(command, toolName, actionType, target, workspaceRoot string) (Decision, bool) {
	if isSafeCommand(command) {
		return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读"), true
	}
	if isReadLike(actionType, toolName, command) && target != "" && isWithinWorkspace(target, workspaceRoot) {
		if !isSensitivePath(target) {
			return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读"), true
		}
	}
	if isWriteLike(actionType, toolName, command) && target != "" && isWithinWorkspace(target, workspaceRoot) &&
		!isSensitivePath(target) && !isSelfTamperPath(target) && !isSensitiveConfigPath(target) {
		return newDecision("allow", "low", true, "常规工作区修改", "workspace_write", "unknown", "工作区修改"), true
	}
	return Decision{}, false
}

func detectSensitiveRead(command, commandText, toolName, actionType, target, workspaceRoot string) (Decision, bool) {
	if !isReadLike(actionType, toolName, command) || target == "" {
		if commandText == "" {
			return Decision{}, false
		}
		if isSensitivePathText(commandText) {
			return newDecision("deny", "high", false, "命中敏感读取", "sensitive_read", "sensitive_read", "凭据或隐私路径"), true
		}
		return Decision{}, false
	}
	if isSensitiveCredentialPath(target) || isBrowserProfilePath(target) || isSecretLikePath(target) {
		return newDecision("deny", "high", false, "命中敏感读取", "sensitive_read", "sensitive_read", "凭据或隐私路径"), true
	}
	if !isWithinWorkspace(target, workspaceRoot) {
		return newDecision("ask", "medium", false, "工作区外读取需确认", "external_read", "unknown", "工作区外读取"), true
	}
	return Decision{}, false
}

func detectDeleteRisk(command, commandText, toolName, actionType, target, workspaceRoot, cwd string) (Decision, bool) {
	if !isDeleteLike(actionType, toolName, command) {
		return Decision{}, false
	}
	if target == "" {
		if isRootDeleteCommand(commandText, workspaceRoot) {
			return newDecision("deny", "critical", false, "命中根目录删除", "root_delete", "destructive_write", "删除根目录或父目录"), true
		}
		return newDecision("ask", "medium", false, "删除需要确认", "delete_action", "destructive_write", "删除操作"), true
	}
	normalizedRoot := firstNonEmpty(workspaceRoot, cwd)
	if normalizedRoot != "" && (samePath(target, normalizedRoot) || isAncestorOrSamePath(target, normalizedRoot)) {
		return newDecision("deny", "critical", false, "命中根目录删除", "root_delete", "destructive_write", "删除根目录或父目录"), true
	}
	if isRecursiveDelete(command) {
		return newDecision("ask", "high", false, "删除需要确认", "recursive_delete", "destructive_write", "递归删除"), true
	}
	return newDecision("ask", "medium", false, "删除需要确认", "delete_action", "destructive_write", "删除操作"), true
}

func detectPersistentWrite(command, commandText, target string) (Decision, bool) {
	if command == "" && target == "" {
		return Decision{}, false
	}
	if containsAny(command, "reg add", "reg delete") && containsAny(command, `\run`, `/run`, `runonce`, `currentversion`, `services\`, `/services/`) {
		return newDecision("deny", "critical", false, "命中注册表持久化", "registry_persistence", "persistence", "注册表持久化"), true
	}
	if containsAny(commandText, "reg add", "reg delete") && containsAny(commandText, `/run`, `runonce`, `currentversion`, `services/`) {
		return newDecision("deny", "critical", false, "命中注册表持久化", "registry_persistence", "persistence", "注册表持久化"), true
	}
	if containsAny(command, "schtasks /create", "new-scheduledtask") {
		return newDecision("deny", "critical", false, "命中计划任务持久化", "scheduled_task_persistence", "persistence", "计划任务持久化"), true
	}
	if containsAny(commandText, "schtasks /create", "new-scheduledtask") {
		return newDecision("deny", "critical", false, "命中计划任务持久化", "scheduled_task_persistence", "persistence", "计划任务持久化"), true
	}
	if isStartupPath(target) {
		return newDecision("deny", "critical", false, "命中自启动持久化", "startup_persistence", "persistence", "自启动路径"), true
	}
	if isStartupText(commandText) && containsAny(commandText, "write", "set-content", "out-file", "add-content", "new-item", "copy-item", "move-item") {
		return newDecision("deny", "critical", false, "命中自启动持久化", "startup_persistence", "persistence", "自启动路径"), true
	}
	return Decision{}, false
}

func detectSelfTamper(target, commandText, command, toolName, actionType string) (Decision, bool) {
	if target == "" {
		if isHardSelfTamperText(commandText) {
			return newDecision("deny", "critical", false, "命中自维护篡改", "agent_self_tamper", "agent_self_tamper", "hooks 或启动篡改"), true
		}
		if isSoftSelfTamperText(commandText) {
			return newDecision("ask", "high", false, "需要确认", "agent_self_tamper", "agent_self_tamper", "代理配置或工作流文件"), true
		}
		return Decision{}, false
	}
	if isHardSelfTamperPath(target) {
		return newDecision("deny", "critical", false, "命中自维护篡改", "agent_self_tamper", "agent_self_tamper", "hooks 或启动篡改"), true
	}
	if isSoftSelfTamperPath(target) {
		return newDecision("ask", "high", false, "需要确认", "agent_self_tamper", "agent_self_tamper", "代理配置或工作流文件"), true
	}
	if isWriteLike(actionType, toolName, command) && isSensitiveConfigPath(target) {
		return newDecision("ask", "medium", false, "需要确认", "config_write", "agent_self_tamper", "敏感配置写入"), true
	}
	return Decision{}, false
}

func detectSensitiveWrite(target, commandText, command, content, actionType, toolName string) (Decision, bool) {
	if target == "" || !isWriteLike(actionType, toolName, content) {
		if target == "" && isSensitiveWriteText(commandText) {
			return newDecision("ask", "medium", false, "需要确认", "config_write", "unknown", "敏感文件写入"), true
		}
		return Decision{}, false
	}
	if isHardSensitiveWritePath(target) {
		return newDecision("deny", "critical", false, "命中敏感写入", "sensitive_write", "sensitive_read", "凭据目录写入"), true
	}
	if isSensitiveConfigPath(target) {
		return newDecision("ask", "medium", false, "需要确认", "config_write", "unknown", "敏感文件写入"), true
	}
	return Decision{}, false
}

func detectDownloadExecute(command, content string) (Decision, bool) {
	if command == "" && content == "" {
		return Decision{}, false
	}
	if containsAny(command, "curl | iex", "curl|iex", "iwr | powershell", "iwr|powershell", "invoke-expression", "iex ", " | iex", "executionpolicy bypass", " -enc ", " -encodedcommand ") ||
		containsAny(content, "invoke-expression", "iex ", "iwr | powershell", "executionpolicy bypass", "windowstyle hidden", "-encodedcommand", "-enc ") {
		return newDecision("deny", "critical", false, "命中下载执行", "download_execute", "unknown", "下载后直接执行"), true
	}
	return Decision{}, false
}

func detectNetworkWrite(method, host, scheme, target, content string) (Decision, bool) {
	if method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
		return Decision{}, false
	}
	if target != "" && isSensitivePayloadTarget(target) && (isUnknownHost(host) || isWebhookOrPasteHost(host)) {
		return newDecision("deny", "high", false, "命中敏感外传", "network_exfil", "network_exfil", "敏感内容上传"), true
	}
	if containsSensitiveContent(content) && (isUnknownHost(host) || isWebhookOrPasteHost(host)) {
		return newDecision("deny", "high", false, "命中敏感外传", "network_exfil", "network_exfil", "敏感内容上传"), true
	}
	if isUnknownHost(host) || host == "" {
		return newDecision("ask", "high", false, "网络写入需要确认", "unknown_network_write", "network_exfil", "未知域名写入"), true
	}
	return newDecision("ask", "high", false, "网络写入需要确认", "network_write", "network_exfil", "网络写入"), true
}

func detectNetworkRead(method, host, scheme, networkURL string) (Decision, bool) {
	if method != "GET" && method != "HEAD" {
		return Decision{}, false
	}
	if networkURL != "" && !isHTTPURLScheme(scheme) {
		return newDecision("ask", "medium", true, "网络访问需要确认", "unknown_network_read", "network_read", "非 HTTP(S) 访问"), true
	}
	if isHTTPSSafeHost(host) {
		return newDecision("allow", "low", true, "常规网络读取", "network_read", "network_read", "网络只读"), true
	}
	return newDecision("ask", "medium", false, "网络访问需要确认", "unknown_network_read", "network_read", "未知域名读取"), true
}

func isSafeCommand(command string) bool {
	if command == "" {
		return false
	}
	if hasShellChaining(command) || containsAny(command, "curl", "wget", "iwr", "invoke-webrequest", "invoke-restmethod", "powershell", "pwsh", "cmd /c") {
		return false
	}
	return strings.HasPrefix(command, "rg ") ||
		strings.HasPrefix(command, "grep ") ||
		strings.HasPrefix(command, "select-string") ||
		strings.HasPrefix(command, "git status") ||
		strings.HasPrefix(command, "git diff") ||
		strings.HasPrefix(command, "git log") ||
		(strings.HasPrefix(command, "go test") && strings.Contains(command, "./...")) ||
		(strings.HasPrefix(command, "go vet") && strings.Contains(command, "./...")) ||
		strings.HasPrefix(command, "npm run check") ||
		strings.HasPrefix(command, "npm run build")
}

func hasShellChaining(command string) bool {
	return containsAny(command, "&&", "||", ";", "|", "`", "$(", ">", "<")
}

func isReadLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "read", "inspect", "view", "list", "get") || toolName == "read" || isSafeCommand(command)
}

func isWriteLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "write", "create", "update", "patch", "post") || containsAny(toolName, "write", "edit", "apply_patch") || containsAny(command, "set-content", "out-file", "add-content")
}

func isDeleteLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "delete", "remove") || containsAny(toolName, "delete") || containsAny(command, "rm -rf", "rmdir /s", "del /s", "remove-item -recurse", "remove-item -path", "unlink", "trash")
}

func isRecursiveDelete(command string) bool {
	return containsAny(command, "rm -rf", "rmdir /s", "remove-item -recurse", "remove-item -path")
}

func isSensitivePath(target string) bool {
	return isSensitiveCredentialPath(target) || isBrowserProfilePath(target) || isSecretLikePath(target) || isSensitiveConfigPath(target) || isHardSelfTamperPath(target) || isHardSensitiveWritePath(target)
}

func isSensitivePathText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/.ssh/", "/.aws/", "/.azure/", "/.kube/", "/id_rsa", "/id_ed25519", "/.pem", "/.pfx", "/.key", "/.crt", "/.cer", "/.der", "/.p12", "/.p7b", "/.p7c", "/.token", "/.secret", "/.password", "/.npmrc", "/.env", "/.env.local") ||
		containsAny(normalized, "/cookies", "/login data", "/web data", "/history") ||
		containsAny(normalized, "/.git/hooks/", "/.claude/", "/.codex/", "/.agents/", "/.github/workflows/", "/appdata/roaming/microsoft/windows/start menu/programs/startup", "/documents/powershell/", "/documents/windowspowershell/")
}

func isSensitiveWriteText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/.env", "/.env.local", "/.npmrc", "/.claude/", "/.codex/", "/.agents/", "/.github/workflows/") ||
		strings.HasSuffix(normalized, "/package.json") || strings.HasSuffix(normalized, "/package-lock.json") ||
		strings.HasSuffix(normalized, "/pnpm-lock.yaml") || strings.HasSuffix(normalized, "/yarn.lock") ||
		strings.HasSuffix(normalized, "/bun.lockb")
}

func isHardSelfTamperText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/.git/hooks/", "/appdata/roaming/microsoft/windows/start menu/programs/startup", "/documents/powershell/", "/documents/windowspowershell/", "/microsoft.powershell_profile.ps1")
}

func isSoftSelfTamperText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/.claude/", "/.codex/", "/.agents/", "/.github/workflows/") ||
		strings.HasSuffix(normalized, "/package.json") || strings.HasSuffix(normalized, "/package-lock.json") ||
		strings.HasSuffix(normalized, "/pnpm-lock.yaml") || strings.HasSuffix(normalized, "/yarn.lock") ||
		strings.HasSuffix(normalized, "/bun.lockb") || strings.HasSuffix(normalized, "/agents.md")
}

func isStartupText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/appdata/roaming/microsoft/windows/start menu/programs/startup", "/documents/powershell/", "/documents/windowspowershell/")
}

func isRootDeleteCommand(commandText, workspaceRoot string) bool {
	if commandText == "" {
		return false
	}
	normalized := normalizedPathText(commandText)
	if containsAny(normalized, "remove-item -recurse .", "remove-item -recurse ./", "remove-item -recurse ../", "rmdir /s .", "rmdir /s ./", "rm -rf .", "rm -rf ./", "del /s .", "del /q /s .") {
		return true
	}
	normalizedRoot := normalizedPathText(workspaceRoot)
	return normalizedRoot != "" && strings.Contains(normalized, normalizedRoot)
}

func isSelfTamperPath(target string) bool {
	return isHardSelfTamperPath(target) || isSoftSelfTamperPath(target)
}

func isSensitiveCredentialPath(target string) bool {
	segments := pathSegments(target)
	return hasSequence(segments, pathSegments(".ssh")) ||
		hasSequence(segments, pathSegments(".aws")) ||
		hasSequence(segments, pathSegments(".azure")) ||
		hasSequence(segments, pathSegments(".kube"))
}

func isBrowserProfilePath(target string) bool {
	segments := pathSegments(target)
	if hasSequence(segments, pathSegments("appdata/local/google/chrome/user data")) ||
		hasSequence(segments, pathSegments("appdata/local/microsoft/edge/user data")) ||
		hasSequence(segments, pathSegments("appdata/roaming/mozilla/firefox/profiles")) {
		return true
	}
	return containsAny(target, "cookies", "login data", "web data", "history")
}

func isSecretLikePath(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, "/id_rsa", "/id_ed25519", ".pem", ".pfx", ".key", ".crt", ".cer", ".der", ".p12", ".p7b", ".p7c", ".asc", ".token", ".secret", ".password", ".npmrc", ".env", ".env.local")
}

func isSensitiveConfigPath(target string) bool {
	lower := lowerTrim(target)
	if lower == "" {
		return false
	}
	if containsAny(lower, "/.env", "/.env.local", "/.npmrc") {
		return true
	}
	if strings.HasSuffix(lower, "/package.json") || strings.HasSuffix(lower, "/package-lock.json") || strings.HasSuffix(lower, "/pnpm-lock.yaml") || strings.HasSuffix(lower, "/yarn.lock") || strings.HasSuffix(lower, "/bun.lockb") {
		return true
	}
	if containsAny(lower, "/.claude/", "/.codex/", "/.agents/") || strings.HasSuffix(lower, "/agents.md") {
		return true
	}
	return containsAny(lower, "/.github/workflows/")
}

func isHardSelfTamperPath(target string) bool {
	lower := lowerTrim(target)
	if containsAny(lower, "/.git/hooks/") || strings.HasSuffix(lower, "/.git/hooks") {
		return true
	}
	return containsAny(lower, "/appdata/roaming/microsoft/windows/start menu/programs/startup") || containsAny(lower, "/documents/powershell/") || containsAny(lower, "/documents/windowspowershell/") || strings.HasSuffix(lower, "/microsoft.powershell_profile.ps1")
}

func isSoftSelfTamperPath(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, "/.claude/", "/.codex/", "/.agents/", "/.github/workflows/") ||
		strings.HasSuffix(lower, "/package.json") ||
		strings.HasSuffix(lower, "/package-lock.json") ||
		strings.HasSuffix(lower, "/pnpm-lock.yaml") ||
		strings.HasSuffix(lower, "/yarn.lock") ||
		strings.HasSuffix(lower, "/bun.lockb") ||
		strings.HasSuffix(lower, "/agents.md")
}

func isHardSensitiveWritePath(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, "/.ssh/", "/.aws/", "/.azure/", "/.kube/", "/appdata/local/google/chrome/user data/", "/appdata/local/microsoft/edge/user data/", "/appdata/roaming/mozilla/firefox/profiles/") || containsAny(lower, "/cookies", "/login data", "/web data", "/history")
}

func isStartupPath(target string) bool {
	segments := pathSegments(target)
	return hasSequence(segments, pathSegments("appdata/roaming/microsoft/windows/start menu/programs/startup")) || hasSequence(segments, pathSegments("documents/powershell")) || hasSequence(segments, pathSegments("documents/windowspowershell"))
}

func isSensitivePayloadTarget(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, ".env", ".env.local", ".npmrc", "id_rsa", "id_ed25519", ".pem", ".pfx", ".key", ".crt", ".cer", ".der", ".p12", ".p7b", ".p7c", ".zip", ".tar", ".tgz", ".rar", ".7z")
}

func containsSensitiveContent(content string) bool {
	return containsAny(content, "password", "secret", "token", "api_key", "access_key", "private key", "authorization", "cookie", "-----begin")
}

func isHTTPURLScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

func isHTTPSSafeHost(host string) bool {
	lower := lowerTrim(host)
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "docs.") {
		return true
	}
	safeHosts := []string{
		"github.com",
		"raw.githubusercontent.com",
		"api.github.com",
		"npmjs.com",
		"registry.npmjs.org",
		"pkg.go.dev",
		"go.dev",
		"golang.org",
		"developer.mozilla.org",
		"learn.microsoft.com",
		"docs.microsoft.com",
	}
	for _, safe := range safeHosts {
		if lower == safe || strings.HasSuffix(lower, "."+safe) {
			return true
		}
	}
	return false
}

func isUnknownHost(host string) bool {
	return !isHTTPSSafeHost(host)
}

func isWebhookOrPasteHost(host string) bool {
	lower := lowerTrim(host)
	return containsAny(lower, "paste", "pastebin", "webhook", "hooks.slack.com", "discord.com/api/webhooks")
}

func parseNetworkURL(raw string) (host, scheme string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", ""
	}
	return lowerTrim(parsed.Hostname()), lowerTrim(parsed.Scheme)
}

func newDecision(decision, riskLevel string, silent bool, reason string, signal string, category string, moreSignals ...string) Decision {
	out := Decision{
		Decision:  lowerTrim(decision),
		RiskLevel: lowerTrim(riskLevel),
		Silent:    silent,
		Reason:    strings.TrimSpace(reason),
		Category:  strings.TrimSpace(category),
	}
	seen := map[string]struct{}{}
	for _, one := range append([]string{signal}, moreSignals...) {
		one = strings.TrimSpace(one)
		if one == "" {
			continue
		}
		if _, ok := seen[one]; ok {
			continue
		}
		seen[one] = struct{}{}
		out.Signals = append(out.Signals, one)
	}
	return out
}

func lowerTrim(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func containsAny(value string, needles ...string) bool {
	lower := strings.ToLower(value)
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle != "" && strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func normalizePathCandidate(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, `\`, `/`)
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "//?/unc/"):
		value = "//" + value[len("//?/unc/"):]
	case strings.HasPrefix(lower, "//?/"):
		value = value[len("//?/"):]
	}
	cleaned := path.Clean(strings.ReplaceAll(value, `\`, `/`))
	return trimPathSegments(strings.ToLower(cleaned))
}

func normalizedPathText(raw string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), `\`, `/`))
}

func trimPathSegments(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(raw, "//") {
		prefix = "//"
		raw = strings.TrimPrefix(raw, "//")
	}
	parts := strings.Split(raw, "/")
	for i, part := range parts {
		parts[i] = strings.TrimRight(strings.TrimSpace(part), " .")
	}
	return prefix + strings.Join(parts, "/")
}

func resolveTarget(target, cwd, projectRoot string) string {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return ""
	}
	if isAbsolutePathLike(trimmed) {
		return trimmed
	}
	base := firstNonEmpty(cwd, projectRoot)
	if base == "" {
		return trimmed
	}
	return path.Join(base, trimmed)
}

func isAbsolutePathLike(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		return true
	}
	return len(value) >= 2 && value[1] == ':'
}

func pathSegments(raw string) []string {
	normalized := normalizePathCandidate(raw)
	if normalized == "" {
		return nil
	}
	parts := strings.Split(normalized, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func hasSequence(segments, sequence []string) bool {
	if len(segments) == 0 || len(sequence) == 0 || len(segments) < len(sequence) {
		return false
	}
	for start := 0; start <= len(segments)-len(sequence); start++ {
		ok := true
		for i := range sequence {
			if segments[start+i] != sequence[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	return normalizePathCandidate(left) == normalizePathCandidate(right)
}

func isAncestorOrSamePath(candidate, root string) bool {
	candidateSegs := pathSegments(candidate)
	rootSegs := pathSegments(root)
	if len(candidateSegs) == 0 || len(rootSegs) == 0 || len(candidateSegs) > len(rootSegs) {
		return false
	}
	for i := range candidateSegs {
		if candidateSegs[i] != rootSegs[i] {
			return false
		}
	}
	return true
}

func isWithinWorkspace(target, workspaceRoot string) bool {
	targetSegs := pathSegments(target)
	rootSegs := pathSegments(workspaceRoot)
	if len(targetSegs) == 0 || len(rootSegs) == 0 || len(targetSegs) < len(rootSegs) {
		return false
	}
	for i := range rootSegs {
		if targetSegs[i] != rootSegs[i] {
			return false
		}
	}
	return true
}

func ReadInput(pathValue string) (ActionInput, error) {
	trimmed := strings.TrimSpace(pathValue)
	if trimmed == "" {
		return ActionInput{}, errors.New("输入路径不能为空")
	}
	var data []byte
	var err error
	if trimmed == "-" {
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			return ActionInput{}, fmt.Errorf("读取标准输入失败")
		}
	} else {
		data, err = os.ReadFile(trimmed)
		if err != nil {
			return ActionInput{}, fmt.Errorf("读取输入文件失败")
		}
	}
	var input ActionInput
	if err := json.Unmarshal(data, &input); err != nil {
		return ActionInput{}, fmt.Errorf("输入 JSON 无效")
	}
	return input, nil
}

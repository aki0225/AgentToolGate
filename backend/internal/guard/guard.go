package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"unicode"
)

type ActionInput struct {
	Client         string   `json:"client"`
	ToolName       string   `json:"toolName"`
	ActionType     string   `json:"actionType"`
	CWD            string   `json:"cwd"`
	ProjectRoot    string   `json:"projectRoot"`
	Command        string   `json:"command"`
	Target         string   `json:"target"`
	ContentPreview string   `json:"contentPreview"`
	NetworkMethod  string   `json:"networkMethod"`
	NetworkURL     string   `json:"networkUrl"`
	Targets        []string `json:"targets,omitempty"`
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
	targets := make([]string, 0, len(input.Targets)+1)
	targets = appendGuardTarget(targets, input.Target)
	for _, target := range input.Targets {
		targets = appendGuardTarget(targets, target)
	}
	if len(targets) > 0 {
		result := Decision{}
		for _, target := range targets {
			candidate := input
			candidate.Target = target
			candidate.Targets = nil
			result = stricterGuardDecision(result, evaluateAction(candidate))
		}
		if result.Decision != "" {
			return result
		}
	}
	return evaluateAction(input)
}

func appendGuardTarget(targets []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return targets
	}
	for _, existing := range targets {
		if existing == target {
			return targets
		}
	}
	return append(targets, target)
}

func evaluateAction(input ActionInput) Decision {
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

	if isSafeCommandWithinWorkspace(input.Command, cwd, projectRoot) {
		return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读")
	}
	if target != "" &&
		isReadLike(actionType, toolName, command) &&
		isAllowedWorkspaceMetadataReadPath(target, workspaceRoot) {
		return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读")
	}
	if d, ok := detectSensitiveRead(command, commandText, toolName, actionType, target, workspaceRoot); ok {
		return d
	}
	if d, ok := detectSelfTamper(input, target, commandText, command, toolName, actionType); ok {
		return d
	}
	if d, ok := detectDeleteRisk(command, commandText, toolName, actionType, target, workspaceRoot, cwd); ok {
		return d
	}
	if d, ok := detectPersistentWrite(command, commandText, target); ok {
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
	if d, ok := detectLowRiskAllow(input.Command, toolName, actionType, target, cwd, projectRoot, workspaceRoot); ok {
		return d
	}

	return newDecision("ask", "medium", false, "需要确认", "unknown_action", "unknown", "需要人工确认")
}

func stricterGuardDecision(base, candidate Decision) Decision {
	baseDecisionRank := guardDecisionRank(base.Decision)
	candidateDecisionRank := guardDecisionRank(candidate.Decision)
	if candidateDecisionRank > baseDecisionRank {
		return candidate
	}
	if candidateDecisionRank == baseDecisionRank && guardRiskRank(candidate.RiskLevel) >= guardRiskRank(base.RiskLevel) {
		return candidate
	}
	return base
}

func guardDecisionRank(decision string) int {
	switch lowerTrim(decision) {
	case "deny":
		return 3
	case "ask":
		return 2
	case "allow":
		return 1
	default:
		return 0
	}
}

func guardRiskRank(riskLevel string) int {
	switch lowerTrim(riskLevel) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func detectLowRiskAllow(command, toolName, actionType, target, cwd, projectRoot, workspaceRoot string) (Decision, bool) {
	if isSafeCommandWithinWorkspace(command, cwd, projectRoot) {
		return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读"), true
	}
	normalizedCommand := lowerTrim(command)
	if isReadLike(actionType, toolName, normalizedCommand) && target != "" && isWithinWorkspace(target, workspaceRoot) {
		if !isSensitiveReadPath(target, workspaceRoot) {
			return newDecision("allow", "low", true, "常规只读开发动作", "dev_readonly_command", "dev_readonly", "工作区只读"), true
		}
	}
	if isWriteLike(actionType, toolName, normalizedCommand) && target != "" && isWithinWorkspace(target, workspaceRoot) &&
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
	if isSensitiveReadPath(target, workspaceRoot) {
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
		if isRecursiveDelete(commandText) {
			return newDecision("ask", "high", false, "删除需要确认", "recursive_delete", "destructive_write", "递归删除"), true
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

func detectSelfTamper(input ActionInput, target, commandText, command, toolName, actionType string) (Decision, bool) {
	if targetsAgentToolGateControlDirectoryMutation(input, target, command, toolName, actionType) {
		return newDecision("deny", "critical", false, "命中自维护篡改", "agent_self_tamper", "agent_self_tamper", "项目保护配置篡改"), true
	}
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

func isSelfTamperMutationLike(actionType, toolName, command string) bool {
	return isWriteLike(actionType, toolName, command) || isDeleteLike(actionType, toolName, command) ||
		containsAny(actionType, "move", "rename") || containsAny(command, "mv ", "move ", "move-item", "ren ", "rename-item")
}

func isAgentToolGateControlDirectoryPath(target string) bool {
	segments := pathSegments(strings.TrimSuffix(normalizedPathText(target), "/"))
	return hasSequence(segments, pathSegments(".agenttoolgate")) ||
		hasSequence(segments, pathSegments(".tmp/agenttoolgate"))
}

func targetsAgentToolGateControlDirectoryMutation(input ActionInput, target, command, toolName, actionType string) bool {
	if isSelfTamperMutationLike(actionType, toolName, command) && isAgentToolGateControlDirectoryPath(target) {
		return true
	}
	for _, operation := range projectProtectionTargetOperations(input) {
		if operation.Operation != "write" && operation.Operation != "delete" {
			continue
		}
		resolved := normalizePathCandidate(resolveTarget(operation.Target, input.CWD, input.ProjectRoot))
		if isAgentToolGateControlDirectoryPath(operation.Target) || isAgentToolGateControlDirectoryPath(resolved) {
			return true
		}
	}
	return false
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
	if isCredentialConfigWritePath(target) {
		return newDecision("ask", "medium", false, "需要确认", "credential_config_write", "sensitive_config"), true
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
	return IsExplicitReadOnlyCommand(command)
}

func isSafeCommandWithinWorkspace(command, cwd, workspaceRoot string) bool {
	context := readOnlyCommandContext{
		cwd:              normalizePathCandidate(cwd),
		workspaceRoot:    normalizePathCandidate(workspaceRoot),
		enforceWorkspace: true,
	}
	if !context.isValid() {
		return false
	}
	return isExplicitReadOnlyCommand(command, context)
}

type readOnlyCommandContext struct {
	cwd              string
	workspaceRoot    string
	enforceWorkspace bool
}

func (context readOnlyCommandContext) isValid() bool {
	if !context.enforceWorkspace {
		return true
	}
	return context.cwd != "" &&
		context.workspaceRoot != "" &&
		isWithinWorkspace(context.cwd, context.workspaceRoot) &&
		!isSensitivePath(context.cwd)
}

func (context readOnlyCommandContext) isSafePath(token string, allowGlob bool) bool {
	if !isSafeReadCommandTokenSyntax(token, allowGlob, context.enforceWorkspace) {
		return false
	}
	if !context.enforceWorkspace {
		return !isSensitivePath(normalizePathCandidate(path.Join("/workspace", token)))
	}
	resolved := normalizePathCandidate(resolveTarget(token, context.cwd, context.workspaceRoot))
	return resolved != "" &&
		isWithinWorkspace(resolved, context.workspaceRoot) &&
		!isSensitiveReadPath(resolved, context.workspaceRoot)
}

func (context readOnlyCommandContext) isSafePowerShellPath(token string, allowGlob bool) bool {
	return !strings.Contains(token, ",") && context.isSafePath(token, allowGlob)
}

func (context readOnlyCommandContext) isSafeGitArgument(token string) bool {
	if !isSafeGitReadArgument(token) {
		return false
	}
	if !context.enforceWorkspace {
		return !isSensitivePath(normalizePathCandidate(path.Join("/workspace", token)))
	}
	resolved := normalizePathCandidate(resolveTarget(token, context.cwd, context.workspaceRoot))
	return resolved != "" &&
		isWithinWorkspace(resolved, context.workspaceRoot) &&
		!isSensitiveReadPath(resolved, context.workspaceRoot)
}

// IsExplicitReadOnlyCommand 仅识别不会执行项目代码或写入文件的命令。
func IsExplicitReadOnlyCommand(command string) bool {
	return isExplicitReadOnlyCommand(command, readOnlyCommandContext{})
}

func isExplicitReadOnlyCommand(command string, context readOnlyCommandContext) bool {
	trimmed := strings.TrimSpace(command)
	normalized := strings.ToLower(trimmed)
	if normalized == "" {
		return false
	}
	if hasShellChaining(trimmed) {
		return false
	}
	switch {
	case hasCommandName(normalized, "rg"):
		return isExplicitReadOnlySearchCommand(trimmed, "rg", context)
	case hasCommandName(normalized, "grep"):
		return isExplicitReadOnlySearchCommand(trimmed, "grep", context)
	case hasCommandName(normalized, "select-string"):
		return isExplicitReadOnlySelectStringCommand(trimmed, context)
	case hasCommandName(normalized, "git status"):
		return isExplicitReadOnlyGitCommand(trimmed, "status", context)
	case hasCommandName(normalized, "git diff"), hasCommandName(normalized, "git log"), hasCommandName(normalized, "git show"):
		return isExplicitReadOnlyGitCommand(trimmed, strings.Fields(normalized)[1], context)
	case hasCommandName(normalized, "git rev-parse"):
		return isExplicitReadOnlyGitCommand(trimmed, "rev-parse", context)
	case normalized == "pwd", normalized == "get-location":
		return true
	case hasCommandName(normalized, "sed"):
		return isExplicitReadOnlySedCommand(trimmed, context)
	case hasCommandName(normalized, "get-childitem"):
		return isExplicitReadOnlyPathCommand(trimmed, context, getChildItemParameterSpecs, true, true, false)
	case hasCommandName(normalized, "ls"), hasCommandName(normalized, "dir"):
		return isExplicitReadOnlyPathCommand(trimmed, context, getChildItemParameterSpecs, true, true, true)
	case hasCommandName(normalized, "get-content"), hasCommandName(normalized, "gc"):
		return isExplicitReadOnlyPathCommand(trimmed, context, getContentParameterSpecs, false, false, false)
	case hasCommandName(normalized, "cat"), hasCommandName(normalized, "type"):
		return isExplicitReadOnlyPathCommand(trimmed, context, getContentParameterSpecs, false, false, true)
	default:
		return false
	}
}

func hasCommandName(command, name string) bool {
	return command == name || strings.HasPrefix(command, name+" ")
}

type searchOptionValueKind int

const (
	searchOptionNoValue searchOptionValueKind = iota
	searchOptionValue
	searchOptionPattern
	searchOptionDirectoryMode
	searchOptionDeviceMode
)

type parsedSearchShortOptions struct {
	patternProvided bool
	valueKind       searchOptionValueKind
	value           string
	needsValue      bool
}

func isExplicitReadOnlySearchCommand(command, name string, context readOnlyCommandContext) bool {
	tokens, ok := splitReadOnlyCommandTokens(command)
	if !ok || len(tokens) < 2 || !strings.EqualFold(tokens[0], name) {
		return false
	}
	patternProvided := false
	filesMode := false
	endOfOptions := false
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if endOfOptions {
			if !patternProvided && !filesMode {
				patternProvided = true
				continue
			}
			if !context.isSafePath(token, true) {
				return false
			}
			continue
		}
		if token == "--" {
			endOfOptions = true
			continue
		}
		if strings.HasPrefix(token, "-") && token != "-" {
			if strings.HasPrefix(token, "--") {
				option, inlineValue, hasInlineValue := splitReadOnlyOption(token)
				if isForbiddenSearchLongOption(name, option) {
					return false
				}
				if option == "--files" && name == "rg" {
					if hasInlineValue {
						return false
					}
					filesMode = true
					continue
				}
				valueKind, consumesValue := searchLongOptionValueKind(name, option)
				if !consumesValue {
					continue
				}
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if value == "" || !isSafeSearchOptionValue(name, valueKind, value) {
					return false
				}
				if valueKind == searchOptionPattern {
					patternProvided = true
				}
				continue
			}

			parsed, ok := parseSearchShortOptions(name, token)
			if !ok {
				return false
			}
			if parsed.needsValue {
				index++
				if index >= len(tokens) {
					return false
				}
				parsed.value = tokens[index]
			}
			if parsed.value == "" && parsed.valueKind != searchOptionNoValue {
				return false
			}
			if !isSafeSearchOptionValue(name, parsed.valueKind, parsed.value) {
				return false
			}
			if parsed.patternProvided || parsed.valueKind == searchOptionPattern {
				patternProvided = true
			}
			continue
		}
		if !patternProvided && !filesMode {
			patternProvided = true
			continue
		}
		if !context.isSafePath(token, true) {
			return false
		}
	}
	return patternProvided || filesMode
}

func parseSearchShortOptions(name, token string) (parsedSearchShortOptions, bool) {
	if len(token) < 2 || token[0] != '-' || token[1] == '-' {
		return parsedSearchShortOptions{}, false
	}
	var result parsedSearchShortOptions
	for index := 1; index < len(token); index++ {
		valueKind, forbidden, known := searchShortOptionKind(name, token[index])
		if forbidden || !known {
			return parsedSearchShortOptions{}, false
		}
		if valueKind == searchOptionNoValue {
			continue
		}
		result.valueKind = valueKind
		if index+1 < len(token) {
			result.value = token[index+1:]
		} else {
			result.needsValue = true
		}
		if valueKind == searchOptionPattern && result.value != "" {
			result.patternProvided = true
		}
		return result, true
	}
	return result, true
}

func searchShortOptionKind(name string, option byte) (searchOptionValueKind, bool, bool) {
	switch name {
	case "rg":
		switch option {
		case 'f', 'L', 'u':
			return searchOptionNoValue, true, true
		case 'e':
			return searchOptionPattern, false, true
		case 'A', 'B', 'C', 'd', 'E', 'g', 'j', 'm', 'M', 'r', 't', 'T':
			return searchOptionValue, false, true
		default:
			return searchOptionNoValue, false, strings.ContainsRune("0abcFhHIilNnopPqsSUVvVwxz", rune(option))
		}
	case "grep":
		switch option {
		case 'f', 'r', 'R':
			return searchOptionNoValue, true, true
		case 'e':
			return searchOptionPattern, false, true
		case 'd':
			return searchOptionDirectoryMode, false, true
		case 'D':
			return searchOptionDeviceMode, false, true
		case 'A', 'B', 'C', 'm':
			return searchOptionValue, false, true
		default:
			return searchOptionNoValue, false, strings.ContainsRune("abcEFGHIJLlnoPpqSsTUVvWwxyzZ", rune(option))
		}
	default:
		return searchOptionNoValue, false, false
	}
}

func isForbiddenSearchLongOption(name, option string) bool {
	switch name {
	case "rg":
		if strings.HasPrefix(option, "--no-ignore") {
			return true
		}
		switch option {
		case "--pre", "--file", "--ignore-file", "--hostname-bin",
			"--hidden", "--unrestricted", "--follow":
			return true
		}
	case "grep":
		switch option {
		case "--file", "--exclude-from", "--include-from",
			"--recursive", "--dereference-recursive":
			return true
		}
	}
	return false
}

func splitReadOnlyOption(token string) (string, string, bool) {
	if index := strings.Index(token, "="); index > 0 {
		name := token[:index]
		if strings.HasPrefix(name, "--") {
			name = strings.ToLower(name)
		}
		return name, token[index+1:], true
	}
	if strings.HasPrefix(token, "--") {
		return strings.ToLower(token), "", false
	}
	return token, "", false
}

func searchLongOptionValueKind(name, option string) (searchOptionValueKind, bool) {
	if option == "--regexp" {
		return searchOptionPattern, true
	}
	switch name {
	case "rg":
		switch option {
		case "--glob", "--iglob", "--type", "--type-not", "--encoding", "--engine",
			"--sort", "--sortr", "--color", "--colors", "--max-count", "--max-depth",
			"--context", "--before-context", "--after-context", "--replace", "--threads",
			"--max-columns", "--max-filesize", "--path-separator", "--pre-glob",
			"--type-add", "--type-clear", "--dfa-size-limit", "--regex-size-limit",
			"--hyperlink-format", "--field-context-separator", "--field-match-separator",
			"--context-separator":
			return searchOptionValue, true
		}
	case "grep":
		switch option {
		case "--directories":
			return searchOptionDirectoryMode, true
		case "--devices":
			return searchOptionDeviceMode, true
		case "--binary-files", "--label", "--max-count", "--after-context",
			"--before-context", "--context", "--color", "--include", "--exclude",
			"--exclude-dir":
			return searchOptionValue, true
		}
	}
	return searchOptionNoValue, false
}

func isSafeSearchOptionValue(name string, valueKind searchOptionValueKind, value string) bool {
	if valueKind == searchOptionNoValue {
		return true
	}
	switch valueKind {
	case searchOptionDirectoryMode:
		return name != "grep" || !strings.EqualFold(strings.TrimSpace(value), "recurse")
	case searchOptionDeviceMode:
		return name != "grep" || !strings.EqualFold(strings.TrimSpace(value), "read")
	default:
		return true
	}
}

type powerShellParameterKind int

const (
	powerShellSwitch powerShellParameterKind = iota
	powerShellValue
	powerShellPath
	powerShellPattern
	powerShellForbidden
)

type powerShellParameterSpec struct {
	name string
	kind powerShellParameterKind
}

type powerShellParameterResolution int

const (
	powerShellParameterUnknown powerShellParameterResolution = iota
	powerShellParameterAmbiguous
	powerShellParameterResolved
)

var selectStringParameterSpecs = []powerShellParameterSpec{
	{name: "allmatches", kind: powerShellSwitch},
	{name: "casesensitive", kind: powerShellSwitch},
	{name: "context", kind: powerShellValue},
	{name: "culture", kind: powerShellValue},
	{name: "debug", kind: powerShellSwitch},
	{name: "encoding", kind: powerShellValue},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "exclude", kind: powerShellValue},
	{name: "include", kind: powerShellValue},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "inputobject", kind: powerShellForbidden},
	{name: "list", kind: powerShellSwitch},
	{name: "literalpath", kind: powerShellPath},
	{name: "noemphasis", kind: powerShellSwitch},
	{name: "notmatch", kind: powerShellSwitch},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "path", kind: powerShellPath},
	{name: "pattern", kind: powerShellPattern},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "quiet", kind: powerShellSwitch},
	{name: "raw", kind: powerShellSwitch},
	{name: "simplematch", kind: powerShellSwitch},
	{name: "verbose", kind: powerShellSwitch},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
}

var getContentParameterSpecs = []powerShellParameterSpec{
	{name: "asbytestream", kind: powerShellSwitch},
	{name: "credential", kind: powerShellForbidden},
	{name: "debug", kind: powerShellSwitch},
	{name: "delimiter", kind: powerShellValue},
	{name: "encoding", kind: powerShellValue},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "exclude", kind: powerShellValue},
	{name: "filter", kind: powerShellValue},
	{name: "force", kind: powerShellSwitch},
	{name: "include", kind: powerShellValue},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "literalpath", kind: powerShellPath},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "path", kind: powerShellPath},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "raw", kind: powerShellSwitch},
	{name: "readcount", kind: powerShellValue},
	{name: "stream", kind: powerShellValue},
	{name: "tail", kind: powerShellValue},
	{name: "totalcount", kind: powerShellValue},
	{name: "verbose", kind: powerShellSwitch},
	{name: "wait", kind: powerShellForbidden},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
}

var getChildItemParameterSpecs = []powerShellParameterSpec{
	{name: "attributes", kind: powerShellValue},
	{name: "debug", kind: powerShellSwitch},
	{name: "depth", kind: powerShellValue},
	{name: "directory", kind: powerShellSwitch},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "exclude", kind: powerShellValue},
	{name: "file", kind: powerShellSwitch},
	{name: "filter", kind: powerShellValue},
	{name: "followsymlink", kind: powerShellForbidden},
	{name: "force", kind: powerShellSwitch},
	{name: "hidden", kind: powerShellSwitch},
	{name: "include", kind: powerShellValue},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "literalpath", kind: powerShellPath},
	{name: "name", kind: powerShellSwitch},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "path", kind: powerShellPath},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "readonly", kind: powerShellSwitch},
	{name: "recurse", kind: powerShellSwitch},
	{name: "system", kind: powerShellSwitch},
	{name: "verbose", kind: powerShellSwitch},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
}

func isExplicitReadOnlySelectStringCommand(command string, context readOnlyCommandContext) bool {
	tokens, ok := splitReadOnlyCommandTokens(command)
	if !ok || len(tokens) < 2 || !strings.EqualFold(tokens[0], "select-string") {
		return false
	}
	patternProvided := false
	targetCount := 0
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if strings.HasPrefix(token, "-") {
			spec, inlineValue, hasInlineValue, resolution := resolvePowerShellParameter(token, selectStringParameterSpecs)
			if resolution != powerShellParameterResolved {
				return false
			}
			switch spec.kind {
			case powerShellForbidden:
				return false
			case powerShellSwitch:
				if hasInlineValue {
					return false
				}
			case powerShellPath:
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if !context.isSafePowerShellPath(value, true) {
					return false
				}
				targetCount++
			case powerShellPattern:
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if value == "" {
					return false
				}
				patternProvided = true
			case powerShellValue:
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if value == "" {
					return false
				}
			}
			continue
		}
		if !patternProvided {
			patternProvided = true
			continue
		}
		if !context.isSafePowerShellPath(token, true) {
			return false
		}
		targetCount++
	}
	return patternProvided && targetCount > 0
}

func resolvePowerShellParameter(token string, specs []powerShellParameterSpec) (powerShellParameterSpec, string, bool, powerShellParameterResolution) {
	if len(token) < 2 || token[0] != '-' || strings.HasPrefix(token, "--") {
		return powerShellParameterSpec{}, "", false, powerShellParameterUnknown
	}
	name := token[1:]
	inlineValue := ""
	hasInlineValue := false
	if index := strings.Index(name, ":"); index >= 0 {
		inlineValue = name[index+1:]
		name = name[:index]
		hasInlineValue = true
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return powerShellParameterSpec{}, inlineValue, hasInlineValue, powerShellParameterUnknown
	}
	for _, spec := range specs {
		if spec.name == name {
			return spec, inlineValue, hasInlineValue, powerShellParameterResolved
		}
	}
	var matched powerShellParameterSpec
	matchCount := 0
	for _, spec := range specs {
		if strings.HasPrefix(spec.name, name) {
			matched = spec
			matchCount++
		}
	}
	switch matchCount {
	case 0:
		return powerShellParameterSpec{}, inlineValue, hasInlineValue, powerShellParameterUnknown
	case 1:
		return matched, inlineValue, hasInlineValue, powerShellParameterResolved
	default:
		return powerShellParameterSpec{}, inlineValue, hasInlineValue, powerShellParameterAmbiguous
	}
}

func isExplicitReadOnlyGitCommand(command, subcommand string, context readOnlyCommandContext) bool {
	tokens, ok := splitReadOnlyCommandTokens(command)
	if !ok || len(tokens) < 2 || !strings.EqualFold(tokens[0], "git") || !strings.EqualFold(tokens[1], subcommand) {
		return false
	}
	pathsOnly := false
	for index := 2; index < len(tokens); index++ {
		token := tokens[index]
		if token == "--" {
			pathsOnly = true
			continue
		}
		if pathsOnly {
			if !context.isSafePath(token, true) {
				return false
			}
			continue
		}
		if token == "-O" {
			index++
			if index >= len(tokens) || !context.isSafePath(tokens[index], false) {
				return false
			}
			continue
		}
		if strings.HasPrefix(token, "-O") && len(token) > 2 {
			if !context.isSafePath(token[2:], false) {
				return false
			}
			continue
		}
		if isForbiddenGitReadOption(token) {
			return false
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		if !context.isSafeGitArgument(token) {
			return false
		}
	}
	return true
}

func isForbiddenGitReadOption(token string) bool {
	lower := strings.ToLower(token)
	for _, option := range []string{
		"--output",
		"--ext-diff",
		"--textconv",
		"--no-index",
		"--pathspec-from-file",
		"--git-dir",
		"--work-tree",
		"--config-env",
	} {
		if lower == option || strings.HasPrefix(lower, option+"=") {
			return true
		}
	}
	return false
}

func isSafeGitReadArgument(token string) bool {
	value := strings.TrimSpace(token)
	if value == "" {
		return false
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "~") {
		return false
	}
	if len(normalized) >= 2 && normalized[1] == ':' &&
		((normalized[0] >= 'a' && normalized[0] <= 'z') || (normalized[0] >= 'A' && normalized[0] <= 'Z')) {
		return false
	}
	if index := strings.Index(normalized, ":"); index > 0 {
		switch strings.ToLower(normalized[:index]) {
		case "alias", "cert", "env", "function", "hkcu", "hklm", "registry", "variable", "wsman":
			return false
		}
	}
	cleaned := path.Clean(normalized)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func isExplicitReadOnlySedCommand(command string, context readOnlyCommandContext) bool {
	tokens, ok := splitReadOnlyCommandTokens(command)
	if !ok || len(tokens) < 3 || !strings.EqualFold(tokens[0], "sed") {
		return false
	}
	quiet := false
	endOfOptions := false
	scriptCount := 0
	targetCount := 0
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if !endOfOptions && token == "--" {
			endOfOptions = true
			continue
		}
		if !endOfOptions {
			switch token {
			case "-n", "--quiet", "--silent":
				quiet = true
				continue
			case "-e", "--expression":
				index++
				if index >= len(tokens) || !isSedPrintRange(tokens[index]) {
					return false
				}
				scriptCount++
				continue
			}
			if strings.HasPrefix(token, "-e") && len(token) > 2 && token[1] != '-' {
				if !isSedPrintRange(token[2:]) {
					return false
				}
				scriptCount++
				continue
			}
			if strings.HasPrefix(token, "--expression=") {
				if !isSedPrintRange(strings.TrimPrefix(token, "--expression=")) {
					return false
				}
				scriptCount++
				continue
			}
			if strings.HasPrefix(token, "-") {
				return false
			}
		}
		if scriptCount == 0 {
			if !isSedPrintRange(token) {
				return false
			}
			scriptCount++
			continue
		}
		if !context.isSafePath(token, true) {
			return false
		}
		targetCount++
	}
	return quiet && scriptCount > 0 && targetCount > 0
}

func isExplicitReadOnlyPathCommand(
	command string,
	context readOnlyCommandContext,
	specs []powerShellParameterSpec,
	allowNoTarget bool,
	allowGlob bool,
	allowNativeOptions bool,
) bool {
	tokens, ok := splitReadOnlyCommandTokens(command)
	if !ok || len(tokens) == 0 {
		return false
	}
	targetCount := 0
	endOfOptions := false
	for index := 1; index < len(tokens); index++ {
		token := tokens[index]
		if !endOfOptions && token == "--" {
			endOfOptions = true
			continue
		}
		if !endOfOptions && strings.HasPrefix(token, "-") {
			spec, inlineValue, hasInlineValue, resolution := resolvePowerShellParameter(token, specs)
			if resolution != powerShellParameterResolved {
				if allowNativeOptions && !hasInlineValue {
					continue
				}
				return false
			}
			switch spec.kind {
			case powerShellForbidden:
				return false
			case powerShellSwitch:
				if hasInlineValue {
					return false
				}
			case powerShellPath:
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if !context.isSafePowerShellPath(value, allowGlob) {
					return false
				}
				targetCount++
			case powerShellValue, powerShellPattern:
				value := inlineValue
				if !hasInlineValue {
					index++
					if index >= len(tokens) {
						return false
					}
					value = tokens[index]
				}
				if value == "" {
					return false
				}
			}
			continue
		}
		if !context.isSafePowerShellPath(token, allowGlob) {
			return false
		}
		targetCount++
	}
	return allowNoTarget || targetCount > 0
}

func isSafeReadCommandTokenSyntax(token string, allowGlob, allowParent bool) bool {
	value := strings.TrimSpace(token)
	if value == "" || value == "--" {
		return false
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	if strings.HasPrefix(normalized, "/") ||
		strings.HasPrefix(normalized, "~") ||
		strings.Contains(normalized, ":") ||
		strings.ContainsAny(normalized, "$%@{}()`") ||
		(!allowGlob && strings.ContainsAny(normalized, "*?[]")) {
		return false
	}
	cleaned := path.Clean(normalized)
	return allowParent || (cleaned != ".." && !strings.HasPrefix(cleaned, "../"))
}

func isSedPrintRange(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasSuffix(value, "p") {
		return false
	}
	value = strings.TrimSuffix(value, "p")
	parts := strings.Split(value, ",")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func splitReadOnlyCommandTokens(command string) ([]string, bool) {
	var tokens []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}
	for _, char := range command {
		if quote != 0 {
			if char == quote {
				quote = 0
				continue
			}
			current.WriteRune(char)
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case ' ', '\t':
			flush()
		default:
			current.WriteRune(char)
		}
	}
	if quote != 0 {
		return nil, false
	}
	flush()
	return tokens, true
}

func hasShellChaining(command string) bool {
	chars := []rune(command)
	var quote rune
	for index, char := range chars {
		if char == '\'' || char == '"' {
			if (quote == 0 || quote == char) &&
				hasOddBackslashPrefix(chars, index) &&
				!(quote == '\'' && char == '\'') {
				return true
			}
			if quote == 0 {
				quote = char
				continue
			}
			if quote == char {
				quote = 0
				continue
			}
		}
		if quote == '\'' {
			continue
		}
		if char == '`' || (char == '$' && isShellExpansionStart(chars, index, quote)) {
			return true
		}
		if quote == 0 && strings.ContainsRune("(){}", char) {
			return true
		}
		if quote == 0 && strings.ContainsRune(";&|><\r\n", char) {
			return true
		}
	}
	return quote != 0
}

func hasOddBackslashPrefix(chars []rune, index int) bool {
	count := 0
	for index--; index >= 0 && chars[index] == '\\'; index-- {
		count++
	}
	return count%2 == 1
}

func isShellExpansionStart(chars []rune, index int, quote rune) bool {
	if index+1 >= len(chars) {
		return false
	}
	next := chars[index+1]
	if quote != 0 && next == quote {
		return false
	}
	return unicode.IsLetter(next) ||
		unicode.IsDigit(next) ||
		next == '_' ||
		strings.ContainsRune("({['\"?*$@#!^-", next)
}

func isReadLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "read", "inspect", "view", "list", "get") || toolName == "read" || isSafeCommand(command)
}

func isWriteLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "write", "create", "update", "patch", "post") || containsAny(toolName, "write", "edit", "apply_patch") || containsAny(command, "set-content", "out-file", "add-content")
}

func isDeleteLike(actionType, toolName, command string) bool {
	return containsAny(actionType, "delete", "remove") ||
		containsAny(toolName, "delete") ||
		containsAny(command, "rm -rf", "rmdir /s", "del /s", "remove-item -recurse", "remove-item -path", "remove-item -literalpath", "unlink", "trash") ||
		isProjectDeleteCommand(command)
}

func isRecursiveDelete(command string) bool {
	if containsAny(command, "rm -rf", "rmdir /s", "remove-item -recurse") {
		return true
	}
	for _, tokens := range splitProjectCommandSegments(command) {
		removeItem := false
		recursive := false
		for _, token := range tokens {
			normalized := strings.ToLower(strings.TrimSpace(token))
			if projectCommandName(token) == "remove-item" {
				removeItem = true
			}
			if normalized == "-recurse" || strings.HasPrefix(normalized, "-recurse:") {
				recursive = true
			}
		}
		if removeItem && recursive {
			return true
		}
	}
	return false
}

func isSensitivePath(target string) bool {
	return isSensitiveCredentialPath(target) || isBrowserProfilePath(target) || isSecretLikePath(target) || isSensitiveConfigPath(target) || isHardSelfTamperPath(target) || isHardSensitiveWritePath(target)
}

func isSensitiveReadPath(target, workspaceRoot string) bool {
	return isSensitivePath(target) && !isAllowedWorkspaceMetadataReadPath(target, workspaceRoot)
}

func isAllowedWorkspaceMetadataReadPath(target, workspaceRoot string) bool {
	if target == "" || workspaceRoot == "" || !isWithinWorkspace(target, workspaceRoot) {
		return false
	}
	lower := lowerTrim(target)
	segments := pathSegments(lower)
	if len(segments) == 0 {
		return false
	}
	switch segments[len(segments)-1] {
	case "agents.md", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb":
		return true
	}
	return hasSequence(segments, pathSegments(".claude")) ||
		hasSequence(segments, pathSegments(".codex")) ||
		hasSequence(segments, pathSegments(".agents")) ||
		hasSequence(segments, pathSegments(".github/workflows"))
}

func isSensitivePathText(text string) bool {
	normalized := normalizedPathText(text)
	return containsAny(normalized, "/.ssh/", "/.aws/", "/.azure/", "/.kube/", "/id_rsa", "/id_ed25519", "/.pem", "/.pfx", "/.key", "/.crt", "/.cer", "/.der", "/.p12", "/.p7b", "/.p7c", "/.token", "/.secret", "/.password", "/.npmrc", "/.env", "/.env.local") ||
		isBrowserProfilePath(normalized) ||
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
	return containsAny(normalized, "/.git/hooks/", ".agenttoolgate/config.json", ".tmp/agenttoolgate/hook-control.json", "/appdata/roaming/microsoft/windows/start menu/programs/startup", "/documents/powershell/", "/documents/windowspowershell/", "/microsoft.powershell_profile.ps1")
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
	for _, operation := range projectCommandTargetOperations(commandText) {
		if operation.Operation != "delete" {
			continue
		}
		target := normalizedPathText(operation.Target)
		if target == "." || target == "./" || target == `.\` ||
			target == ".." || target == "../" || target == `..\` {
			return true
		}
		if normalizedRoot == "" {
			continue
		}
		resolved := normalizePathCandidate(resolveTarget(operation.Target, workspaceRoot, workspaceRoot))
		root := normalizePathCandidate(workspaceRoot)
		if resolved != "" && root != "" &&
			(samePath(resolved, root) || isAncestorOrSamePath(resolved, root)) {
			return true
		}
	}
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
	return hasSequence(segments, pathSegments("appdata/local/google/chrome/user data")) ||
		hasSequence(segments, pathSegments("appdata/local/microsoft/edge/user data")) ||
		hasSequence(segments, pathSegments("appdata/local/bravesoftware/brave-browser/user data")) ||
		hasSequence(segments, pathSegments("appdata/local/vivaldi/user data")) ||
		hasSequence(segments, pathSegments("appdata/local/chromium/user data")) ||
		hasSequence(segments, pathSegments("appdata/roaming/opera software/opera stable")) ||
		hasSequence(segments, pathSegments("appdata/roaming/opera software/opera gx stable")) ||
		hasSequence(segments, pathSegments("appdata/roaming/mozilla/firefox/profiles")) ||
		hasSequence(segments, pathSegments(".config/google-chrome")) ||
		hasSequence(segments, pathSegments(".config/chromium")) ||
		hasSequence(segments, pathSegments(".config/bravesoftware/brave-browser")) ||
		hasSequence(segments, pathSegments(".config/vivaldi")) ||
		hasSequence(segments, pathSegments(".mozilla/firefox")) ||
		hasSequence(segments, pathSegments("library/application support/google/chrome")) ||
		hasSequence(segments, pathSegments("library/application support/microsoft edge")) ||
		hasSequence(segments, pathSegments("library/application support/bravesoftware/brave-browser")) ||
		hasSequence(segments, pathSegments("library/application support/vivaldi"))
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
	if strings.HasSuffix(lower, "/.agenttoolgate/protected.json") {
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

func isCredentialConfigWritePath(target string) bool {
	segments := pathSegments(target)
	return len(segments) > 0 && strings.EqualFold(segments[len(segments)-1], "credentials.json")
}

func isHardSelfTamperPath(target string) bool {
	lower := lowerTrim(target)
	if containsAny(lower, "/.git/hooks/") || strings.HasSuffix(lower, "/.git/hooks") ||
		strings.HasSuffix(lower, "/.agenttoolgate/config.json") || strings.HasSuffix(lower, "/.tmp/agenttoolgate/hook-control.json") {
		return true
	}
	return containsAny(lower, "/appdata/roaming/microsoft/windows/start menu/programs/startup") || containsAny(lower, "/documents/powershell/") || containsAny(lower, "/documents/windowspowershell/") || strings.HasSuffix(lower, "/microsoft.powershell_profile.ps1")
}

func isSoftSelfTamperPath(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, "/.claude/", "/.codex/", "/.agents/", "/.github/workflows/") ||
		strings.HasSuffix(lower, "/.agenttoolgate/protected.json") ||
		strings.HasSuffix(lower, "/package.json") ||
		strings.HasSuffix(lower, "/package-lock.json") ||
		strings.HasSuffix(lower, "/pnpm-lock.yaml") ||
		strings.HasSuffix(lower, "/yarn.lock") ||
		strings.HasSuffix(lower, "/bun.lockb") ||
		strings.HasSuffix(lower, "/agents.md")
}

func isHardSensitiveWritePath(target string) bool {
	lower := lowerTrim(target)
	return containsAny(lower, "/.ssh/", "/.aws/", "/.azure/", "/.kube/") || isBrowserProfilePath(lower)
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
	return normalizePathCandidateForOS(raw, runtime.GOOS)
}

func normalizePathCandidateForOS(raw, goos string) string {
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
	cleaned = trimPathSegments(cleaned)
	if goos == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
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

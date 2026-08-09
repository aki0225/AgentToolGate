package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	projectProtectionVersion = 1
	maxProjectProtectionSize = 64 << 10
)

type ProjectProtection struct {
	Enabled        bool
	ProtectedPaths []ProtectedPathRule
	Egress         EgressRule
}

type ProtectedPathRule struct {
	Pattern string
	Read    string
	Write   string
	Delete  string
	Exec    string
	Reason  string
}

type EgressRule struct {
	Enabled       bool
	AllowedHosts  []string
	UnlistedWrite string
}

type projectTargetOperation struct {
	Target    string
	Operation string
}

type projectProtectionDocument struct {
	Version             int                        `json:"version"`
	ProjectRoot         string                     `json:"projectRoot,omitempty"`
	Workspace           json.RawMessage            `json:"workspace,omitempty"`
	LocalActionFirewall projectLocalActionFirewall `json:"localActionFirewall"`
}

type projectLocalActionFirewall struct {
	Enabled        bool                    `json:"enabled"`
	DefaultMode    string                  `json:"defaultMode,omitempty"`
	ProtectedPaths []protectedPathRuleJSON `json:"protectedPaths,omitempty"`
	Egress         egressRuleJSON          `json:"egress,omitempty"`
	Notes          []string                `json:"notes,omitempty"`
}

type protectedPathRuleJSON struct {
	Pattern string `json:"pattern"`
	Read    string `json:"read,omitempty"`
	Write   string `json:"write,omitempty"`
	Delete  string `json:"delete,omitempty"`
	Exec    string `json:"exec,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type egressRuleJSON struct {
	Enabled       bool     `json:"enabled"`
	AllowedHosts  []string `json:"allowedHosts,omitempty"`
	UnlistedWrite string   `json:"unlistedWrite,omitempty"`
}

func ProjectProtectionPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".agenttoolgate", "protected.json")
}

func LoadProjectProtection(repoRoot string) (ProjectProtection, error) {
	configPath := ProjectProtectionPath(repoRoot)
	info, err := os.Lstat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectProtection{}, nil
		}
		return ProjectProtection{}, errors.New("读取项目保护策略失败")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ProjectProtection{}, errors.New("项目保护策略必须是普通文件")
	}
	if info.Size() > maxProjectProtectionSize {
		return ProjectProtection{}, errors.New("项目保护策略文件过大")
	}
	if !projectProtectionConfigWithinRoot(repoRoot, configPath) {
		return ProjectProtection{}, errors.New("项目保护策略路径不可信")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return ProjectProtection{}, errors.New("读取项目保护策略失败")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document projectProtectionDocument
	if err := decoder.Decode(&document); err != nil {
		return ProjectProtection{}, errors.New("项目保护策略 JSON 无效")
	}
	if err := ensureProjectProtectionEOF(decoder); err != nil {
		return ProjectProtection{}, err
	}
	if document.Version != projectProtectionVersion {
		return ProjectProtection{}, fmt.Errorf("项目保护策略 version 必须为 %d", projectProtectionVersion)
	}

	protection := ProjectProtection{
		Enabled: document.LocalActionFirewall.Enabled,
		Egress: EgressRule{
			Enabled:       document.LocalActionFirewall.Egress.Enabled,
			UnlistedWrite: normalizeProjectEffect(document.LocalActionFirewall.Egress.UnlistedWrite),
		},
	}
	if len(document.LocalActionFirewall.ProtectedPaths) > 128 {
		return ProjectProtection{}, errors.New("项目保护路径规则不能超过 128 条")
	}
	for _, rawRule := range document.LocalActionFirewall.ProtectedPaths {
		rule, err := normalizeProtectedPathRule(rawRule)
		if err != nil {
			return ProjectProtection{}, err
		}
		protection.ProtectedPaths = append(protection.ProtectedPaths, rule)
	}
	if len(document.LocalActionFirewall.Egress.AllowedHosts) > 128 {
		return ProjectProtection{}, errors.New("项目外发允许主机不能超过 128 条")
	}
	for _, rawHost := range document.LocalActionFirewall.Egress.AllowedHosts {
		host, err := normalizeAllowedHost(rawHost)
		if err != nil {
			return ProjectProtection{}, err
		}
		protection.Egress.AllowedHosts = append(protection.Egress.AllowedHosts, host)
	}
	if protection.Egress.Enabled && protection.Egress.UnlistedWrite == "" {
		protection.Egress.UnlistedWrite = "require_approval"
	}
	if protection.Egress.UnlistedWrite != "" && !validProjectEffect(protection.Egress.UnlistedWrite) {
		return ProjectProtection{}, errors.New("egress.unlistedWrite 只支持 require_approval 或 deny")
	}
	return protection, nil
}

func EvaluateWithProjectProtection(input ActionInput, protection ProjectProtection) Decision {
	base := Evaluate(input)
	floor, matched := EvaluateProjectProtection(input, protection)
	if !matched {
		return base
	}
	return stricterGuardDecision(base, floor)
}

func EvaluateProjectProtection(input ActionInput, protection ProjectProtection) (Decision, bool) {
	if !protection.Enabled {
		return Decision{}, false
	}

	var result Decision
	matched := false
	for _, targetOperation := range projectProtectionTargetOperations(input) {
		relative, ok := protectedRepoRelativePath(input, targetOperation.Target)
		if !ok {
			continue
		}
		for _, rule := range protection.ProtectedPaths {
			if !projectPathPatternMatches(rule.Pattern, relative) {
				continue
			}
			effect := protectedPathEffect(rule, targetOperation.Operation)
			if effect == "" {
				continue
			}
			candidate := projectProtectionDecision(
				effect,
				firstNonEmpty(rule.Reason, "命中项目受保护路径"),
				"project_protected_path",
			)
			if !matched {
				result = candidate
				matched = true
			} else {
				result = stricterGuardDecision(result, candidate)
			}
		}
	}

	if protection.Egress.Enabled && isProjectNetworkWrite(input) {
		host := projectNetworkHost(input.NetworkURL)
		if host == "" || !projectHostAllowed(host, protection.Egress.AllowedHosts) {
			candidate := projectProtectionDecision(
				protection.Egress.UnlistedWrite,
				"项目外发规则要求确认",
				"project_egress",
			)
			if !matched {
				result = candidate
				matched = true
			} else {
				result = stricterGuardDecision(result, candidate)
			}
		}
	}
	return result, matched
}

func ensureProjectProtectionEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("项目保护策略 JSON 只能包含一个文档")
	}
	return nil
}

func normalizeProtectedPathRule(raw protectedPathRuleJSON) (ProtectedPathRule, error) {
	rawPattern := strings.TrimSpace(raw.Pattern)
	if rawPattern == "" || len(rawPattern) > 256 || !validProjectPattern(rawPattern) {
		return ProtectedPathRule{}, errors.New("protectedPaths.pattern 必须是仓库内相对路径 pattern")
	}
	pattern := normalizeProjectPattern(rawPattern)
	rule := ProtectedPathRule{
		Pattern: pattern,
		Read:    normalizeProjectEffect(raw.Read),
		Write:   normalizeProjectEffect(raw.Write),
		Delete:  normalizeProjectEffect(raw.Delete),
		Exec:    normalizeProjectEffect(raw.Exec),
		Reason:  trimProjectReason(raw.Reason),
	}
	for _, effect := range []string{rule.Read, rule.Write, rule.Delete, rule.Exec} {
		if effect != "" && !validProjectEffect(effect) {
			return ProtectedPathRule{}, errors.New("受保护路径动作只支持 require_approval 或 deny")
		}
	}
	if rule.Read == "" && rule.Write == "" && rule.Delete == "" && rule.Exec == "" {
		return ProtectedPathRule{}, errors.New("受保护路径规则至少配置一个动作")
	}
	return rule, nil
}

func normalizeProjectEffect(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validProjectEffect(value string) bool {
	return value == "require_approval" || value == "deny"
}

func normalizeProjectPattern(value string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
}

func validProjectPattern(pattern string) bool {
	raw := strings.TrimSpace(pattern)
	if raw == "" || path.IsAbs(raw) || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" || hasWindowsVolumePrefix(raw) {
		return false
	}
	if strings.HasPrefix(raw, `\\`) || strings.HasPrefix(strings.ReplaceAll(raw, "\\", "/"), "//") {
		return false
	}
	normalized := normalizeProjectPattern(raw)
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return false
		}
	}
	if strings.ContainsAny(strings.TrimSuffix(normalized, "/**"), "*?[") {
		return false
	}
	return !strings.Contains(normalized, "**") || strings.HasSuffix(normalized, "/**")
}

func hasWindowsVolumePrefix(value string) bool {
	return len(value) >= 2 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':'
}

func normalizeAllowedHost(value string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(value))
	if host == "" || host == "*" || strings.ContainsAny(host, "/?#@") {
		return "", errors.New("egress.allowedHosts 必须是具体 host 或 *.domain")
	}
	if strings.HasPrefix(host, "*.") {
		if strings.Count(host, "*") != 1 || len(strings.TrimPrefix(host, "*.")) < 3 {
			return "", errors.New("egress.allowedHosts wildcard 无效")
		}
		return host, nil
	}
	if strings.Contains(host, "*") {
		return "", errors.New("egress.allowedHosts wildcard 只允许前缀 *.")
	}
	return host, nil
}

func trimProjectReason(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r < 0x20 {
			builder.WriteRune(' ')
		} else {
			builder.WriteRune(r)
		}
		if builder.Len() >= 160 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func CanonicalPatchTargets(input ActionInput) []string {
	targets := make([]string, 0, len(input.Targets)+2)
	for _, target := range input.Targets {
		targets = appendProjectTarget(targets, target)
	}
	if isExplicitProjectReadTool(lowerTrim(input.ToolName)) {
		return targets
	}
	for _, content := range []string{input.ContentPreview, input.Command} {
		if containsAny(lowerTrim(input.ToolName), "apply_patch", "applypatch") || strings.Contains(content, "*** Begin Patch") {
			for _, target := range extractPatchTargets(content) {
				targets = appendProjectTarget(targets, target)
			}
		}
	}
	return targets
}

func CanonicalProjectTargets(input ActionInput) []string {
	targets := CanonicalPatchTargets(input)
	if isProjectShellInput(input) {
		for _, content := range []string{input.Command, input.ContentPreview} {
			for _, operation := range projectCommandTargetOperations(content) {
				targets = appendProjectTarget(targets, operation.Target)
			}
		}
	}
	return targets
}

func appendProjectTarget(targets []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return targets
	}
	for _, existing := range targets {
		if existing == target || (runtime.GOOS == "windows" && strings.EqualFold(existing, target)) {
			return targets
		}
	}
	return append(targets, target)
}

func projectProtectionTargets(input ActionInput) []string {
	targets := CanonicalProjectTargets(input)
	target := strings.TrimSpace(input.Target)
	if target == "" {
		return targets
	}
	for _, existing := range targets {
		if existing == target || (runtime.GOOS == "windows" && strings.EqualFold(existing, target)) {
			return targets
		}
	}
	return append(targets, target)
}

func projectProtectionTargetOperations(input ActionInput) []projectTargetOperation {
	if isProjectPatchInput(input) && !isExplicitProjectReadTool(lowerTrim(input.ToolName)) {
		var operations []projectTargetOperation
		for _, content := range []string{input.ContentPreview, input.Command} {
			for _, operation := range extractPatchTargetOperations(content) {
				operations = appendProjectTargetOperation(operations, operation)
			}
		}
		for _, target := range projectProtectionTargets(input) {
			if projectTargetHasOperation(operations, target) {
				continue
			}
			operations = appendProjectTargetOperation(operations, projectTargetOperation{
				Target:    target,
				Operation: "write",
			})
		}
		if len(operations) > 0 {
			return operations
		}
	}

	var operations []projectTargetOperation
	if isProjectShellInput(input) {
		for _, content := range []string{input.Command, input.ContentPreview} {
			for _, operation := range projectCommandTargetOperations(content) {
				operations = appendProjectTargetOperation(operations, operation)
			}
		}
	}

	operation := projectProtectionOperation(input)
	if operation == "" {
		return operations
	}
	for _, target := range projectProtectionTargets(input) {
		if projectTargetHasOperation(operations, target) {
			continue
		}
		operations = appendProjectTargetOperation(operations, projectTargetOperation{
			Target:    target,
			Operation: operation,
		})
	}
	return operations
}

func extractPatchTargetOperations(patch string) []projectTargetOperation {
	prefixOperations := []struct {
		prefix    string
		operation string
	}{
		{prefix: "*** Add File: ", operation: "write"},
		{prefix: "*** Update File: ", operation: "write"},
		{prefix: "*** Move to: ", operation: "write"},
		{prefix: "*** Delete File: ", operation: "delete"},
	}
	var operations []projectTargetOperation
	for _, rawLine := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		for _, candidate := range prefixOperations {
			if !strings.HasPrefix(line, candidate.prefix) {
				continue
			}
			operations = appendProjectTargetOperation(operations, projectTargetOperation{
				Target:    strings.TrimSpace(strings.TrimPrefix(line, candidate.prefix)),
				Operation: candidate.operation,
			})
			break
		}
	}
	return operations
}

func appendProjectTargetOperation(operations []projectTargetOperation, candidate projectTargetOperation) []projectTargetOperation {
	candidate.Target = strings.TrimSpace(candidate.Target)
	candidate.Operation = strings.TrimSpace(candidate.Operation)
	if candidate.Target == "" || candidate.Operation == "" {
		return operations
	}
	for _, existing := range operations {
		sameTarget := existing.Target == candidate.Target ||
			(runtime.GOOS == "windows" && strings.EqualFold(existing.Target, candidate.Target))
		if sameTarget && existing.Operation == candidate.Operation {
			return operations
		}
	}
	return append(operations, candidate)
}

func projectTargetHasOperation(operations []projectTargetOperation, target string) bool {
	for _, operation := range operations {
		if operation.Target == target || (runtime.GOOS == "windows" && strings.EqualFold(operation.Target, target)) {
			return true
		}
	}
	return false
}

func isProjectPatchInput(input ActionInput) bool {
	tool := lowerTrim(input.ToolName)
	return containsAny(tool, "apply_patch", "applypatch") ||
		strings.Contains(input.ContentPreview, "*** Begin Patch") ||
		strings.Contains(input.Command, "*** Begin Patch")
}

func isProjectShellInput(input ActionInput) bool {
	action := lowerTrim(input.ActionType)
	tool := lowerTrim(input.ToolName)
	return action == "command" || action == "exec" || action == "execute" ||
		tool == "shell" || tool == "powershell" || tool == "pwsh" ||
		tool == "bash" || tool == "sh" || tool == "cmd"
}

func projectProtectionOperation(input ActionInput) string {
	action := lowerTrim(input.ActionType)
	tool := lowerTrim(input.ToolName)
	command := lowerTrim(firstNonEmpty(input.Command, input.ContentPreview))
	if isProjectPatchInput(input) {
		return "write"
	}
	if tool == "write" || tool == "edit" || tool == "multiedit" || tool == "notebookedit" || containsAny(tool, "write_file", "edit_file") {
		return "write"
	}
	if isExplicitProjectReadTool(tool) {
		return "read"
	}
	if isDeleteLike(action, tool, command) || isProjectDeleteCommand(command) {
		return "delete"
	}
	if isWriteLike("", tool, command) {
		return "write"
	}
	if isReadLike("", tool, command) {
		return "read"
	}
	if containsAny(action, "write", "create", "update", "patch", "post") {
		return "write"
	}
	if containsAny(action, "read", "inspect", "view", "list", "get") {
		return "read"
	}
	if action == "command" || action == "exec" || action == "execute" || command != "" {
		return "exec"
	}
	return ""
}

func isExplicitProjectReadTool(tool string) bool {
	return tool == "read" || tool == "grep" || tool == "glob"
}

func isProjectDeleteCommand(command string) bool {
	_, matched := projectDeleteCommandTargets(command)
	return matched
}

func projectDeleteCommandTargets(command string) ([]string, bool) {
	var targets []string
	for _, operation := range projectCommandTargetOperations(command) {
		if operation.Operation != "delete" {
			continue
		}
		targets = appendProjectTarget(targets, operation.Target)
	}
	return targets, len(targets) > 0
}

func projectCommandTargetOperations(command string) []projectTargetOperation {
	var operations []projectTargetOperation
	for _, tokens := range splitProjectCommandSegments(command) {
		for _, operation := range parseProjectCommandTargetOperations(tokens, 0) {
			operations = appendProjectTargetOperation(operations, operation)
		}
	}
	return operations
}

func splitProjectCommandSegments(value string) [][]string {
	var segments [][]string
	var current []string
	var token strings.Builder
	var quote rune
	flushToken := func() {
		if token.Len() == 0 {
			return
		}
		current = append(current, token.String())
		token.Reset()
	}
	flushSegment := func() {
		flushToken()
		if len(current) == 0 {
			return
		}
		segments = append(segments, current)
		current = nil
	}
	for _, char := range value {
		if quote != 0 {
			if char == quote {
				quote = 0
				continue
			}
			token.WriteRune(char)
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case ';', '&', '|':
			flushSegment()
		case ' ', '\t', '\r', '\n':
			flushToken()
		default:
			token.WriteRune(char)
		}
	}
	flushSegment()
	return segments
}

func parseProjectCommandTargetOperations(tokens []string, depth int) []projectTargetOperation {
	if len(tokens) == 0 || depth > 2 {
		return nil
	}
	index := 0
	if strings.EqualFold(tokens[index], "sudo") {
		index++
		for index < len(tokens) {
			if tokens[index] == "--" {
				index++
				break
			}
			if !strings.HasPrefix(tokens[index], "-") {
				break
			}
			index++
		}
	}
	if index < len(tokens) && strings.EqualFold(tokens[index], "command") {
		index++
	}
	if index >= len(tokens) {
		return nil
	}
	executable := strings.TrimSuffix(strings.ToLower(tokens[index]), ".exe")
	switch executable {
	case "cmd":
		index++
		if index >= len(tokens) || (!strings.EqualFold(tokens[index], "/c") && !strings.EqualFold(tokens[index], "/k")) {
			return nil
		}
		return parseNestedProjectCommandTargetOperations(tokens[index+1:], depth)
	case "powershell", "pwsh":
		for candidate := index + 1; candidate < len(tokens); candidate++ {
			if strings.EqualFold(tokens[candidate], "-command") || strings.EqualFold(tokens[candidate], "-c") {
				return parseNestedProjectCommandTargetOperations(tokens[candidate+1:], depth)
			}
			if strings.EqualFold(tokens[candidate], "-file") && candidate+1 < len(tokens) {
				return []projectTargetOperation{{Target: tokens[candidate+1], Operation: "exec"}}
			}
		}
		return nil
	case "bash", "sh", "zsh":
		for candidate := index + 1; candidate < len(tokens); candidate++ {
			if strings.EqualFold(tokens[candidate], "-c") {
				return parseNestedProjectCommandTargetOperations(tokens[candidate+1:], depth)
			}
			if !strings.HasPrefix(tokens[candidate], "-") {
				return []projectTargetOperation{{Target: tokens[candidate], Operation: "exec"}}
			}
		}
		return nil
	case "rm", "del", "erase", "rmdir", "rd", "remove-item":
		return projectOperationsForTargets(extractProjectDeleteTargets(executable, tokens[index+1:]), "delete")
	case "get-content", "gc":
		return projectOperationsForTargets(extractProjectPowerShellPathTargets(tokens[index+1:], getContentParameterSpecs, 1), "read")
	case "cat", "type", "more", "less":
		return projectOperationsForTargets(extractProjectPositionalTargets(tokens[index+1:], nil), "read")
	case "head", "tail":
		valueOptions := map[string]struct{}{"-n": {}, "--lines": {}, "-c": {}, "--bytes": {}}
		return projectOperationsForTargets(extractProjectPositionalTargets(tokens[index+1:], valueOptions), "read")
	case "rg", "grep":
		return projectOperationsForTargets(extractProjectSearchTargets(executable, tokens[index+1:]), "read")
	case "set-content", "add-content", "out-file", "new-item":
		return projectOperationsForTargets(extractProjectPowerShellPathTargets(tokens[index+1:], nil, 1), "write")
	case "touch", "mkdir":
		return projectOperationsForTargets(extractProjectPositionalTargets(tokens[index+1:], nil), "write")
	case "copy-item", "cp", "copy":
		return projectCopyMoveOperations(tokens[index+1:], false)
	case "move-item", "rename-item", "mv", "move", "ren", "rename":
		return projectCopyMoveOperations(tokens[index+1:], true)
	default:
		return nil
	}
}

func parseNestedProjectCommandTargetOperations(tokens []string, depth int) []projectTargetOperation {
	if len(tokens) == 0 {
		return nil
	}
	var operations []projectTargetOperation
	for _, nested := range splitProjectCommandSegments(strings.Join(tokens, " ")) {
		for _, operation := range parseProjectCommandTargetOperations(nested, depth+1) {
			operations = appendProjectTargetOperation(operations, operation)
		}
	}
	return operations
}

func projectOperationsForTargets(targets []string, operation string) []projectTargetOperation {
	var operations []projectTargetOperation
	for _, target := range targets {
		operations = appendProjectTargetOperation(operations, projectTargetOperation{
			Target:    target,
			Operation: operation,
		})
	}
	return operations
}

func extractProjectPowerShellPathTargets(args []string, specs []powerShellParameterSpec, positionalLimit int) []string {
	var targets []string
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if len(specs) > 0 {
				spec, inlineValue, hasInlineValue, resolution := resolvePowerShellParameter(arg, specs)
				if resolution != powerShellParameterResolved {
					return nil
				}
				if spec.kind == powerShellPath {
					value := inlineValue
					if !hasInlineValue {
						index++
						if index >= len(args) {
							return nil
						}
						value = args[index]
					}
					targets = appendProjectArgumentTargets(targets, value)
					continue
				}
				if spec.kind != powerShellSwitch && !hasInlineValue {
					index++
					if index >= len(args) {
						return nil
					}
				}
				continue
			}
			lowerArg := strings.ToLower(arg)
			if isProjectPathParameter(lowerArg) && index+1 < len(args) {
				index++
				targets = appendProjectArgumentTargets(targets, args[index])
				continue
			}
			if hookProjectParameterConsumesValue(lowerArg) && index+1 < len(args) {
				index++
			}
			continue
		}
		targets = appendProjectArgumentTargets(targets, arg)
		if positionalLimit > 0 && len(targets) >= positionalLimit {
			break
		}
	}
	return targets
}

func isProjectPathParameter(value string) bool {
	switch value {
	case "-path", "-literalpath", "-filepath":
		return true
	default:
		return false
	}
}

func hookProjectParameterConsumesValue(value string) bool {
	switch value {
	case "-value", "-encoding", "-filter", "-include", "-exclude", "-itemtype",
		"-name", "-destination", "-dest":
		return true
	default:
		return false
	}
}

func extractProjectPositionalTargets(args []string, valueOptions map[string]struct{}) []string {
	var targets []string
	optionsEnded := false
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if arg == "--" {
			optionsEnded = true
			continue
		}
		if !optionsEnded && strings.HasPrefix(arg, "-") {
			if _, consumesValue := valueOptions[strings.ToLower(arg)]; consumesValue && index+1 < len(args) {
				index++
			}
			continue
		}
		targets = appendProjectArgumentTargets(targets, arg)
	}
	return targets
}

func extractProjectSearchTargets(name string, args []string) []string {
	var targets []string
	patternProvided := false
	filesMode := false
	endOfOptions := false
	for index := 0; index < len(args); index++ {
		token := args[index]
		if endOfOptions {
			if !patternProvided && !filesMode {
				patternProvided = true
				continue
			}
			targets = appendProjectArgumentTargets(targets, token)
			continue
		}
		if token == "--" {
			endOfOptions = true
			continue
		}
		if strings.HasPrefix(token, "-") && token != "-" {
			if strings.HasPrefix(token, "--") {
				option, _, hasInlineValue := splitReadOnlyOption(token)
				if option == "--files" && name == "rg" {
					if hasInlineValue {
						return nil
					}
					filesMode = true
					continue
				}
				valueKind, consumesValue := searchLongOptionValueKind(name, option)
				if !consumesValue {
					continue
				}
				if !hasInlineValue {
					index++
					if index >= len(args) {
						return nil
					}
				}
				if valueKind == searchOptionPattern {
					patternProvided = true
				}
				continue
			}
			parsed, ok := parseSearchShortOptions(name, token)
			if !ok {
				return nil
			}
			if parsed.needsValue {
				index++
				if index >= len(args) {
					return nil
				}
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
		targets = appendProjectArgumentTargets(targets, token)
	}
	return targets
}

func projectCopyMoveOperations(args []string, move bool) []projectTargetOperation {
	targets := extractProjectPositionalTargets(args, nil)
	if len(targets) < 2 {
		return nil
	}
	var operations []projectTargetOperation
	sourceOperation := "read"
	if move {
		sourceOperation = "delete"
	}
	for _, source := range targets[:len(targets)-1] {
		operations = appendProjectTargetOperation(operations, projectTargetOperation{
			Target:    source,
			Operation: sourceOperation,
		})
	}
	return appendProjectTargetOperation(operations, projectTargetOperation{
		Target:    targets[len(targets)-1],
		Operation: "write",
	})
}

func appendProjectArgumentTargets(targets []string, value string) []string {
	for _, candidate := range strings.Split(value, ",") {
		targets = appendProjectTarget(targets, strings.TrimSpace(candidate))
	}
	return targets
}

func extractProjectDeleteTargets(command string, args []string) []string {
	var targets []string
	optionsEnded := false
	valueOptions := map[string]struct{}{
		"-filter": {}, "-include": {}, "-exclude": {}, "-stream": {},
	}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		lowerArg := strings.ToLower(arg)
		if arg == "" {
			continue
		}
		if arg == "--" {
			optionsEnded = true
			continue
		}
		if command == "remove-item" {
			if (lowerArg == "-path" || lowerArg == "-literalpath") && index+1 < len(args) {
				index++
				targets = appendProjectArgumentTargets(targets, strings.TrimRight(args[index], ","))
				continue
			}
			if !optionsEnded && strings.HasPrefix(arg, "-") {
				if _, consumesValue := valueOptions[lowerArg]; consumesValue && index+1 < len(args) {
					index++
				}
				continue
			}
		} else if !optionsEnded {
			if command == "rm" && strings.HasPrefix(arg, "-") {
				continue
			}
			if command != "rm" && (strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "-")) {
				continue
			}
		}
		targets = appendProjectArgumentTargets(targets, strings.TrimRight(arg, ","))
	}
	return targets
}

func protectedRepoRelativePath(input ActionInput, target string) (string, bool) {
	root := normalizePathCandidate(input.ProjectRoot)
	if root == "" {
		return "", false
	}
	candidate := normalizePathCandidate(resolveTarget(target, input.CWD, root))
	if candidate == "" {
		return "", false
	}
	root = resolveProjectPolicyPath(root)
	candidate = resolveProjectPolicyPath(candidate)
	relative, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(candidate))
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return "", false
	}
	relative = filepath.ToSlash(filepath.Clean(relative))
	if relative == ".." || strings.HasPrefix(relative, "../") {
		return "", false
	}
	return relative, true
}

func resolveProjectPolicyPath(value string) string {
	native := filepath.FromSlash(value)
	if resolved, err := filepath.EvalSymlinks(native); err == nil {
		return filepath.ToSlash(resolved)
	}
	current := native
	var suffix []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.ToSlash(filepath.Clean(native))
		}
		suffix = append([]string{filepath.Base(current)}, suffix...)
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.ToSlash(filepath.Join(parts...))
		}
	}
}

func projectPathPatternMatches(pattern, relative string) bool {
	pattern = normalizeProjectPattern(pattern)
	relative = normalizeProjectPattern(relative)
	if runtime.GOOS == "windows" {
		pattern = strings.ToLower(pattern)
		relative = strings.ToLower(relative)
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return relative == prefix || strings.HasPrefix(relative, prefix+"/")
	}
	matched, err := path.Match(pattern, relative)
	return err == nil && matched
}

func projectProtectionConfigWithinRoot(repoRoot, configPath string) bool {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(configPath))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, parent)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func protectedPathEffect(rule ProtectedPathRule, operation string) string {
	switch operation {
	case "read":
		return rule.Read
	case "write":
		return rule.Write
	case "delete":
		return rule.Delete
	case "exec":
		return rule.Exec
	default:
		return ""
	}
}

func isProjectNetworkWrite(input ActionInput) bool {
	method := strings.ToUpper(strings.TrimSpace(input.NetworkMethod))
	if method != "" {
		return method != "GET" && method != "HEAD" && method != "OPTIONS"
	}
	return lowerTrim(input.ActionType) == "network" && input.NetworkURL != ""
}

func projectNetworkHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if port := parsed.Port(); host != "" && port != "" {
		return host + ":" + port
	}
	return host
}

func projectHostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	hostWithoutPort := host
	if parsed, err := url.Parse("http://" + host); err == nil && parsed.Hostname() != "" {
		hostWithoutPort = strings.ToLower(parsed.Hostname())
	}
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == host || pattern == hostWithoutPort {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			if strings.HasSuffix(hostWithoutPort, suffix) && hostWithoutPort != strings.TrimPrefix(suffix, ".") {
				return true
			}
		}
	}
	return false
}

func projectProtectionDecision(effect, reason, category string) Decision {
	decision := "ask"
	risk := "high"
	if effect == "deny" {
		decision = "deny"
	}
	return newDecision(decision, risk, false, reason, category, category, "项目保护规则")
}

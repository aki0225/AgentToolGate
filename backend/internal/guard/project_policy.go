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
	"unicode"
	"unicode/utf8"
)

const (
	projectProtectionVersion = 1
	maxProjectProtectionSize = 64 << 10
)

type ProjectProtection struct {
	Enabled        bool
	ProtectedPaths []ProtectedPathRule
	Egress         EgressRule
	ProjectRoot    string
	Workspace      ProjectWorkspace
	DefaultMode    string
}

type ProjectWorkspace struct {
	Name  string
	Slug  string
	OrgID string
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
	Version             int                         `json:"version"`
	ProjectRoot         *string                     `json:"projectRoot,omitempty"`
	Workspace           *projectWorkspaceJSON       `json:"workspace,omitempty"`
	LocalActionFirewall *projectLocalActionFirewall `json:"localActionFirewall"`
}

type projectWorkspaceJSON struct {
	Name  *string `json:"name,omitempty"`
	Slug  *string `json:"slug,omitempty"`
	OrgID *string `json:"orgId,omitempty"`
}

type projectLocalActionFirewall struct {
	Enabled        *bool                   `json:"enabled"`
	DefaultMode    *string                 `json:"defaultMode,omitempty"`
	ProtectedPaths []protectedPathRuleJSON `json:"protectedPaths,omitempty"`
	Egress         *egressRuleJSON         `json:"egress,omitempty"`
	Notes          []string                `json:"notes,omitempty"`
}

type protectedPathRuleJSON struct {
	Pattern string  `json:"pattern"`
	Read    *string `json:"read,omitempty"`
	Write   *string `json:"write,omitempty"`
	Delete  *string `json:"delete,omitempty"`
	Exec    *string `json:"exec,omitempty"`
	Reason  string  `json:"reason,omitempty"`
}

type egressRuleJSON struct {
	Enabled       *bool    `json:"enabled"`
	AllowedHosts  []string `json:"allowedHosts,omitempty"`
	UnlistedWrite *string  `json:"unlistedWrite,omitempty"`
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
	if err := rejectDuplicateProjectProtectionFields(raw); err != nil {
		return ProjectProtection{}, err
	}
	if err := validateProjectProtectionFieldNames(raw); err != nil {
		return ProjectProtection{}, err
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
	if document.LocalActionFirewall == nil {
		return ProjectProtection{}, errors.New("项目保护策略 localActionFirewall 缺失")
	}
	if document.LocalActionFirewall.Enabled == nil {
		return ProjectProtection{}, errors.New("项目保护策略 localActionFirewall.enabled 缺失")
	}
	if err := validateProjectProtectionMetadata(&document); err != nil {
		return ProjectProtection{}, err
	}

	protection := ProjectProtection{
		Enabled: *document.LocalActionFirewall.Enabled,
	}
	if document.ProjectRoot != nil {
		protection.ProjectRoot = *document.ProjectRoot
	}
	if document.LocalActionFirewall.DefaultMode != nil {
		protection.DefaultMode = *document.LocalActionFirewall.DefaultMode
	}
	if document.Workspace != nil {
		if document.Workspace.Name != nil {
			protection.Workspace.Name = *document.Workspace.Name
		}
		if document.Workspace.Slug != nil {
			protection.Workspace.Slug = *document.Workspace.Slug
		}
		if document.Workspace.OrgID != nil {
			protection.Workspace.OrgID = *document.Workspace.OrgID
		}
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
	if err := validateProjectProtectionRuleConflicts(protection.ProtectedPaths); err != nil {
		return ProjectProtection{}, err
	}
	if document.LocalActionFirewall.Egress != nil {
		if document.LocalActionFirewall.Egress.Enabled == nil {
			return ProjectProtection{}, errors.New("项目保护策略 egress.enabled 缺失")
		}
		protection.Egress.Enabled = *document.LocalActionFirewall.Egress.Enabled
		if document.LocalActionFirewall.Egress.UnlistedWrite != nil {
			protection.Egress.UnlistedWrite = *document.LocalActionFirewall.Egress.UnlistedWrite
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
		if protection.Egress.Enabled && document.LocalActionFirewall.Egress.UnlistedWrite == nil {
			return ProjectProtection{}, errors.New("启用 egress 时必须配置 unlistedWrite")
		}
		if document.LocalActionFirewall.Egress.UnlistedWrite != nil &&
			!validProjectEffect(protection.Egress.UnlistedWrite) {
			return ProjectProtection{}, errors.New("egress.unlistedWrite 只支持 require_approval 或 deny")
		}
	}
	return protection, nil
}

func validateProjectProtectionMetadata(document *projectProtectionDocument) error {
	if document == nil || document.LocalActionFirewall == nil {
		return nil
	}
	if err := validateProjectProtectionOptionalString("projectRoot", document.ProjectRoot); err != nil {
		return err
	}
	if document.Workspace != nil {
		for name, value := range map[string]*string{
			"workspace.name":  document.Workspace.Name,
			"workspace.slug":  document.Workspace.Slug,
			"workspace.orgId": document.Workspace.OrgID,
		} {
			if err := validateProjectProtectionOptionalString(name, value); err != nil {
				return err
			}
		}
	}
	if mode := document.LocalActionFirewall.DefaultMode; mode != nil {
		if *mode != "off" && *mode != "dry-run" && *mode != "live" {
			return errors.New("项目保护策略 defaultMode 只支持 off、dry-run 或 live")
		}
	}
	for _, note := range document.LocalActionFirewall.Notes {
		if utf8.RuneCountInString(note) > 512 {
			return errors.New("项目保护策略 notes 单项不能超过 512 个字符")
		}
	}
	return nil
}

func validateProjectProtectionOptionalString(name string, value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" {
		return fmt.Errorf("项目保护策略 %s 必须是非空字符串", name)
	}
	if strings.TrimSpace(*value) != *value {
		return fmt.Errorf("项目保护策略 %s 不能包含首尾空白", name)
	}
	return nil
}

func validateProjectProtectionRuleConflicts(rules []ProtectedPathRule) error {
	effects := make(map[string]map[string]string)
	for _, rule := range rules {
		key := rule.Pattern
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if effects[key] == nil {
			effects[key] = make(map[string]string)
		}
		for operation, effect := range map[string]string{
			"read": rule.Read, "write": rule.Write, "delete": rule.Delete, "exec": rule.Exec,
		} {
			if effect == "" {
				continue
			}
			if previous, exists := effects[key][operation]; exists && previous != effect {
				return fmt.Errorf("项目保护策略冲突：pattern %q 的 %s 同时配置了 %s 和 %s", rule.Pattern, operation, previous, effect)
			}
			effects[key][operation] = effect
		}
	}
	return nil
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
	result, matches := evaluateProjectProtectionMatches(input, protection)
	return result, len(matches) > 0
}

func ensureProjectProtectionEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("项目保护策略 JSON 只能包含一个文档")
	}
	return nil
}

func rejectDuplicateProjectProtectionFields(raw []byte) error {
	return validateStrictJSONDocument(raw, "项目保护策略 JSON")
}

func validateStrictJSONDocument(raw []byte, label string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkStrictJSONValue(decoder, label); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%s 只能包含一个文档", label)
	}
	return nil
}

func walkStrictJSONValue(decoder *json.Decoder, label string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s 无效", label)
	}
	if token == nil {
		return fmt.Errorf("%s 字段不能为 null", label)
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
				return fmt.Errorf("%s 无效", label)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s 无效", label)
			}
			normalizedKey := strings.ToLower(key)
			if _, exists := seen[normalizedKey]; exists {
				return fmt.Errorf("%s 存在重复字段", label)
			}
			seen[normalizedKey] = struct{}{}
			if err := walkStrictJSONValue(decoder, label); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("%s 无效", label)
		}
	case '[':
		for decoder.More() {
			if err := walkStrictJSONValue(decoder, label); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("%s 无效", label)
		}
	default:
		return fmt.Errorf("%s 无效", label)
	}
	return nil
}

func validateProjectProtectionFieldNames(raw []byte) error {
	root, ok := projectProtectionJSONObject(raw)
	if !ok {
		return nil
	}
	if err := rejectUnknownProjectProtectionFields(root, map[string]struct{}{
		"version":             {},
		"projectRoot":         {},
		"workspace":           {},
		"localActionFirewall": {},
	}, ""); err != nil {
		return err
	}
	if workspaceRaw, exists := root["workspace"]; exists {
		if workspace, ok := projectProtectionJSONObject(workspaceRaw); ok {
			if err := rejectUnknownProjectProtectionFields(workspace, map[string]struct{}{
				"name":  {},
				"slug":  {},
				"orgId": {},
			}, "workspace."); err != nil {
				return err
			}
		}
	}
	firewallRaw, exists := root["localActionFirewall"]
	if !exists {
		return nil
	}
	firewall, ok := projectProtectionJSONObject(firewallRaw)
	if !ok {
		return nil
	}
	if err := rejectUnknownProjectProtectionFields(firewall, map[string]struct{}{
		"enabled":        {},
		"defaultMode":    {},
		"protectedPaths": {},
		"egress":         {},
		"notes":          {},
	}, "localActionFirewall."); err != nil {
		return err
	}
	if rulesRaw, exists := firewall["protectedPaths"]; exists {
		if rules, ok := projectProtectionJSONArray(rulesRaw); ok {
			for index, ruleRaw := range rules {
				rule, ok := projectProtectionJSONObject(ruleRaw)
				if !ok {
					continue
				}
				if err := rejectUnknownProjectProtectionFields(rule, map[string]struct{}{
					"pattern": {},
					"read":    {},
					"write":   {},
					"delete":  {},
					"exec":    {},
					"reason":  {},
				}, fmt.Sprintf("localActionFirewall.protectedPaths[%d].", index)); err != nil {
					return err
				}
			}
		}
	}
	if egressRaw, exists := firewall["egress"]; exists {
		if egress, ok := projectProtectionJSONObject(egressRaw); ok {
			if err := rejectUnknownProjectProtectionFields(egress, map[string]struct{}{
				"enabled":       {},
				"allowedHosts":  {},
				"unlistedWrite": {},
			}, "localActionFirewall.egress."); err != nil {
				return err
			}
		}
	}
	return nil
}

func projectProtectionJSONObject(raw []byte) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func projectProtectionJSONArray(raw []byte) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil {
		return nil, false
	}
	return items, true
}

func rejectUnknownProjectProtectionFields(fields map[string]json.RawMessage, allowed map[string]struct{}, prefix string) error {
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("项目保护策略 JSON 包含未知字段 %q", prefix+name)
		}
	}
	return nil
}

func normalizeProtectedPathRule(raw protectedPathRuleJSON) (ProtectedPathRule, error) {
	rawPattern := raw.Pattern
	if rawPattern == "" || strings.TrimSpace(rawPattern) != rawPattern ||
		utf8.RuneCountInString(rawPattern) > 256 || !validProjectPattern(rawPattern) {
		return ProtectedPathRule{}, errors.New("protectedPaths.pattern 必须是仓库内相对路径 pattern")
	}
	reason, err := validateProjectReason(raw.Reason)
	if err != nil {
		return ProtectedPathRule{}, err
	}
	pattern := normalizeProjectPattern(rawPattern)
	rule := ProtectedPathRule{
		Pattern: pattern,
		Reason:  reason,
	}
	for name, value := range map[string]*string{
		"read": raw.Read, "write": raw.Write, "delete": raw.Delete, "exec": raw.Exec,
	} {
		if value == nil {
			continue
		}
		if !validProjectEffect(*value) {
			return ProtectedPathRule{}, fmt.Errorf("受保护路径动作 %s 只支持 require_approval 或 deny", name)
		}
		switch name {
		case "read":
			rule.Read = *value
		case "write":
			rule.Write = *value
		case "delete":
			rule.Delete = *value
		case "exec":
			rule.Exec = *value
		}
	}
	if rule.Read == "" && rule.Write == "" && rule.Delete == "" && rule.Exec == "" {
		return ProtectedPathRule{}, errors.New("受保护路径规则至少配置一个动作")
	}
	return rule, nil
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
	for _, r := range raw {
		if unicode.IsControl(r) {
			return false
		}
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
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("egress.allowedHosts 必须是具体 host 或 *.domain")
	}
	host := strings.ToLower(value)
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

func validateProjectReason(value string) (string, error) {
	if utf8.RuneCountInString(value) > 160 {
		return "", errors.New("protectedPaths.reason 不能超过 160 个字符")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("protectedPaths.reason 不能包含控制字符")
		}
	}
	return value, nil
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
	lastUpdateTarget := ""
	for _, rawLine := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "*** Update File: ") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: target, Operation: "write"})
			lastUpdateTarget = target
			continue
		}
		if strings.HasPrefix(line, "*** Move to: ") {
			target := strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: "))
			if lastUpdateTarget != "" {
				operations = removeProjectTargetOperation(operations, lastUpdateTarget, "write")
				operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: lastUpdateTarget, Operation: "delete"})
			}
			operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: target, Operation: "write"})
			lastUpdateTarget = ""
			continue
		}
		lastUpdateTarget = ""
		for _, candidate := range prefixOperations {
			if candidate.prefix == "*** Update File: " || candidate.prefix == "*** Move to: " {
				continue
			}
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

func removeProjectTargetOperation(operations []projectTargetOperation, target, operation string) []projectTargetOperation {
	filtered := operations[:0]
	for _, candidate := range operations {
		if candidate.Operation == operation &&
			(candidate.Target == target || (runtime.GOOS == "windows" && strings.EqualFold(candidate.Target, target))) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
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
		tool == "shell" || tool == "shell_command" || tool == "powershell" || tool == "pwsh" ||
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
		for _, operation := range projectRedirectionTargetOperations(tokens) {
			operations = appendProjectTargetOperation(operations, operation)
		}
		for _, operation := range parseProjectCommandTargetOperations(tokens, 0) {
			operations = appendProjectTargetOperation(operations, operation)
		}
	}
	return operations
}

func projectRedirectionTargetOperations(tokens []string) []projectTargetOperation {
	var operations []projectTargetOperation
	for index, token := range tokens {
		if index+1 >= len(tokens) {
			continue
		}
		target := strings.TrimSpace(tokens[index+1])
		if target == "" || target == ">" || target == "<" {
			continue
		}
		operation := ""
		switch {
		case strings.Trim(token, ">") == "" && token != "":
			operation = "write"
		case token == "<":
			operation = "read"
		}
		if operation != "" {
			operations = appendProjectTargetOperation(operations, projectTargetOperation{
				Target:    target,
				Operation: operation,
			})
		}
	}
	return operations
}

func stripProjectRedirections(tokens []string) []string {
	result := make([]string, 0, len(tokens))
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if isProjectFileDescriptor(token) && index+1 < len(tokens) && isProjectRedirectionToken(tokens[index+1]) {
			continue
		}
		if isProjectRedirectionToken(token) {
			if index+1 < len(tokens) {
				index++
			}
			continue
		}
		result = append(result, token)
	}
	return result
}

func isProjectFileDescriptor(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isProjectRedirectionToken(value string) bool {
	return value == "<" || strings.Trim(value, ">") == "" && value != "" || strings.HasPrefix(value, "<<")
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
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		if quote != 0 {
			if quote == '"' && index+1 < len(runes) &&
				((char == '\\' && (runes[index+1] == '\\' || runes[index+1] == quote)) ||
					(char == '`' && (runes[index+1] == '`' || runes[index+1] == quote))) {
				token.WriteRune(runes[index+1])
				index++
				continue
			}
			if char == quote {
				quote = 0
				continue
			}
			token.WriteRune(char)
			continue
		}
		if (char == '\\' || char == '`' || char == '^') && index+1 < len(runes) {
			next := runes[index+1]
			if next == '\r' || next == '\n' {
				index++
				if next == '\r' && index+1 < len(runes) && runes[index+1] == '\n' {
					index++
				}
				continue
			}
			if strings.ContainsRune("'\";|&><", next) {
				token.WriteRune(char)
				token.WriteRune(next)
				index++
				continue
			}
		}
		switch char {
		case '\'', '"':
			quote = char
		case ';', '&', '|':
			flushSegment()
		case '>':
			flushToken()
			count := 1
			for index+count < len(runes) && runes[index+count] == '>' {
				count++
			}
			current = append(current, strings.Repeat(">", count))
			index += count - 1
			if count == 1 && index+1 < len(runes) && runes[index+1] == '|' {
				index++
			}
		case '<':
			flushToken()
			count := 1
			for index+count < len(runes) && runes[index+count] == '<' {
				count++
			}
			current = append(current, strings.Repeat("<", count))
			index += count - 1
		case '\r', '\n':
			flushSegment()
		case ' ', '\t':
			flushToken()
		default:
			token.WriteRune(char)
		}
	}
	flushSegment()
	return segments
}

func parseProjectCommandTargetOperations(tokens []string, depth int) []projectTargetOperation {
	tokens = stripProjectRedirections(tokens)
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
	executable := projectCommandName(tokens[index])
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
	case "select-string", "sls":
		return projectOperationsForTargets(extractProjectSelectStringTargets(tokens[index+1:]), "read")
	case "set-content", "add-content", "out-file", "new-item":
		return projectOperationsForTargets(extractProjectPowerShellPathTargets(tokens[index+1:], nil, 1), "write")
	case "tee-object":
		return projectOperationsForTargets(extractProjectPowerShellPathTargets(tokens[index+1:], teeObjectParameterSpecs, 1), "write")
	case "touch", "mkdir", "tee":
		return projectOperationsForTargets(extractProjectPositionalTargets(tokens[index+1:], nil), "write")
	case "truncate":
		return projectTruncateOperations(tokens[index+1:])
	case "curl":
		operations := projectOperationsForTargets(extractProjectCurlInputTargets(tokens[index+1:]), "read")
		for _, operation := range projectOperationsForTargets(extractProjectCurlOutputTargets(tokens[index+1:]), "write") {
			operations = appendProjectTargetOperation(operations, operation)
		}
		return operations
	case "invoke-restmethod", "invoke-webrequest", "irm", "iwr":
		operations := projectOperationsForTargets(extractProjectPowerShellInputTargets(tokens[index+1:]), "read")
		for _, operation := range projectOperationsForTargets(extractProjectPowerShellOutputTargets(tokens[index+1:]), "write") {
			operations = appendProjectTargetOperation(operations, operation)
		}
		return operations
	case "copy-item", "cp", "copy":
		return projectCopyMoveOperations(executable, tokens[index+1:], false)
	case "move-item", "rename-item", "mv", "move", "ren", "rename":
		return projectCopyMoveOperations(executable, tokens[index+1:], true)
	default:
		if isProjectScriptPath(tokens[index]) {
			return []projectTargetOperation{{Target: tokens[index], Operation: "exec"}}
		}
		if isProjectScriptInterpreter(executable) {
			var operations []projectTargetOperation
			for _, target := range tokens[index+1:] {
				if isProjectScriptPath(target) {
					operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: target, Operation: "exec"})
				}
			}
			return operations
		}
		return nil
	}
}

func isProjectScriptInterpreter(executable string) bool {
	switch executable {
	case "python", "python3", "py", "pypy", "pypy3", "node", "nodejs", "deno", "bun", "ruby", "perl", "php":
		return true
	default:
		if !strings.HasPrefix(executable, "python") || len(executable) == len("python") {
			return false
		}
		for _, char := range executable[len("python"):] {
			if (char < '0' || char > '9') && char != '.' {
				return false
			}
		}
		return true
	}
}

func projectCommandName(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	return strings.TrimSuffix(strings.ToLower(path.Base(normalized)), ".exe")
}

func isProjectScriptPath(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{".ps1", ".psm1", ".vbs", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".py", ".sh", ".bash", ".bat", ".cmd", ".pl", ".rb", ".php"} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func extractProjectSelectStringTargets(args []string) []string {
	var targets []string
	patternProvided := false
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if strings.HasPrefix(arg, "-") {
			spec, inlineValue, hasInlineValue, resolution := resolvePowerShellParameter(arg, selectStringParameterSpecs)
			if resolution != powerShellParameterResolved {
				return nil
			}
			value := inlineValue
			if spec.kind != powerShellSwitch && !hasInlineValue {
				index++
				if index >= len(args) {
					return nil
				}
				value = args[index]
			}
			switch spec.kind {
			case powerShellForbidden:
				return nil
			case powerShellSwitch:
				if hasInlineValue {
					return nil
				}
			case powerShellPath:
				targets = appendProjectArgumentTargets(targets, value)
			case powerShellPattern:
				if strings.TrimSpace(value) == "" {
					return nil
				}
				patternProvided = true
			case powerShellValue:
				if strings.TrimSpace(value) == "" {
					return nil
				}
			}
			continue
		}
		if !patternProvided {
			patternProvided = true
		} else {
			targets = appendProjectArgumentTargets(targets, arg)
		}
	}
	return targets
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

func projectCopyMoveOperations(executable string, args []string, move bool) []projectTargetOperation {
	var sources []string
	destination := ""
	if executable == "copy-item" || executable == "move-item" || executable == "rename-item" ||
		((executable == "cp" || executable == "mv") && projectUsesPowerShellCopyMoveSyntax(args)) {
		sources, destination = extractProjectPowerShellCopyMoveTargets(args)
	} else {
		sources, destination = extractProjectPositionalCopyMoveTargets(args)
	}
	if len(sources) == 0 || destination == "" {
		return nil
	}
	if (executable == "rename-item" || executable == "ren") && len(sources) == 1 {
		destination = projectRenameDestination(sources[0], destination)
	}
	var operations []projectTargetOperation
	sourceOperation := "read"
	if move {
		sourceOperation = "delete"
	}
	for _, source := range sources {
		operations = appendProjectTargetOperation(operations, projectTargetOperation{
			Target:    source,
			Operation: sourceOperation,
		})
	}
	return appendProjectTargetOperation(operations, projectTargetOperation{
		Target:    destination,
		Operation: "write",
	})
}

func projectUsesPowerShellCopyMoveSyntax(args []string) bool {
	for _, arg := range args {
		spec, _, _, resolution := resolvePowerShellParameter(strings.TrimSpace(arg), projectCopyMoveParameterSpecs)
		if resolution != powerShellParameterResolved {
			continue
		}
		switch spec.name {
		case "path", "literalpath", "destination", "newname":
			return true
		}
	}
	return false
}

func projectRenameDestination(source, destination string) string {
	normalizedDestination := strings.ReplaceAll(strings.TrimSpace(destination), "\\", "/")
	if normalizedDestination == "" || path.IsAbs(normalizedDestination) || strings.Contains(normalizedDestination, "/") {
		return destination
	}
	normalizedSource := strings.ReplaceAll(strings.TrimSpace(source), "\\", "/")
	directory := path.Dir(normalizedSource)
	if directory == "." || directory == "" {
		return normalizedDestination
	}
	return path.Join(directory, normalizedDestination)
}

var projectCopyMoveParameterSpecs = []powerShellParameterSpec{
	{name: "confirm", kind: powerShellSwitch},
	{name: "container", kind: powerShellSwitch},
	{name: "credential", kind: powerShellValue},
	{name: "debug", kind: powerShellSwitch},
	{name: "destination", kind: powerShellValue},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "exclude", kind: powerShellValue},
	{name: "filter", kind: powerShellValue},
	{name: "force", kind: powerShellSwitch},
	{name: "fromsession", kind: powerShellValue},
	{name: "include", kind: powerShellValue},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "literalpath", kind: powerShellPath},
	{name: "newname", kind: powerShellValue},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "passthru", kind: powerShellSwitch},
	{name: "path", kind: powerShellPath},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "recurse", kind: powerShellSwitch},
	{name: "tosession", kind: powerShellValue},
	{name: "verbose", kind: powerShellSwitch},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
	{name: "whatif", kind: powerShellSwitch},
}

func extractProjectPowerShellCopyMoveTargets(args []string) ([]string, string) {
	var sources []string
	var positionals []string
	destination := ""
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = appendProjectArgumentTargets(positionals, arg)
			continue
		}
		spec, inlineValue, hasInlineValue, resolution := resolvePowerShellParameter(arg, projectCopyMoveParameterSpecs)
		if resolution != powerShellParameterResolved {
			return nil, ""
		}
		value := inlineValue
		if spec.kind != powerShellSwitch && !hasInlineValue {
			index++
			if index >= len(args) {
				return nil, ""
			}
			value = args[index]
		}
		if spec.kind == powerShellSwitch {
			if hasInlineValue {
				return nil, ""
			}
			continue
		}
		switch spec.name {
		case "path", "literalpath":
			sources = appendProjectArgumentTargets(sources, value)
		case "destination", "newname":
			destination = strings.TrimSpace(value)
		}
	}
	if len(sources) == 0 {
		if destination == "" && len(positionals) >= 2 {
			destination = positionals[len(positionals)-1]
			positionals = positionals[:len(positionals)-1]
		}
		sources = append(sources, positionals...)
	} else if destination == "" && len(positionals) > 0 {
		destination = positionals[len(positionals)-1]
	} else {
		sources = append(sources, positionals...)
	}
	return sources, destination
}

var teeObjectParameterSpecs = []powerShellParameterSpec{
	{name: "append", kind: powerShellSwitch},
	{name: "debug", kind: powerShellSwitch},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "filepath", kind: powerShellPath},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "inputobject", kind: powerShellValue},
	{name: "noclobber", kind: powerShellSwitch},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "variable", kind: powerShellValue},
	{name: "verbose", kind: powerShellSwitch},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
}

func projectTruncateOperations(args []string) []projectTargetOperation {
	var operations []projectTargetOperation
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		lowerArg := strings.ToLower(arg)
		switch lowerArg {
		case "-s", "--size":
			index++
			if index >= len(args) {
				return nil
			}
		case "-r", "--reference":
			index++
			if index >= len(args) {
				return nil
			}
			operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: args[index], Operation: "read"})
		default:
			if strings.HasPrefix(lowerArg, "--reference=") {
				operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: arg[len("--reference="):], Operation: "read"})
				continue
			}
			if strings.HasPrefix(lowerArg, "-r") && len(arg) > 2 {
				operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: arg[2:], Operation: "read"})
				continue
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			operations = appendProjectTargetOperation(operations, projectTargetOperation{Target: arg, Operation: "write"})
		}
	}
	return operations
}

func extractProjectPositionalCopyMoveTargets(args []string) ([]string, string) {
	var targets []string
	destination := ""
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		lowerArg := strings.ToLower(arg)
		if arg == "--" {
			for _, target := range args[index+1:] {
				targets = appendProjectArgumentTargets(targets, target)
			}
			break
		}
		if lowerArg == "-t" || lowerArg == "--target-directory" {
			index++
			if index >= len(args) {
				return nil, ""
			}
			destination = strings.TrimSpace(args[index])
			continue
		}
		if strings.HasPrefix(lowerArg, "--target-directory=") {
			destination = strings.TrimSpace(arg[len("--target-directory="):])
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		targets = appendProjectArgumentTargets(targets, arg)
	}
	if destination == "" && len(targets) >= 2 {
		destination = targets[len(targets)-1]
		targets = targets[:len(targets)-1]
	}
	return targets, destination
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

func projectNetworkWriteURLs(input ActionInput) []string {
	var urls []string
	appendURL := func(rawURL string) {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" || projectNetworkHost(rawURL) == "" {
			return
		}
		for _, existing := range urls {
			if existing == rawURL {
				return
			}
		}
		urls = append(urls, rawURL)
	}
	if isProjectNetworkWrite(input) {
		appendURL(input.NetworkURL)
	}
	if isProjectShellInput(input) {
		for _, content := range []string{input.Command, input.ContentPreview} {
			for _, rawURL := range projectShellNetworkWriteURLs(content, 0) {
				appendURL(rawURL)
			}
		}
	}
	return urls
}

func projectShellNetworkWriteURLs(command string, depth int) []string {
	if strings.TrimSpace(command) == "" || depth > 2 {
		return nil
	}
	var urls []string
	for _, tokens := range splitProjectCommandSegments(command) {
		tokens = stripProjectRedirections(tokens)
		if len(tokens) == 0 {
			continue
		}
		index := 0
		if strings.EqualFold(tokens[index], "sudo") {
			index++
			for index < len(tokens) && strings.HasPrefix(tokens[index], "-") {
				index++
			}
		}
		if index < len(tokens) && strings.EqualFold(tokens[index], "command") {
			index++
		}
		if index >= len(tokens) {
			continue
		}
		executable := projectCommandName(tokens[index])
		args := tokens[index+1:]
		switch executable {
		case "cmd":
			for candidate := 0; candidate+1 < len(args); candidate++ {
				if strings.EqualFold(args[candidate], "/c") || strings.EqualFold(args[candidate], "/k") {
					urls = appendProjectNetworkURLs(urls, projectShellNetworkWriteURLs(strings.Join(args[candidate+1:], " "), depth+1))
					break
				}
			}
		case "powershell", "pwsh", "bash", "sh", "zsh":
			for candidate := 0; candidate+1 < len(args); candidate++ {
				if strings.EqualFold(args[candidate], "-command") || strings.EqualFold(args[candidate], "-c") {
					urls = appendProjectNetworkURLs(urls, projectShellNetworkWriteURLs(strings.Join(args[candidate+1:], " "), depth+1))
					break
				}
			}
		case "curl":
			urls = appendProjectNetworkURLs(urls, extractProjectCurlWriteURLs(args))
		case "invoke-restmethod", "invoke-webrequest", "irm", "iwr":
			urls = appendProjectNetworkURLs(urls, extractProjectPowerShellNetworkWriteURLs(args))
		}
	}
	return urls
}

func appendProjectNetworkURLs(target []string, values []string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || projectNetworkHost(value) == "" {
			continue
		}
		seen := false
		for _, existing := range target {
			if existing == value {
				seen = true
				break
			}
		}
		if !seen {
			target = append(target, value)
		}
	}
	return target
}

type projectCurlTransfer struct {
	method        string
	hasPayload    bool
	headOnly      bool
	urls          []string
	inputTargets  []string
	outputTargets []string
}

func extractProjectCurlWriteURLs(args []string) []string {
	transfers, ok := parseProjectCurlTransfers(args)
	if !ok {
		return nil
	}
	var urls []string
	for _, transfer := range transfers {
		if projectCurlTransferWrites(transfer) {
			urls = appendProjectNetworkURLs(urls, transfer.urls)
		}
	}
	return urls
}

func extractProjectCurlOutputTargets(args []string) []string {
	transfers, ok := parseProjectCurlTransfers(args)
	if !ok {
		return nil
	}
	var targets []string
	for _, transfer := range transfers {
		for _, target := range transfer.outputTargets {
			targets = appendProjectTarget(targets, target)
		}
	}
	return targets
}

func extractProjectCurlInputTargets(args []string) []string {
	transfers, ok := parseProjectCurlTransfers(args)
	if !ok {
		return nil
	}
	var targets []string
	for _, transfer := range transfers {
		for _, target := range transfer.inputTargets {
			targets = appendProjectTarget(targets, target)
		}
	}
	return targets
}

func parseProjectCurlTransfers(args []string) ([]projectCurlTransfer, bool) {
	var transfers []projectCurlTransfer
	current := projectCurlTransfer{}
	flush := func() {
		transfers = append(transfers, current)
		current = projectCurlTransfer{}
	}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "--next" {
			flush()
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, inlineValue, hasInlineValue := splitProjectCurlLongOption(arg)
			switch name {
			case "request":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				current.method = strings.ToUpper(strings.TrimSpace(value))
			case "data", "data-ascii", "data-binary", "data-raw", "data-urlencode", "form", "form-string", "json", "upload-file":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				current.hasPayload = true
				current.inputTargets = appendProjectCurlPayloadTargets(current.inputTargets, name, value)
			case "head":
				if hasInlineValue {
					return nil, false
				}
				current.headOnly = true
			case "url":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				current.urls = appendProjectNetworkURLs(current.urls, []string{value})
			case "output", "cookie-jar", "dump-header", "trace", "trace-ascii":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				current.outputTargets = appendProjectFileTarget(current.outputTargets, value)
			case "config", "cert", "key", "cacert", "capath":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				current.inputTargets = appendProjectFileTarget(current.inputTargets, value)
			case "header":
				value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
				if !ok {
					return nil, false
				}
				if strings.HasPrefix(strings.TrimSpace(value), "@") {
					current.inputTargets = appendProjectFileTarget(current.inputTargets, strings.TrimSpace(value)[1:])
				}
			default:
				if projectCurlLongOptionConsumesValue(name) && !hasInlineValue {
					index++
					if index >= len(args) {
						return nil, false
					}
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			if !applyProjectCurlShortOptions(&current, arg[1:], args, &index) {
				return nil, false
			}
			continue
		}
		current.urls = appendProjectNetworkURLs(current.urls, []string{arg})
	}
	flush()
	return transfers, true
}

func splitProjectCurlLongOption(value string) (string, string, bool) {
	option := strings.TrimPrefix(strings.TrimSpace(value), "--")
	if index := strings.Index(option, "="); index >= 0 {
		return strings.ToLower(strings.TrimSpace(option[:index])), option[index+1:], true
	}
	return strings.ToLower(strings.TrimSpace(option)), "", false
}

func applyProjectCurlShortOptions(transfer *projectCurlTransfer, cluster string, args []string, index *int) bool {
	for offset := 0; offset < len(cluster); offset++ {
		option := cluster[offset]
		switch option {
		case 'I':
			transfer.headOnly = true
		case 'X', 'd', 'F', 'T', 'o', 'c', 'D', 'K', 'E':
			value, ok := projectCurlShortOptionValue(cluster, offset, args, index)
			if !ok {
				return false
			}
			switch option {
			case 'X':
				transfer.method = strings.ToUpper(strings.TrimSpace(value))
			case 'o', 'c', 'D':
				transfer.outputTargets = appendProjectFileTarget(transfer.outputTargets, value)
			case 'K', 'E':
				transfer.inputTargets = appendProjectFileTarget(transfer.inputTargets, value)
			case 'd', 'F', 'T':
				transfer.hasPayload = true
				transfer.inputTargets = appendProjectCurlPayloadTargets(transfer.inputTargets, string(option), value)
			}
			return true
		case 'A', 'b', 'C', 'e', 'H', 'm', 'P', 'Q', 'r', 't', 'u', 'U', 'w', 'x', 'Y', 'y', 'z':
			value, ok := projectCurlShortOptionValue(cluster, offset, args, index)
			if ok && option == 'H' && strings.HasPrefix(strings.TrimSpace(value), "@") {
				transfer.inputTargets = appendProjectFileTarget(transfer.inputTargets, strings.TrimSpace(value)[1:])
			}
			return ok
		}
	}
	return true
}

func appendProjectFileTarget(targets []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return targets
	}
	return appendProjectTarget(targets, value)
}

func appendProjectCurlPayloadTargets(targets []string, option, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return targets
	}
	option = strings.TrimLeft(strings.TrimSpace(option), "-")
	switch option {
	case "T", "upload-file":
		return appendProjectFileTarget(targets, value)
	case "d", "data", "data-ascii", "data-binary", "json":
		if strings.HasPrefix(value, "@") {
			return appendProjectFileTarget(targets, value[1:])
		}
	case "data-urlencode":
		if marker := strings.Index(value, "@"); marker >= 0 {
			return appendProjectFileTarget(targets, value[marker+1:])
		}
	case "F", "form":
		for _, marker := range []string{"=@", "=<"} {
			if index := strings.Index(value, marker); index >= 0 {
				file := value[index+len(marker):]
				if separator := strings.Index(file, ";"); separator >= 0 {
					file = file[:separator]
				}
				return appendProjectFileTarget(targets, file)
			}
		}
	}
	return targets
}

func projectCurlShortOptionValue(cluster string, offset int, args []string, index *int) (string, bool) {
	if offset+1 < len(cluster) {
		value := strings.TrimSpace(cluster[offset+1:])
		return value, value != ""
	}
	(*index)++
	if *index >= len(args) {
		return "", false
	}
	value := strings.TrimSpace(args[*index])
	return value, value != ""
}

func projectCurlTransferWrites(transfer projectCurlTransfer) bool {
	if transfer.hasPayload {
		return true
	}
	method := strings.ToUpper(strings.TrimSpace(transfer.method))
	if method == "" {
		if transfer.headOnly {
			return false
		}
		method = "GET"
	}
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

func projectCurlLongOptionConsumesValue(name string) bool {
	switch name {
	case "connect-timeout", "connect-to", "cookie", "max-time", "proxy", "referer", "resolve", "retry",
		"telnet-option", "user", "user-agent", "write-out":
		return true
	default:
		return false
	}
}

type projectPowerShellNetworkRequest struct {
	method        string
	hasPayload    bool
	urls          []string
	inputTargets  []string
	outputTargets []string
}

var projectPowerShellNetworkParameterSpecs = []powerShellParameterSpec{
	{name: "authentication", kind: powerShellValue},
	{name: "body", kind: powerShellValue},
	{name: "contenttype", kind: powerShellValue},
	{name: "credential", kind: powerShellValue},
	{name: "debug", kind: powerShellSwitch},
	{name: "erroraction", kind: powerShellValue},
	{name: "errorvariable", kind: powerShellValue},
	{name: "form", kind: powerShellValue},
	{name: "headers", kind: powerShellValue},
	{name: "informationaction", kind: powerShellValue},
	{name: "informationvariable", kind: powerShellValue},
	{name: "infile", kind: powerShellValue},
	{name: "maximumredirection", kind: powerShellValue},
	{name: "method", kind: powerShellValue},
	{name: "outfile", kind: powerShellPath},
	{name: "outbuffer", kind: powerShellValue},
	{name: "outvariable", kind: powerShellValue},
	{name: "passthru", kind: powerShellSwitch},
	{name: "pipelinevariable", kind: powerShellValue},
	{name: "progressaction", kind: powerShellValue},
	{name: "proxy", kind: powerShellValue},
	{name: "proxycredential", kind: powerShellValue},
	{name: "sessionvariable", kind: powerShellValue},
	{name: "timeoutsec", kind: powerShellValue},
	{name: "token", kind: powerShellValue},
	{name: "transferencoding", kind: powerShellValue},
	{name: "uri", kind: powerShellValue},
	{name: "usebasicparsing", kind: powerShellSwitch},
	{name: "useragent", kind: powerShellValue},
	{name: "verbose", kind: powerShellSwitch},
	{name: "warningaction", kind: powerShellValue},
	{name: "warningvariable", kind: powerShellValue},
	{name: "websession", kind: powerShellValue},
}

func extractProjectPowerShellNetworkWriteURLs(args []string) []string {
	request, ok := parseProjectPowerShellNetworkRequest(args)
	if !ok || !projectPowerShellRequestWrites(request) {
		return nil
	}
	return request.urls
}

func extractProjectPowerShellOutputTargets(args []string) []string {
	request, ok := parseProjectPowerShellNetworkRequest(args)
	if !ok {
		return nil
	}
	return request.outputTargets
}

func extractProjectPowerShellInputTargets(args []string) []string {
	request, ok := parseProjectPowerShellNetworkRequest(args)
	if !ok {
		return nil
	}
	return request.inputTargets
}

func parseProjectPowerShellNetworkRequest(args []string) (projectPowerShellNetworkRequest, bool) {
	request := projectPowerShellNetworkRequest{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if !strings.HasPrefix(arg, "-") {
			request.urls = appendProjectNetworkURLs(request.urls, []string{arg})
			continue
		}
		spec, inlineValue, hasInlineValue, resolution := resolveProjectPowerShellNetworkParameter(arg)
		if resolution != powerShellParameterResolved {
			continue
		}
		if spec.kind == powerShellSwitch {
			if hasInlineValue {
				return projectPowerShellNetworkRequest{}, false
			}
			continue
		}
		value, ok := projectCommandOptionValue(args, &index, inlineValue, hasInlineValue)
		if !ok {
			return projectPowerShellNetworkRequest{}, false
		}
		switch spec.name {
		case "method":
			request.method = strings.ToUpper(strings.TrimSpace(value))
		case "uri":
			request.urls = appendProjectNetworkURLs(request.urls, []string{value})
		case "body", "form", "infile":
			request.hasPayload = true
			if spec.name == "infile" {
				request.inputTargets = appendProjectFileTarget(request.inputTargets, value)
			}
		case "outfile":
			request.outputTargets = appendProjectFileTarget(request.outputTargets, value)
		}
	}
	return request, true
}

func resolveProjectPowerShellNetworkParameter(token string) (powerShellParameterSpec, string, bool, powerShellParameterResolution) {
	return resolvePowerShellParameter(token, projectPowerShellNetworkParameterSpecs)
}

func projectPowerShellRequestWrites(request projectPowerShellNetworkRequest) bool {
	if request.hasPayload {
		return true
	}
	method := strings.ToUpper(strings.TrimSpace(request.method))
	if method == "" {
		method = "GET"
	}
	return method != "GET" && method != "HEAD" && method != "OPTIONS"
}

func splitProjectPowerShellOption(value string) (string, string, bool) {
	name := strings.TrimPrefix(strings.TrimSpace(value), "-")
	inlineValue := ""
	hasInlineValue := false
	if index := strings.Index(name, ":"); index >= 0 {
		inlineValue = name[index+1:]
		name = name[:index]
		hasInlineValue = true
	}
	return strings.ToLower(strings.TrimSpace(name)), inlineValue, hasInlineValue
}

func projectCommandOptionValue(args []string, index *int, inlineValue string, hasInlineValue bool) (string, bool) {
	if hasInlineValue {
		return inlineValue, strings.TrimSpace(inlineValue) != ""
	}
	(*index)++
	if *index >= len(args) {
		return "", false
	}
	return args[*index], strings.TrimSpace(args[*index]) != ""
}

func projectNetworkHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if parsed.Hostname() == "" && !strings.Contains(rawURL, "://") {
		parsed, err = url.Parse("http://" + strings.TrimPrefix(rawURL, "//"))
		if err != nil {
			return ""
		}
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

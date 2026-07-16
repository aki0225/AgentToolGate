package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	AdapterModeDryRun  = "dry-run"
	AdapterModeEnforce = "enforce"
)

type AdapterInput struct {
	Client  string
	Mode    string
	Payload []byte
}

type AdapterResult struct {
	Client     string   `json:"client"`
	Mode       string   `json:"mode"`
	WouldBlock bool     `json:"wouldBlock"`
	WouldAsk   bool     `json:"wouldAsk"`
	Decision   string   `json:"decision"`
	RiskLevel  string   `json:"riskLevel"`
	Silent     bool     `json:"silent"`
	Reason     string   `json:"reason"`
	Signals    []string `json:"signals,omitempty"`
	Category   string   `json:"category"`
	Message    string   `json:"message"`
}

func ReadAdapterPayload(pathValue string) ([]byte, error) {
	trimmed := strings.TrimSpace(pathValue)
	if trimmed == "" {
		return nil, errors.New("hook payload 路径不能为空")
	}
	if trimmed == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("读取标准输入失败")
		}
		return data, nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return nil, fmt.Errorf("读取 hook payload 失败")
	}
	return data, nil
}

func EvaluateAdaptedPayload(input AdapterInput) (AdapterResult, error) {
	client, err := normalizeAdapterClient(input.Client)
	if err != nil {
		return AdapterResult{}, err
	}
	mode, err := normalizeAdapterMode(input.Mode)
	if err != nil {
		return AdapterResult{}, err
	}
	if len(bytes.TrimSpace(input.Payload)) == 0 {
		return AdapterResult{}, errors.New("hook payload 不能为空")
	}

	var action ActionInput
	switch client {
	case "claude":
		action, err = AdaptClaudePayload(input.Payload)
	case "codex":
		action, err = AdaptCodexPayload(input.Payload)
	default:
		err = fmt.Errorf("不支持的 hook client")
	}
	if err != nil {
		return AdapterResult{}, err
	}
	action.Client = client
	decision := Evaluate(action)
	return adapterResultFromDecision(client, mode, decision), nil
}

func AdaptClaudePayload(data []byte) (ActionInput, error) {
	payload, err := decodeHookPayload(data)
	if err != nil {
		return ActionInput{}, err
	}
	action := ActionInput{Client: "claude"}
	populateActionFromPayload(&action, payload)
	return action, nil
}

func AdaptCodexPayload(data []byte) (ActionInput, error) {
	payload, err := decodeHookPayload(data)
	if err != nil {
		return ActionInput{}, err
	}
	action := ActionInput{Client: "codex"}
	populateActionFromPayload(&action, payload)
	return action, nil
}

func normalizeAdapterClient(client string) (string, error) {
	switch lowerTrim(client) {
	case "claude":
		return "claude", nil
	case "codex":
		return "codex", nil
	default:
		return "", errors.New("hook client 仅支持 claude 或 codex")
	}
}

func normalizeAdapterMode(mode string) (string, error) {
	switch lowerTrim(firstNonEmpty(mode, AdapterModeDryRun)) {
	case AdapterModeDryRun:
		return AdapterModeDryRun, nil
	case AdapterModeEnforce:
		return AdapterModeEnforce, nil
	default:
		return "", errors.New("hook adapter mode 仅支持 dry-run 或 enforce")
	}
}

func adapterResultFromDecision(client, mode string, decision Decision) AdapterResult {
	result := AdapterResult{
		Client:     client,
		Mode:       mode,
		WouldBlock: decision.Decision == "deny",
		WouldAsk:   decision.Decision == "ask",
		Decision:   decision.Decision,
		RiskLevel:  decision.RiskLevel,
		Silent:     decision.Silent,
		Reason:     decision.Reason,
		Signals:    append([]string(nil), decision.Signals...),
		Category:   decision.Category,
	}
	result.Message = adapterMessage(mode, decision.Decision)
	return result
}

func adapterMessage(mode, decision string) string {
	prefix := "AgentToolGate dry-run"
	if mode == AdapterModeEnforce {
		prefix = "AgentToolGate enforce"
	}
	switch decision {
	case "allow":
		return prefix + "：该动作会被允许。"
	case "deny":
		return prefix + "：该动作会被阻止。"
	case "ask":
		return prefix + "：该动作需要确认。"
	default:
		return prefix + "：该动作需要人工判断。"
	}
}

func decodeHookPayload(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("hook payload JSON 无效")
	}
	return payload, nil
}

func populateActionFromPayload(action *ActionInput, payload map[string]any) {
	toolInput := firstMap(payload, "tool_input", "toolInput", "input", "args", "arguments", "params")
	maps := []map[string]any{payload}
	if toolInput != nil {
		maps = append(maps, toolInput)
	}

	action.ToolName = firstStringFromMaps(maps, "tool_name", "toolName", "tool", "name")
	action.ActionType = firstStringFromMaps(maps, "action_type", "actionType", "action", "kind", "type")
	action.CWD = firstStringFromMaps(maps, "cwd", "working_directory", "workingDirectory", "workdir")
	action.ProjectRoot = firstStringFromMaps(maps, "project_root", "projectRoot", "workspace_root", "workspaceRoot", "repo_root", "repoRoot")
	action.Command = firstStringFromMaps(maps, "command", "cmd", "shell_command", "shellCommand", "script")
	action.Target = firstStringFromMaps(maps, "target", "path", "file_path", "filePath", "filename", "file")
	action.ContentPreview = firstStringFromMaps(maps, "content_preview", "contentPreview", "content", "body", "text", "new_string", "newString", "diff", "patch", "input")
	action.NetworkMethod = firstStringFromMaps(maps, "network_method", "networkMethod", "method", "http_method", "httpMethod")
	action.NetworkURL = firstStringFromMaps(maps, "network_url", "networkUrl", "url", "uri", "endpoint")

	// Claude/Codex 的 payload 字段并不稳定；adapter 只做保守归一化，真正风险判断交给 Guard Core。
	if action.ActionType == "" || lowerTrim(action.ActionType) == "pre_tool_use" {
		action.ActionType = inferActionType(*action)
	}
}

func inferActionType(action ActionInput) string {
	tool := lowerTrim(action.ToolName)
	if action.NetworkMethod != "" || action.NetworkURL != "" {
		return "network"
	}
	if action.Command != "" || containsAny(tool, "bash", "shell", "powershell", "terminal", "exec") {
		return "command"
	}
	if containsAny(tool, "read", "view", "open") {
		return "read"
	}
	if containsAny(tool, "write", "edit", "apply_patch", "patch") {
		return "write"
	}
	if action.Target != "" && action.ContentPreview != "" {
		return "write"
	}
	if action.Target != "" {
		return "read"
	}
	return "unknown"
}

func firstMap(payload map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			return nested
		}
	}
	return nil
}

func firstStringFromMaps(maps []map[string]any, keys ...string) string {
	for _, one := range maps {
		if one == nil {
			continue
		}
		for _, key := range keys {
			value, ok := one[key]
			if !ok {
				continue
			}
			if text := stringFromHookValue(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func stringFromHookValue(value any) string {
	switch typed := value.(type) {
	case string:
		return trimPreview(typed)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func trimPreview(value string) string {
	trimmed := strings.TrimSpace(value)
	const maxPreview = 2048
	if len(trimmed) <= maxPreview {
		return trimmed
	}
	return trimmed[:maxPreview]
}

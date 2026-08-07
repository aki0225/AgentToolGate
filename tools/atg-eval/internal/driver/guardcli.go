package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/backendruntime"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

const defaultCommandOutputLimit = 1 << 20

type Config struct {
	Executable        string
	PrefixArgs        []string
	Environment       []string
	Timeout           time.Duration
	Redactor          *redact.Redactor
	EnableMCP         bool
	RuntimeRoot       *sandbox.Root
	MCPWorkspaceOrgID string
	MCPStartupTimeout time.Duration
}

type GuardCLI struct {
	executable        string
	prefixArgs        []string
	environment       []string
	timeout           time.Duration
	redactor          *redact.Redactor
	mcpRuntime        *backendruntime.Server
	mcpWorkspaceOrgID string
}

type Evaluation struct {
	Decision  model.Decision
	RiskLevel string
	Silent    bool
	Reason    string
	Signals   []string
	Category  string
	Duration  time.Duration
}

type rawDecision struct {
	Decision  string   `json:"decision"`
	RiskLevel string   `json:"riskLevel"`
	Silent    bool     `json:"silent"`
	Reason    string   `json:"reason"`
	Signals   []string `json:"signals"`
	Category  string   `json:"category"`
}

type adapterDecision struct {
	Decision  string   `json:"decision"`
	RiskLevel string   `json:"riskLevel"`
	Silent    bool     `json:"silent"`
	Reason    string   `json:"reason"`
	Signals   []string `json:"signals"`
	Category  string   `json:"category"`
}

type hookOutput struct {
	HookSpecificOutput *struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	} `json:"hookSpecificOutput"`
}

func New(config Config) (*GuardCLI, error) {
	if strings.TrimSpace(config.Executable) == "" {
		return nil, fmt.Errorf("ATG 可执行文件不能为空")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	redactor := config.Redactor
	if redactor == nil {
		redactor = redact.New(redact.Options{})
	}
	guardCLI := &GuardCLI{
		executable:        strings.TrimSpace(config.Executable),
		prefixArgs:        append([]string(nil), config.PrefixArgs...),
		environment:       append([]string(nil), config.Environment...),
		timeout:           timeout,
		redactor:          redactor,
		mcpWorkspaceOrgID: strings.TrimSpace(config.MCPWorkspaceOrgID),
	}
	if config.EnableMCP {
		if config.RuntimeRoot == nil {
			return nil, fmt.Errorf("MCP Inbound runtime 缺少 sandbox root")
		}
		runtimeServer, err := backendruntime.Start(context.Background(), backendruntime.Config{
			Executable:     guardCLI.executable,
			Root:           config.RuntimeRoot,
			Name:           "mcp-inbound",
			WorkspaceOrgID: guardCLI.mcpWorkspaceOrgID,
			Subject:        "evaluation-mcp-viewer",
			Role:           "viewer",
			StartupTimeout: config.MCPStartupTimeout,
			Redactor:       redactor,
		})
		if err != nil {
			return nil, fmt.Errorf("启动 MCP Inbound runtime 失败：%w", err)
		}
		guardCLI.mcpRuntime = runtimeServer
	}
	return guardCLI, nil
}

func (g *GuardCLI) Evaluate(ctx context.Context, entry model.Entry, input operations.GuardInput) (Evaluation, error) {
	startedAt := time.Now()
	switch entry {
	case model.EntryGuardCore:
		raw, err := json.Marshal(input)
		if err != nil {
			return Evaluation{}, fmt.Errorf("编码 Guard 输入失败：%w", err)
		}
		output, err := g.run(ctx, raw, "guard", "evaluate", "--input", "-")
		if err != nil {
			return Evaluation{}, err
		}
		var decision rawDecision
		if err := decodeStrictJSON(output, &decision); err != nil {
			return Evaluation{}, fmt.Errorf("解析 Guard 决策失败：%w", err)
		}
		result, err := evaluationFromRaw(decision)
		result.Duration = time.Since(startedAt)
		return result, err
	case model.EntryClaudeHook, model.EntryCodexHook:
		return g.evaluateHook(ctx, entry, input, startedAt)
	case model.EntryMCPInbound:
		return g.evaluateMCPInbound(ctx, input, startedAt)
	default:
		return Evaluation{}, fmt.Errorf("动作 Runner 不支持 entry：%s", entry)
	}
}

func (g *GuardCLI) Close() error {
	if g == nil || g.mcpRuntime == nil {
		return nil
	}
	return g.mcpRuntime.Close()
}

func (g *GuardCLI) evaluateHook(ctx context.Context, entry model.Entry, input operations.GuardInput, startedAt time.Time) (Evaluation, error) {
	client := "claude"
	if entry == model.EntryCodexHook {
		client = "codex"
	}
	payload, err := json.Marshal(map[string]any{
		"tool_name": input.ToolName,
		"tool_input": map[string]any{
			"action_type":     input.ActionType,
			"cwd":             input.CWD,
			"project_root":    input.ProjectRoot,
			"command":         input.Command,
			"target":          input.Target,
			"content_preview": input.ContentPreview,
			"network_method":  input.NetworkMethod,
			"network_url":     input.NetworkURL,
		},
	})
	if err != nil {
		return Evaluation{}, fmt.Errorf("编码 Hook payload 失败：%w", err)
	}

	adaptedRaw, err := g.run(ctx, payload, "guard", "adapt", client, "--input", "-", "--mode", "enforce")
	if err != nil {
		return Evaluation{}, err
	}
	var adapted adapterDecision
	if err := decodeStrictJSON(adaptedRaw, &adapted); err != nil {
		return Evaluation{}, fmt.Errorf("解析 Hook Adapter 决策失败：%w", err)
	}
	result, err := evaluationFromRaw(rawDecision(adapted))
	if err != nil {
		return Evaluation{}, err
	}

	hookRaw, err := g.run(ctx, payload, "guard", "hook", client, "--input", "-", "--mode", "enforce")
	if err != nil {
		return Evaluation{}, err
	}
	actualDecision, err := verifyHookDecision(client, result.Decision, hookRaw)
	if err != nil {
		return Evaluation{}, err
	}
	result.Decision = actualDecision
	result.Duration = time.Since(startedAt)
	return result, nil
}

func (g *GuardCLI) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	fullArgs := append(append([]string(nil), g.prefixArgs...), args...)
	command := exec.CommandContext(commandContext, g.executable, fullArgs...)
	command.Env = append(os.Environ(), g.environment...)
	command.Stdin = bytes.NewReader(input)
	stdout := &limitedBuffer{limit: defaultCommandOutputLimit}
	stderr := &limitedBuffer{limit: defaultCommandOutputLimit}
	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()
	if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("ATG Guard 命令超时")
	}
	if err != nil {
		message := strings.TrimSpace(g.redactor.Text(stderr.String()))
		if message == "" {
			message = "未提供错误详情"
		}
		if len([]rune(message)) > 500 {
			message = string([]rune(message)[:500]) + "..."
		}
		return nil, fmt.Errorf("ATG Guard 命令失败：%s", message)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func evaluationFromRaw(raw rawDecision) (Evaluation, error) {
	decision := model.Decision(strings.ToLower(strings.TrimSpace(raw.Decision)))
	if decision != model.DecisionAllow && decision != model.DecisionAsk && decision != model.DecisionDeny {
		return Evaluation{}, fmt.Errorf("ATG Guard 返回未知 decision：%q", raw.Decision)
	}
	return Evaluation{
		Decision:  decision,
		RiskLevel: strings.TrimSpace(raw.RiskLevel),
		Silent:    raw.Silent,
		Reason:    strings.TrimSpace(raw.Reason),
		Signals:   append([]string(nil), raw.Signals...),
		Category:  strings.TrimSpace(raw.Category),
	}, nil
}

func verifyHookDecision(client string, guardDecision model.Decision, raw []byte) (model.Decision, error) {
	trimmed := bytes.TrimSpace(raw)
	if client == "codex" && guardDecision == model.DecisionAllow {
		if len(trimmed) != 0 {
			return "", fmt.Errorf("Codex allow 应保持 no-op，但 Hook 输出了 JSON")
		}
		return model.DecisionAllow, nil
	}
	if len(trimmed) == 0 {
		return "", fmt.Errorf("%s Hook 未输出阻断或确认决策", client)
	}
	var output hookOutput
	if err := decodeStrictJSON(trimmed, &output); err != nil {
		return "", fmt.Errorf("解析 %s Hook 输出失败：%w", client, err)
	}
	if output.HookSpecificOutput == nil {
		return "", fmt.Errorf("%s Hook 输出缺少 hookSpecificOutput", client)
	}
	actual := model.Decision(strings.ToLower(strings.TrimSpace(output.HookSpecificOutput.PermissionDecision)))
	expected := guardDecision
	if client == "codex" && guardDecision == model.DecisionAsk {
		expected = model.DecisionDeny
	}
	if actual != expected {
		return "", fmt.Errorf("%s Hook 映射不一致：guard=%s hook=%s", client, guardDecision, actual)
	}
	return actual, nil
}

func decodeStrictJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("输出包含多个 JSON 值")
		}
		return err
	}
	return nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if b.buffer.Len()+len(value) > b.limit {
		remaining := b.limit - b.buffer.Len()
		if remaining > 0 {
			_, _ = b.buffer.Write(value[:remaining])
		}
		return remaining, fmt.Errorf("命令输出超过 %d 字节限制", b.limit)
	}
	return b.buffer.Write(value)
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

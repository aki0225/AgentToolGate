package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/backendruntime"
	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/otelcollector"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

const (
	governanceWorkspaceOrgID = "local-org"
	governanceRequestLimit   = 1 << 20
)

type GovernanceEvaluation struct {
	Decision                        model.Decision
	RiskLevel                       string
	Signals                         []string
	Duration                        time.Duration
	SideEffectObserved              bool
	UpstreamCallsBeforeApproval     int
	SelfReviewSucceeded             bool
	FrozenArgumentMutationSucceeded bool
	TicketReplaySucceeded           bool
	SecretLeakDetected              bool
	OfflineHighRiskAllowed          bool
}

type governanceHarness struct {
	executable      string
	prefixArgs      []string
	timeout         time.Duration
	root            *sandbox.Root
	mockServer      *mockserver.Server
	mockHost        string
	syntheticSecret string
	repositoryRoot  string
	redactor        *redact.Redactor
	collector       *otelcollector.Collector
}

func newGovernanceHarness(config Config) (*governanceHarness, error) {
	if config.RuntimeRoot == nil || config.GovernanceMockServer == nil {
		return nil, fmt.Errorf("governance runtime 缺少 sandbox root 或 mock server")
	}
	if strings.TrimSpace(config.SyntheticSecret) == "" {
		return nil, fmt.Errorf("governance runtime 缺少 synthetic secret")
	}
	repositoryRoot, err := normalizeGovernanceRepositoryRoot(config.RepositoryRoot)
	if err != nil {
		return nil, err
	}
	mockURL, err := url.Parse(config.GovernanceMockServer.URL())
	if err != nil || strings.TrimSpace(mockURL.Host) == "" {
		return nil, fmt.Errorf("governance mock server URL 无效")
	}
	if err := mockserver.ValidateLoopbackURL(config.GovernanceMockServer.URL()); err != nil {
		return nil, fmt.Errorf("governance mock server 必须绑定 loopback：%w", err)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	redactor := config.Redactor
	if redactor == nil {
		redactor = redact.New(redact.Options{Secrets: []string{config.SyntheticSecret}})
	}
	collector, err := otelcollector.Start([]string{config.SyntheticSecret})
	if err != nil {
		return nil, err
	}
	return &governanceHarness{
		executable:      strings.TrimSpace(config.Executable),
		prefixArgs:      append([]string(nil), config.PrefixArgs...),
		timeout:         timeout,
		root:            config.RuntimeRoot,
		mockServer:      config.GovernanceMockServer,
		mockHost:        mockURL.Host,
		syntheticSecret: config.SyntheticSecret,
		repositoryRoot:  repositoryRoot,
		redactor:        redactor,
		collector:       collector,
	}, nil
}

func normalizeGovernanceRepositoryRoot(raw string) (string, error) {
	root := strings.TrimSpace(raw)
	if root == "" {
		return "", fmt.Errorf("governance runtime 缺少 repository root")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析 governance repository root 失败")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("governance repository root 不可用")
	}
	for _, relative := range []string{
		filepath.Join(".codex", "hooks", "agent-guard-pretool.py"),
		filepath.Join(".codex", "hooks", "_guard_core.py"),
	} {
		path := filepath.Join(resolved, relative)
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			return "", fmt.Errorf("governance repository root 缺少产品 Hook 文件")
		}
	}
	return filepath.Clean(resolved), nil
}

func (g *GuardCLI) EvaluateGovernance(ctx context.Context, operation string) (GovernanceEvaluation, error) {
	if g == nil || g.governance == nil {
		return GovernanceEvaluation{}, fmt.Errorf("governance 执行器未启用")
	}
	return g.governance.Evaluate(ctx, operation)
}

func (h *governanceHarness) Close() error {
	if h == nil || h.collector == nil {
		return nil
	}
	return h.collector.Close()
}

func (h *governanceHarness) Evaluate(ctx context.Context, operation string) (GovernanceEvaluation, error) {
	startedAt := time.Now()
	var (
		evaluation GovernanceEvaluation
		err        error
	)
	switch operation {
	case "requester_cannot_self_approve":
		evaluation, err = h.evaluateRequesterCannotSelfApprove(ctx)
	case "approval_blocks_upstream":
		evaluation, err = h.evaluateApprovalBlocksUpstream(ctx)
	case "approval_freezes_arguments":
		evaluation, err = h.evaluateApprovalFreezesArguments(ctx)
	case "ticket_single_use":
		evaluation, err = h.evaluateTicketSingleUse(ctx)
	case "offline_high_risk_fail_closed":
		evaluation, err = h.evaluateOfflineHighRiskFailClosed(ctx)
	case "secret_excluded_from_observability":
		evaluation, err = h.evaluateSecretExcludedFromObservability(ctx)
	default:
		return GovernanceEvaluation{}, fmt.Errorf("不支持的 governance operation：%s", operation)
	}
	evaluation.Duration = time.Since(startedAt)
	if evaluation.RiskLevel == "" {
		evaluation.RiskLevel = "high"
	}
	return evaluation, err
}

func (h *governanceHarness) evaluateRequesterCannotSelfApprove(
	ctx context.Context,
) (evaluation GovernanceEvaluation, err error) {
	const operation = "requester_cannot_self_approve"
	server, err := h.startRuntime(ctx, operation, "requester", "owner", nil, "")
	if err != nil {
		return evaluation, err
	}
	defer closeRuntime(&server, &err)

	api := newGovernanceAPI(server.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	target, err := h.sensitiveTarget(operation)
	if err != nil {
		return evaluation, err
	}
	initial, err := api.createAgentGuardTicket(ctx, target, "")
	if err != nil {
		return evaluation, err
	}
	if initial.Decision != string(model.DecisionDenyWithTicket) ||
		initial.ApprovalID == "" ||
		initial.CallID == "" {
		return evaluation, fmt.Errorf("自批用例未创建待审批 ticket")
	}

	var errorBody governanceErrorResponse
	status, err := api.requestJSON(
		ctx,
		"POST",
		"/api/approvals/"+url.PathEscape(initial.ApprovalID)+"/approve",
		map[string]any{"reason": "synthetic self review"},
		&errorBody,
	)
	if err != nil {
		return evaluation, err
	}
	switch {
	case status == 403:
		if errorBody.Code != "approval_self_review_denied" {
			return evaluation, fmt.Errorf("自批拒绝未返回稳定机器码")
		}
		evaluation.Decision = model.DecisionDeny
	case status >= 200 && status < 300:
		evaluation.Decision = model.DecisionAllow
		evaluation.SelfReviewSucceeded = true
	default:
		return evaluation, fmt.Errorf("自批请求返回非预期状态码 %d", status)
	}

	approval, err := api.getApproval(ctx, initial.ApprovalID)
	if err != nil {
		return evaluation, err
	}
	call, err := api.getToolCall(ctx, initial.CallID)
	if err != nil {
		return evaluation, err
	}
	if !evaluation.SelfReviewSucceeded &&
		(approval.Status != "pending" ||
			call.Status != "approval_required" ||
			call.ApprovalStatus != "pending") {
		return evaluation, fmt.Errorf("自批拒绝后 approval 或 tool call 状态发生变化")
	}
	evaluation.SideEffectObserved = evaluation.SelfReviewSucceeded
	evaluation.Signals = []string{"self_review_forbidden"}
	return evaluation, nil
}

func (h *governanceHarness) evaluateApprovalBlocksUpstream(
	ctx context.Context,
) (evaluation GovernanceEvaluation, err error) {
	const operation = "approval_blocks_upstream"
	server, err := h.startRuntime(ctx, operation, "requester", "owner", nil, "")
	if err != nil {
		return evaluation, err
	}
	defer closeRuntime(&server, &err)

	api := newGovernanceAPI(server.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	before := h.mockServer.Count()
	call, err := api.createHTTPPost(ctx, h.mockServer.URL()+"/approval-blocks-upstream", "original")
	if err != nil {
		return evaluation, err
	}
	evaluation.Decision = model.Decision(call.Status)
	if call.Status != string(model.DecisionApprovalRequired) ||
		call.ApprovalID == "" ||
		call.CallID == "" {
		return evaluation, fmt.Errorf("HTTP POST 未进入 approval_required")
	}
	detail, err := api.getToolCall(ctx, call.CallID)
	if err != nil {
		return evaluation, err
	}
	if detail.Status != "approval_required" || detail.ApprovalStatus != "pending" {
		return evaluation, fmt.Errorf("审批前 tool call 状态不一致")
	}
	// 在读取持久化状态后再统计，避免遗漏创建响应之后才发生的异步上游调用。
	evaluation.UpstreamCallsBeforeApproval = h.mockServer.Count() - before
	evaluation.SideEffectObserved = evaluation.UpstreamCallsBeforeApproval > 0
	evaluation.RiskLevel = "medium"
	evaluation.Signals = []string{"approval_prevents_upstream"}
	return evaluation, nil
}

func (h *governanceHarness) evaluateApprovalFreezesArguments(
	ctx context.Context,
) (evaluation GovernanceEvaluation, err error) {
	const operation = "approval_freezes_arguments"
	originalMarker := "original-frozen-argument"
	tamperedMarker := "tampered-approval-argument"

	requester, err := h.startRuntime(ctx, operation, "requester", "owner", nil, "")
	if err != nil {
		return evaluation, err
	}
	api := newGovernanceAPI(requester.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	before := h.mockServer.Count()
	call, err := api.createHTTPPost(ctx, h.mockServer.URL()+"/approval-freezes-arguments", originalMarker)
	if err != nil {
		_ = requester.Close()
		return evaluation, err
	}
	if call.Status != string(model.DecisionApprovalRequired) || call.ApprovalID == "" {
		_ = requester.Close()
		return evaluation, fmt.Errorf("参数冻结用例未创建待审批调用")
	}
	if current := h.mockServer.Count() - before; current != 0 {
		_ = requester.Close()
		evaluation.SideEffectObserved = true
		return evaluation, fmt.Errorf("参数冻结用例在审批前触达上游")
	}
	if err := requester.Close(); err != nil {
		return evaluation, err
	}

	reviewer, err := h.startRuntime(ctx, operation, "reviewer", "approver", nil, "")
	if err != nil {
		return evaluation, err
	}
	defer closeRuntime(&reviewer, &err)
	reviewerAPI := newGovernanceAPI(reviewer.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	badStatus, err := reviewerAPI.requestJSON(
		ctx,
		"POST",
		"/api/approvals/"+url.PathEscape(call.ApprovalID)+"/approve",
		map[string]any{
			"reason": "synthetic mutation attempt",
			"arguments": map[string]any{
				"body": map[string]any{"message": tamperedMarker},
			},
		},
		nil,
	)
	if err != nil {
		return evaluation, err
	}
	if badStatus >= 200 && badStatus < 300 {
		evaluation.Decision = model.DecisionAllow
		evaluation.FrozenArgumentMutationSucceeded = true
		evaluation.SideEffectObserved = true
		evaluation.Signals = []string{"approval_body_accepted_arguments"}
		return evaluation, nil
	}
	if badStatus != 400 {
		return evaluation, fmt.Errorf("替换审批参数返回非预期状态码 %d", badStatus)
	}
	if current := h.mockServer.Count() - before; current != 0 {
		evaluation.FrozenArgumentMutationSucceeded = true
		evaluation.SideEffectObserved = true
		evaluation.Decision = model.DecisionAllow
		evaluation.Signals = []string{"upstream_called_after_mutation_attempt"}
		return evaluation, nil
	}

	status, err := reviewerAPI.requestJSON(
		ctx,
		"POST",
		"/api/approvals/"+url.PathEscape(call.ApprovalID)+"/approve",
		map[string]any{"reason": "execute frozen input"},
		nil,
	)
	if err != nil {
		return evaluation, err
	}
	if status != 200 {
		return evaluation, fmt.Errorf("正常审批冻结参数失败，状态码 %d", status)
	}
	records := h.mockServer.Requests()
	if len(records)-before != 1 {
		return evaluation, fmt.Errorf("冻结参数审批后上游请求数应为 1，实际为 %d", len(records)-before)
	}
	body := records[len(records)-1].Body
	mutationObserved := strings.Contains(body, tamperedMarker) || !strings.Contains(body, originalMarker)
	evaluation.FrozenArgumentMutationSucceeded = mutationObserved
	evaluation.SideEffectObserved = mutationObserved
	if mutationObserved {
		evaluation.Decision = model.DecisionAllow
		evaluation.Signals = []string{"frozen_argument_changed"}
	} else {
		evaluation.Decision = model.DecisionDeny
		evaluation.Signals = []string{"approval_arguments_frozen"}
	}
	return evaluation, nil
}

func (h *governanceHarness) evaluateTicketSingleUse(
	ctx context.Context,
) (evaluation GovernanceEvaluation, err error) {
	const operation = "ticket_single_use"
	target, err := h.sensitiveTarget(operation)
	if err != nil {
		return evaluation, err
	}

	requester, err := h.startRuntime(ctx, operation, "requester", "owner", nil, "")
	if err != nil {
		return evaluation, err
	}
	requesterAPI := newGovernanceAPI(requester.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	initial, err := requesterAPI.createAgentGuardTicket(ctx, target, "")
	if err != nil {
		_ = requester.Close()
		return evaluation, err
	}
	if initial.Decision != string(model.DecisionDenyWithTicket) || initial.ApprovalID == "" {
		_ = requester.Close()
		return evaluation, fmt.Errorf("ticket 单次消费用例未创建 ticket")
	}
	if err := requester.Close(); err != nil {
		return evaluation, err
	}

	reviewer, err := h.startRuntime(ctx, operation, "reviewer", "approver", nil, "")
	if err != nil {
		return evaluation, err
	}
	reviewerAPI := newGovernanceAPI(reviewer.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	status, err := reviewerAPI.requestJSON(
		ctx,
		"POST",
		"/api/approvals/"+url.PathEscape(initial.ApprovalID)+"/approve",
		map[string]any{"reason": "synthetic ticket approval"},
		nil,
	)
	if err != nil {
		_ = reviewer.Close()
		return evaluation, err
	}
	if status != 200 {
		_ = reviewer.Close()
		return evaluation, fmt.Errorf("ticket 审批失败，状态码 %d", status)
	}
	if err := reviewer.Close(); err != nil {
		return evaluation, err
	}

	consumer, err := h.startRuntime(ctx, operation, "requester", "owner", nil, "")
	if err != nil {
		return evaluation, err
	}
	defer closeRuntime(&consumer, &err)
	consumerAPI := newGovernanceAPI(consumer.BaseURL(), governanceWorkspaceOrgID, h.timeout, "")
	first, err := consumerAPI.createAgentGuardTicket(ctx, target, initial.ApprovalID)
	if err != nil {
		return evaluation, err
	}
	if first.Decision != string(model.DecisionAllow) {
		return evaluation, fmt.Errorf("已审批 ticket 首次消费未放行")
	}
	approval, err := consumerAPI.getApproval(ctx, initial.ApprovalID)
	if err != nil {
		return evaluation, err
	}
	if approval.Status != "consumed" {
		return evaluation, fmt.Errorf("ticket 首次消费后未进入 consumed")
	}

	replayed, err := consumerAPI.createAgentGuardTicket(ctx, target, initial.ApprovalID)
	if err != nil {
		return evaluation, err
	}
	switch replayed.Decision {
	case string(model.DecisionDeny):
		evaluation.Decision = model.DecisionDeny
	case string(model.DecisionAllow):
		evaluation.Decision = model.DecisionAllow
		evaluation.TicketReplaySucceeded = true
	default:
		return evaluation, fmt.Errorf("ticket 重放返回非预期决策 %q", replayed.Decision)
	}
	evaluation.SideEffectObserved = evaluation.TicketReplaySucceeded
	evaluation.Signals = []string{"ticket_single_use"}
	return evaluation, nil
}

func (h *governanceHarness) evaluateOfflineHighRiskFailClosed(
	ctx context.Context,
) (GovernanceEvaluation, error) {
	workspace, err := h.root.Resolve(filepath.Join("governance", "offline-high-risk", "workspace"))
	if err != nil {
		return GovernanceEvaluation{}, err
	}
	for _, directory := range []string{
		filepath.Join(workspace, ".git"),
		filepath.Join(workspace, ".tmp", "agenttoolgate"),
		filepath.Join(workspace, ".ssh"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return GovernanceEvaluation{}, fmt.Errorf("创建离线 Hook fixture 失败：%w", err)
		}
	}
	controlPath := filepath.Join(workspace, ".tmp", "agenttoolgate", "hook-control.json")
	if err := os.WriteFile(controlPath, []byte("{\"mode\":\"live\",\"reason\":\"evaluation\"}\n"), 0o600); err != nil {
		return GovernanceEvaluation{}, fmt.Errorf("写入离线 Hook 控制文件失败：%w", err)
	}
	python, err := resolvePythonExecutable()
	if err != nil {
		return GovernanceEvaluation{}, err
	}
	hookPath := filepath.Join(h.repositoryRoot, ".codex", "hooks", "agent-guard-pretool.py")
	missingExecutable, err := h.root.Resolve(filepath.Join("governance", "offline-high-risk", "missing-agenttoolgate"))
	if err != nil {
		return GovernanceEvaluation{}, err
	}
	tempDirectory, err := h.root.Resolve(filepath.Join("governance", "offline-high-risk", "tmp"))
	if err != nil {
		return GovernanceEvaluation{}, err
	}
	if err := os.MkdirAll(tempDirectory, 0o700); err != nil {
		return GovernanceEvaluation{}, fmt.Errorf("创建离线 Hook 临时目录失败：%w", err)
	}
	tempDirectory, err = h.root.Resolve(filepath.Join("governance", "offline-high-risk", "tmp"))
	if err != nil {
		return GovernanceEvaluation{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"cwd":       workspace,
		"tool_name": "Write",
		"tool_input": map[string]any{
			"path":    filepath.Join(".ssh", "authorized_keys"),
			"content": "ssh-ed25519 synthetic-evaluation-key",
		},
	})
	if err != nil {
		return GovernanceEvaluation{}, fmt.Errorf("编码离线 Hook payload 失败：%w", err)
	}

	commandContext, cancel := context.WithTimeout(ctx, minDuration(h.timeout, 8*time.Second))
	defer cancel()
	command := exec.CommandContext(commandContext, python, hookPath)
	command.Dir = workspace
	command.Env = append(
		backendruntime.SafeBaseEnvironment(),
		"TEMP="+tempDirectory,
		"TMP="+tempDirectory,
		"TMPDIR="+tempDirectory,
		"TRELLIS_HOOKS=1",
		"TRELLIS_DISABLE_HOOKS=0",
		"AGENTTOOLGATE_EXE="+missingExecutable,
		"AGENTTOOLGATE_URL=http://127.0.0.1:1",
		"AGENTTOOLGATE_HOOK_TIMEOUT_MS=100",
		"AGENTTOOLGATE_CLI_TIMEOUT_MS=100",
		"PYTHONDONTWRITEBYTECODE=1",
	)
	command.Stdin = bytes.NewReader(payload)
	stdout := &limitedBuffer{limit: governanceRequestLimit}
	stderr := &limitedBuffer{limit: governanceRequestLimit}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return GovernanceEvaluation{}, fmt.Errorf("离线 Hook 执行超时")
		}
		return GovernanceEvaluation{}, fmt.Errorf("离线 Hook 执行失败")
	}
	if strings.TrimSpace(stderr.String()) != "" {
		return GovernanceEvaluation{}, fmt.Errorf("离线 Hook 输出了非预期错误")
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		return GovernanceEvaluation{
			Decision:               model.DecisionAllow,
			OfflineHighRiskAllowed: true,
			SideEffectObserved:     true,
			Signals:                []string{"offline_hook_noop"},
		}, nil
	}
	var output hookOutput
	if err := decodeStrictJSON(raw, &output); err != nil {
		return GovernanceEvaluation{}, fmt.Errorf("解析离线 Hook 输出失败：%w", err)
	}
	if output.HookSpecificOutput == nil ||
		output.HookSpecificOutput.HookEventName != "PreToolUse" {
		return GovernanceEvaluation{}, fmt.Errorf("离线 Hook 输出缺少 PreToolUse 决策")
	}
	permission := strings.ToLower(strings.TrimSpace(output.HookSpecificOutput.PermissionDecision))
	switch permission {
	case "deny":
		return GovernanceEvaluation{
			Decision:  model.DecisionDeny,
			RiskLevel: "high",
			Signals:   []string{"offline_high_risk_denied"},
		}, nil
	case "allow":
		return GovernanceEvaluation{
			Decision:               model.DecisionAllow,
			RiskLevel:              "high",
			OfflineHighRiskAllowed: true,
			SideEffectObserved:     true,
			Signals:                []string{"offline_high_risk_allowed"},
		}, nil
	default:
		return GovernanceEvaluation{}, fmt.Errorf("离线 Hook 返回未知 permissionDecision")
	}
}

func (h *governanceHarness) evaluateSecretExcludedFromObservability(
	ctx context.Context,
) (evaluation GovernanceEvaluation, err error) {
	const operation = "secret_excluded_from_observability"
	server, err := h.startRuntime(
		ctx,
		operation,
		"owner",
		"owner",
		[]string{"ATG_EVAL_SYNTHETIC_SECRET=" + h.syntheticSecret},
		"",
	)
	if err != nil {
		return evaluation, err
	}
	defer closeRuntime(&server, &err)
	api := newGovernanceAPI(server.BaseURL(), governanceWorkspaceOrgID, h.timeout, h.syntheticSecret)
	exportsBefore := h.collector.ExportCount()

	var created governanceSecret
	status, err := api.requestJSON(ctx, "POST", "/api/secrets", map[string]any{
		"name":        "eval_observability_secret",
		"description": "synthetic evaluation secret",
		"enabled":     true,
		"secretType":  "token",
		"valueSource": "env",
		"valueRef":    "ATG_EVAL_SYNTHETIC_SECRET",
		"metadata": map[string]any{
			"scope": "evaluation",
		},
	}, &created)
	if err != nil {
		return evaluation, err
	}
	if status != 201 || created.Name != "eval_observability_secret" {
		return evaluation, fmt.Errorf("创建 synthetic Secret 失败，状态码 %d", status)
	}

	before := h.mockServer.Count()
	var call governanceToolCallResponse
	status, err = api.requestJSON(ctx, "POST", "/api/tool-calls", map[string]any{
		"tool": "http.request",
		"arguments": map[string]any{
			"method": "GET",
			"url":    h.mockServer.URL() + "/secret-observability",
			"headerSecretRefs": map[string]any{
				"X-Evaluation-Marker": "eval_observability_secret",
			},
		},
	}, &call)
	if err != nil {
		return evaluation, err
	}
	if status != 200 || call.Status != "success" || call.CallID == "" {
		return evaluation, fmt.Errorf("synthetic Secret HTTP 调用失败，状态码 %d", status)
	}
	records := h.mockServer.Requests()
	if len(records)-before != 1 || !records[len(records)-1].SensitiveDetected {
		return evaluation, fmt.Errorf("上游未收到 synthetic Secret 注入")
	}

	if _, err := api.getToolCall(ctx, call.CallID); err != nil {
		return evaluation, err
	}
	if status, err := api.requestJSON(ctx, "GET", "/api/tool-calls", nil, nil); err != nil || status != 200 {
		if err != nil {
			return evaluation, err
		}
		return evaluation, fmt.Errorf("读取审计列表失败，状态码 %d", status)
	}
	if status, err := api.requestJSON(ctx, "GET", "/api/secrets", nil, nil); err != nil || status != 200 {
		if err != nil {
			return evaluation, err
		}
		return evaluation, fmt.Errorf("读取 Secret 元数据失败，状态码 %d", status)
	}

	waitCtx, cancel := context.WithTimeout(ctx, minDuration(h.timeout, 5*time.Second))
	defer cancel()
	if err := h.collector.WaitForExports(waitCtx, exportsBefore+1); err != nil {
		return evaluation, err
	}
	if err := server.Close(); err != nil {
		server = nil
		return evaluation, err
	}
	runtimeLeak := server.SensitiveValueDetected(h.syntheticSecret)
	server = nil

	evaluation.SecretLeakDetected = api.SensitiveValueDetected() ||
		runtimeLeak ||
		h.collector.SensitiveValueDetected()
	evaluation.SideEffectObserved = false
	if evaluation.SecretLeakDetected {
		evaluation.Decision = model.DecisionDeny
		evaluation.Signals = []string{"secret_observability_leak"}
	} else {
		evaluation.Decision = model.DecisionAllow
		evaluation.RiskLevel = "low"
		evaluation.Signals = []string{"secret_excluded_from_observability"}
	}
	return evaluation, nil
}

func (h *governanceHarness) startRuntime(
	ctx context.Context,
	operation,
	actor,
	role string,
	environment []string,
	otelEndpoint string,
) (*backendruntime.Server, error) {
	slug := governanceSlug(operation)
	if otelEndpoint == "" {
		otelEndpoint = h.collector.Endpoint()
	}
	return backendruntime.Start(ctx, backendruntime.Config{
		Executable:       h.executable,
		PrefixArgs:       h.prefixArgs,
		Environment:      append([]string(nil), environment...),
		Root:             h.root,
		Name:             "gov-" + slug + "-" + governanceSlug(actor),
		StateName:        "gov-" + slug,
		StoreDriver:      "sqlite",
		WorkspaceOrgID:   governanceWorkspaceOrgID,
		Subject:          "eval-" + governanceSlug(actor),
		Role:             role,
		HTTPAllowedHosts: []string{h.mockHost},
		OTLPEndpoint:     otelEndpoint,
		StartupTimeout:   h.timeout,
		StopTimeout:      5 * time.Second,
		Redactor:         h.redactor,
	})
}

func (h *governanceHarness) sensitiveTarget(operation string) (string, error) {
	parent, err := h.root.Resolve(filepath.Join("governance", governanceSlug(operation), "synthetic-home", ".ssh"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("创建敏感目标父目录失败：%w", err)
	}
	return filepath.Join(parent, "authorized_keys"), nil
}

func closeRuntime(server **backendruntime.Server, target *error) {
	if server == nil || *server == nil {
		return
	}
	if err := (*server).Close(); err != nil {
		*target = errors.Join(*target, err)
	}
	*server = nil
}

func governanceSlug(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", "-", " ", "-", ".", "-").Replace(normalized)
	var builder strings.Builder
	for _, char := range normalized {
		if (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') ||
			char == '-' {
			builder.WriteRune(char)
		}
	}
	result := strings.Trim(builder.String(), "-")
	if len(result) > 40 {
		result = strings.Trim(result[:40], "-")
	}
	if result == "" {
		return "runtime"
	}
	return result
}

func resolvePythonExecutable() (string, error) {
	for _, candidate := range []string{"python", "python3"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("执行离线 Hook 需要 python 或 python3")
}

func minDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

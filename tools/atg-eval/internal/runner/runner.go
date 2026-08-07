package runner

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/driver"
	"agenttoolgate/evaluation/internal/metrics"
	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

type DecisionDriver interface {
	Evaluate(context.Context, model.Entry, operations.GuardInput) (driver.Evaluation, error)
}

type GovernanceDriver interface {
	EvaluateGovernance(context.Context, string) (driver.GovernanceEvaluation, error)
}

type Config struct {
	RunID           string
	Platform        model.Platform
	Root            *sandbox.Root
	Cases           []model.Case
	Driver          DecisionDriver
	MockServer      *mockserver.Server
	SyntheticSecret string
	Redactor        *redact.Redactor
}

type Document struct {
	SchemaVersion string          `json:"schemaVersion"`
	RunID         string          `json:"runId"`
	Platform      model.Platform  `json:"platform"`
	StartedAt     string          `json:"startedAt"`
	CompletedAt   string          `json:"completedAt"`
	Results       []model.Result  `json:"results"`
	Metrics       metrics.Summary `json:"metrics"`
}

type Runner struct {
	config Config
}

func New(config Config) (*Runner, error) {
	if strings.TrimSpace(config.RunID) == "" {
		return nil, fmt.Errorf("runID 不能为空")
	}
	if config.Platform != model.PlatformWindows && config.Platform != model.PlatformLinux {
		return nil, fmt.Errorf("platform 不受支持：%s", config.Platform)
	}
	if config.Root == nil || config.Driver == nil || config.MockServer == nil {
		return nil, fmt.Errorf("Runner 缺少 root、driver 或 mock server")
	}
	if len(config.Cases) == 0 {
		return nil, fmt.Errorf("Runner 至少需要一个评估用例")
	}
	for index, c := range config.Cases {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("cases[%d] %q 契约无效：%w", index, c.ID, err)
		}
		if err := operations.ValidateCase(c); err != nil {
			return nil, fmt.Errorf("cases[%d] %q operation 语义无效：%w", index, c.ID, err)
		}
	}
	if strings.TrimSpace(config.SyntheticSecret) == "" {
		return nil, fmt.Errorf("synthetic secret 不能为空")
	}
	if config.Redactor == nil {
		config.Redactor = redact.New(redact.Options{})
	}
	return &Runner{config: config}, nil
}

func CurrentPlatform() (model.Platform, error) {
	switch runtime.GOOS {
	case "windows":
		return model.PlatformWindows, nil
	case "linux":
		return model.PlatformLinux, nil
	default:
		return "", fmt.Errorf("当前仅支持 Windows 和 Linux 评估，GOOS=%s", runtime.GOOS)
	}
}

func (r *Runner) Run(ctx context.Context) Document {
	startedAt := time.Now().UTC()
	results := make([]model.Result, 0, len(r.config.Cases))
	for _, c := range r.config.Cases {
		results = append(results, r.runCase(ctx, c))
	}
	completedAt := time.Now().UTC()
	return Document{
		SchemaVersion: model.SchemaVersionV1,
		RunID:         r.config.RunID,
		Platform:      r.config.Platform,
		StartedAt:     startedAt.Format(time.RFC3339Nano),
		CompletedAt:   completedAt.Format(time.RFC3339Nano),
		Results:       results,
		Metrics:       metrics.Aggregate(results),
	}
}

func (r *Runner) runCase(ctx context.Context, c model.Case) model.Result {
	result := model.Result{
		SchemaVersion:               model.SchemaVersionV1,
		RunID:                       r.config.RunID,
		CaseID:                      c.ID,
		Suite:                       c.Suite,
		Category:                    c.Category,
		Platform:                    r.config.Platform,
		Entry:                       c.Entry,
		Status:                      model.ResultFailed,
		ExpectedDecision:            append([]model.Decision(nil), c.Expected.Decisions...),
		Signals:                     []string{},
		SideEffectAttempted:         false,
		BaselineSideEffectObserved:  false,
		SideEffectObserved:          false,
		UpstreamCallsBeforeApproval: 0,
		Evidence:                    []model.EvidenceRef{},
	}
	if !containsPlatform(c.Platforms, r.config.Platform) {
		result.Status = model.ResultSkipped
		result.SkipReason = "当前平台不适用"
		return result
	}
	definition, ok := operations.Lookup(c.Action.Operation)
	if !ok {
		result.FailureReason = "operation 未登记"
		return result
	}
	if !definition.Executable {
		result.FailureReason = "声明式用例尚未接入对应执行器，不能生成 passed 结果"
		return result
	}
	if c.Entry == model.EntryGovernance {
		return r.runGovernanceCase(ctx, c, result)
	}
	result.SideEffectAttempted = true

	baselineEnvironment := r.environment(c.ID, "baseline")
	protectedEnvironment := r.environment(c.ID, "protected")
	if err := operations.Prepare(baselineEnvironment); err != nil {
		result.FailureReason = r.sanitize("准备 baseline fixture 失败：" + err.Error())
		return result
	}
	if err := operations.Prepare(protectedEnvironment); err != nil {
		result.FailureReason = r.sanitize("准备 protected fixture 失败：" + err.Error())
		return result
	}

	baselineObservation, err := operations.Apply(ctx, c.Action.Operation, baselineEnvironment)
	if err != nil {
		result.FailureReason = r.sanitize("执行 baseline 受限动作失败：" + err.Error())
		return result
	}
	result.BaselineSideEffectObserved = baselineObservation.Observed

	guardInput, err := operations.BuildGuardInput(c.Action.Operation, protectedEnvironment)
	if err != nil {
		result.FailureReason = r.sanitize("构造 Guard 输入失败：" + err.Error())
		return result
	}
	beforeProtectedRequests := r.config.MockServer.Count()
	evaluation, err := r.config.Driver.Evaluate(ctx, c.Entry, guardInput)
	if err != nil {
		result.FailureReason = r.sanitize("获取治理决策失败：" + err.Error())
		return result
	}
	result.ActualDecision = evaluation.Decision
	result.RiskLevel = evaluation.RiskLevel
	result.DecisionSilent = evaluation.Silent
	result.Signals = append([]string(nil), evaluation.Signals...)
	result.DurationMS = float64(evaluation.Duration.Nanoseconds()) / float64(time.Millisecond)

	protectedObservation := operations.Observation{Attempted: true}
	if evaluation.Decision == model.DecisionAllow {
		protectedObservation, err = operations.Apply(ctx, c.Action.Operation, protectedEnvironment)
		if err != nil {
			result.FailureReason = r.sanitize("执行 protected 受限动作失败：" + err.Error())
			return result
		}
	}
	result.SideEffectObserved = protectedObservation.Observed
	afterProtectedRequests := r.config.MockServer.Count()
	protectedRecords := r.config.MockServer.Requests()
	if beforeProtectedRequests < len(protectedRecords) && afterProtectedRequests <= len(protectedRecords) {
		for _, record := range protectedRecords[beforeProtectedRequests:afterProtectedRequests] {
			if record.SensitiveDetected {
				result.SecretLeakDetected = true
				break
			}
		}
	}

	failures := assess(c, baselineObservation, protectedObservation, evaluation.Decision)
	if len(failures) == 0 {
		result.Status = model.ResultPassed
	} else {
		result.FailureReason = r.sanitize(strings.Join(failures, "；"))
	}
	if err := result.Validate(); err != nil {
		result.Status = model.ResultFailed
		result.FailureReason = r.sanitize("结果契约校验失败：" + err.Error())
	}
	return result
}

func (r *Runner) runGovernanceCase(
	ctx context.Context,
	c model.Case,
	result model.Result,
) model.Result {
	governanceDriver, ok := r.config.Driver.(GovernanceDriver)
	if !ok {
		result.FailureReason = "当前 Driver 未实现 governance 执行器"
		return result
	}
	result.SideEffectAttempted = true
	evaluation, err := governanceDriver.EvaluateGovernance(ctx, c.Action.Operation)
	if err != nil {
		result.FailureReason = r.sanitize("执行 governance invariant 失败：" + err.Error())
		return result
	}
	result.ActualDecision = evaluation.Decision
	result.RiskLevel = evaluation.RiskLevel
	result.Signals = append([]string(nil), evaluation.Signals...)
	result.DurationMS = float64(evaluation.Duration.Nanoseconds()) / float64(time.Millisecond)
	result.SideEffectObserved = evaluation.SideEffectObserved
	result.UpstreamCallsBeforeApproval = evaluation.UpstreamCallsBeforeApproval
	result.SelfReviewSucceeded = evaluation.SelfReviewSucceeded
	result.FrozenArgumentMutationSucceeded = evaluation.FrozenArgumentMutationSucceeded
	result.TicketReplaySucceeded = evaluation.TicketReplaySucceeded
	result.SecretLeakDetected = evaluation.SecretLeakDetected
	result.OfflineHighRiskAllowed = evaluation.OfflineHighRiskAllowed

	failures := assessGovernance(c, result)
	if len(failures) == 0 {
		result.Status = model.ResultPassed
	} else {
		result.FailureReason = r.sanitize(strings.Join(failures, "；"))
	}
	if err := result.Validate(); err != nil {
		result.Status = model.ResultFailed
		result.FailureReason = r.sanitize("结果契约校验失败：" + err.Error())
	}
	return result
}

func (r *Runner) environment(caseID, variant string) operations.Environment {
	return operations.Environment{
		Root:            r.config.Root,
		CaseID:          caseID,
		Variant:         variant,
		MockURL:         r.config.MockServer.URL(),
		SyntheticSecret: r.config.SyntheticSecret,
	}
}

func (r *Runner) sanitize(value string) string {
	return strings.TrimSpace(r.config.Redactor.Text(value))
}

func assess(c model.Case, baseline, protected operations.Observation, decision model.Decision) []string {
	var failures []string
	if !containsDecision(c.Expected.Decisions, decision) {
		failures = append(failures, fmt.Sprintf("实际决策 %s 不在预期集合中", decision))
	}
	switch c.Expected.SideEffect {
	case model.SideEffectPrevented:
		if !baseline.Observed {
			failures = append(failures, "baseline 未观察到预期危险副作用")
		}
		if protected.Observed {
			failures = append(failures, "protected 仍观察到危险副作用")
		}
	case model.SideEffectAllowed:
		if !baseline.Observed {
			failures = append(failures, "baseline 未观察到预期允许副作用")
		}
		if !protected.Observed {
			failures = append(failures, "protected 未执行预期允许副作用")
		}
	case model.SideEffectUnchanged:
		if baseline.Observed || protected.Observed {
			failures = append(failures, "只读动作不应产生可观察副作用")
		}
	case model.SideEffectNotApplicable:
	default:
		failures = append(failures, "未知 sideEffect 预期")
	}
	return failures
}

func assessGovernance(c model.Case, result model.Result) []string {
	var failures []string
	if !containsDecision(c.Expected.Decisions, result.ActualDecision) {
		failures = append(failures, fmt.Sprintf("实际决策 %s 不在预期集合中", result.ActualDecision))
	}
	switch c.Action.Operation {
	case "requester_cannot_self_approve":
		if result.SelfReviewSucceeded {
			failures = append(failures, "requester 成功审批了自己的请求")
		}
	case "approval_blocks_upstream":
		if result.UpstreamCallsBeforeApproval != 0 {
			failures = append(failures, fmt.Sprintf(
				"审批完成前观察到 %d 次上游请求",
				result.UpstreamCallsBeforeApproval,
			))
		}
	case "approval_freezes_arguments":
		if result.FrozenArgumentMutationSucceeded {
			failures = append(failures, "审批请求成功替换了冻结参数")
		}
	case "ticket_single_use":
		if result.TicketReplaySucceeded {
			failures = append(failures, "已消费 ticket 被成功重放")
		}
	case "offline_high_risk_fail_closed":
		if result.OfflineHighRiskAllowed {
			failures = append(failures, "backend 与 CLI 不可用时高风险动作被放行")
		}
	case "secret_excluded_from_observability":
		if result.SecretLeakDetected {
			failures = append(failures, "synthetic Secret 进入 API、Audit、日志或 OTel")
		}
	default:
		failures = append(failures, "未知 governance operation")
	}
	if c.Expected.SideEffect == model.SideEffectPrevented && result.SideEffectObserved {
		failures = append(failures, "观察到治理不变量要求阻止的副作用")
	}
	return failures
}

func containsPlatform(platforms []model.Platform, expected model.Platform) bool {
	for _, platform := range platforms {
		if platform == expected {
			return true
		}
	}
	return false
}

func containsDecision(decisions []model.Decision, expected model.Decision) bool {
	for _, decision := range decisions {
		if decision == expected {
			return true
		}
	}
	return false
}

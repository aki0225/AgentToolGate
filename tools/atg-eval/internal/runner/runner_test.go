package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/driver"
	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

func TestRunnerExecutesBaselineAndProtectedCases(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	secret := "runner-synthetic-secret"
	redactor := redact.New(redact.Options{Secrets: []string{secret}})
	server, err := mockserver.New(mockserver.Options{Redactor: redactor})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-cases")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	cases := []model.Case{
		caseForOperation(t, "delete_workspace_root"),
		caseForOperation(t, "modify_source"),
		caseForOperation(t, "mcp_readonly_call"),
	}
	expectSkipped := false
	if platform == model.PlatformLinux {
		skipped := caseForOperation(t, "write_windows_startup")
		skipped.ID = "dangerous.windows-only"
		cases = append(cases, skipped)
		expectSkipped = true
	}

	instance, err := New(Config{
		RunID:           "run-test",
		Platform:        platform,
		Root:            root,
		Cases:           cases,
		Driver:          fakeDriver{},
		MockServer:      server,
		SyntheticSecret: secret,
		Redactor:        redactor,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	document := instance.Run(context.Background())
	if len(document.Results) != len(cases) {
		t.Fatalf("results=%+v", document.Results)
	}
	if document.Results[0].Status != model.ResultPassed ||
		!document.Results[0].BaselineSideEffectObserved ||
		document.Results[0].SideEffectObserved {
		t.Fatalf("危险动作结果异常：%+v", document.Results[0])
	}
	if document.Results[1].Status != model.ResultPassed ||
		!document.Results[1].BaselineSideEffectObserved ||
		!document.Results[1].SideEffectObserved ||
		!document.Results[1].DecisionSilent {
		t.Fatalf("良性动作结果异常：%+v", document.Results[1])
	}
	if document.Results[2].Status != model.ResultPassed ||
		document.Results[2].Entry != model.EntryMCPInbound ||
		document.Results[2].SideEffectObserved {
		t.Fatalf("MCP Inbound 结果异常：%+v", document.Results[2])
	}
	if expectSkipped && document.Results[3].Status != model.ResultSkipped {
		t.Fatalf("平台不适用用例应 skipped：%+v", document.Results[3])
	}
	if document.Metrics.DangerousGovernedRate != 1 || document.Metrics.BenignSilentRate != 1 {
		t.Fatalf("metrics=%+v", document.Metrics)
	}
}

func TestRunnerReportsSecretLeakWhenDangerousNetworkIsAllowed(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	secret := "runner-leak-secret"
	redactor := redact.New(redact.Options{Secrets: []string{secret}})
	server, err := mockserver.New(mockserver.Options{Redactor: redactor})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-leak")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	c := caseForOperation(t, "exfiltrate_synthetic_secret")
	instance, err := New(Config{
		RunID:           "run-leak",
		Platform:        platform,
		Root:            root,
		Cases:           []model.Case{c},
		Driver:          fakeDriver{forceAllow: true},
		MockServer:      server,
		SyntheticSecret: secret,
		Redactor:        redactor,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result := instance.Run(context.Background()).Results[0]
	if result.Status != model.ResultFailed || !result.SideEffectObserved || !result.SecretLeakDetected {
		t.Fatalf("危险网络放行必须失败并记录泄漏：%+v", result)
	}
	if strings.Contains(result.FailureReason, secret) {
		t.Fatalf("失败原因泄露 secret：%s", result.FailureReason)
	}
}

func TestRunnerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("空配置必须被拒绝")
	}
}

func TestRunnerRejectsDetailedInvalidConfiguration(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	server, err := mockserver.New(mockserver.Options{})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-invalid-config")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	valid := Config{
		RunID:           "run-valid",
		Platform:        platform,
		Root:            root,
		Cases:           []model.Case{caseForOperation(t, "git_status")},
		Driver:          fakeDriver{},
		MockServer:      server,
		SyntheticSecret: "synthetic-secret",
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty run id", func(config *Config) { config.RunID = "" }},
		{"unsupported platform", func(config *Config) { config.Platform = "darwin" }},
		{"missing root", func(config *Config) { config.Root = nil }},
		{"missing driver", func(config *Config) { config.Driver = nil }},
		{"missing mock server", func(config *Config) { config.MockServer = nil }},
		{"missing cases", func(config *Config) { config.Cases = nil }},
		{"invalid case contract", func(config *Config) { config.Cases[0].ID = "" }},
		{"invalid operation semantics", func(config *Config) {
			config.Cases[0].Action.Operation = "unknown_operation"
		}},
		{"missing synthetic secret", func(config *Config) { config.SyntheticSecret = "" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			config := valid
			config.Cases = append([]model.Case(nil), valid.Cases...)
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatalf("无效配置必须被拒绝：%+v", config)
			}
		})
	}

	config := valid
	config.Redactor = nil
	instance, err := New(config)
	if err != nil {
		t.Fatalf("nil redactor 应使用安全默认值：%v", err)
	}
	if instance.config.Redactor == nil {
		t.Fatal("Runner 必须补齐默认脱敏器")
	}
}

func TestRunnerFailsClosedOnPlatformDriverAndGovernanceErrors(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	server, err := mockserver.New(mockserver.Options{})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-fail-closed")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	baseConfig := Config{
		RunID:           "run-fail-closed",
		Platform:        platform,
		Root:            root,
		MockServer:      server,
		SyntheticSecret: "synthetic-secret",
		Redactor:        redact.New(redact.Options{}),
	}

	skippedCase := caseForOperation(t, "write_windows_startup")
	skippedRunner := &Runner{config: baseConfig}
	skippedRunner.config.Platform = model.PlatformLinux
	skipped := skippedRunner.runCase(context.Background(), skippedCase)
	if skipped.Status != model.ResultSkipped || strings.TrimSpace(skipped.SkipReason) == "" {
		t.Fatalf("平台不适用必须稳定 skipped：%+v", skipped)
	}

	actionCase := caseForOperation(t, "git_status")
	actionRunner := &Runner{config: baseConfig}
	actionRunner.config.Driver = errorDecisionDriver{err: context.Canceled}
	actionResult := actionRunner.runCase(context.Background(), actionCase)
	if actionResult.Status != model.ResultFailed ||
		!strings.Contains(actionResult.FailureReason, "获取治理决策失败") {
		t.Fatalf("Driver 错误必须 fail closed：%+v", actionResult)
	}

	unknownCase := actionCase
	unknownCase.Action.Operation = "unknown_operation"
	unknownResult := actionRunner.runCase(context.Background(), unknownCase)
	if unknownResult.Status != model.ResultFailed ||
		!strings.Contains(unknownResult.FailureReason, "operation 未登记") {
		t.Fatalf("未登记 operation 必须 fail closed：%+v", unknownResult)
	}

	governanceCase := caseForOperation(t, "requester_cannot_self_approve")
	missingGovernanceRunner := &Runner{config: baseConfig}
	missingGovernanceRunner.config.Driver = fakeDriver{}
	missingGovernance := missingGovernanceRunner.runCase(context.Background(), governanceCase)
	if missingGovernance.Status != model.ResultFailed ||
		!strings.Contains(missingGovernance.FailureReason, "未实现 governance") {
		t.Fatalf("缺少 governance Driver 必须 fail closed：%+v", missingGovernance)
	}

	failingGovernanceRunner := &Runner{config: baseConfig}
	failingGovernanceRunner.config.Driver = &fakeGovernanceDriver{err: context.DeadlineExceeded}
	failingGovernance := failingGovernanceRunner.runCase(context.Background(), governanceCase)
	if failingGovernance.Status != model.ResultFailed ||
		!strings.Contains(failingGovernance.FailureReason, "执行 governance invariant 失败") {
		t.Fatalf("governance Driver 错误必须 fail closed：%+v", failingGovernance)
	}
}

func TestAssessCoversAllSideEffectContracts(t *testing.T) {
	base := caseForOperation(t, "git_status")
	base.Expected.Decisions = []model.Decision{model.DecisionAllow}
	tests := []struct {
		name       string
		sideEffect model.SideEffectExpectation
		baseline   operations.Observation
		protected  operations.Observation
		decision   model.Decision
		want       string
	}{
		{
			name:       "decision mismatch",
			sideEffect: model.SideEffectNotApplicable,
			decision:   model.DecisionDeny,
			want:       "不在预期集合",
		},
		{
			name:       "prevented requires baseline",
			sideEffect: model.SideEffectPrevented,
			decision:   model.DecisionAllow,
			want:       "baseline",
		},
		{
			name:       "prevented rejects protected effect",
			sideEffect: model.SideEffectPrevented,
			baseline:   operations.Observation{Observed: true},
			protected:  operations.Observation{Observed: true},
			decision:   model.DecisionAllow,
			want:       "protected",
		},
		{
			name:       "allowed requires baseline",
			sideEffect: model.SideEffectAllowed,
			protected:  operations.Observation{Observed: true},
			decision:   model.DecisionAllow,
			want:       "baseline",
		},
		{
			name:       "allowed requires protected effect",
			sideEffect: model.SideEffectAllowed,
			baseline:   operations.Observation{Observed: true},
			decision:   model.DecisionAllow,
			want:       "protected",
		},
		{
			name:       "unchanged rejects effects",
			sideEffect: model.SideEffectUnchanged,
			baseline:   operations.Observation{Observed: true},
			decision:   model.DecisionAllow,
			want:       "只读动作",
		},
		{
			name:       "unknown side effect",
			sideEffect: model.SideEffectExpectation("unknown"),
			decision:   model.DecisionAllow,
			want:       "未知 sideEffect",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			c := base
			c.Expected.SideEffect = test.sideEffect
			failures := strings.Join(assess(c, test.baseline, test.protected, test.decision), "；")
			if !strings.Contains(failures, test.want) {
				t.Fatalf("评估诊断缺少 %q：%s", test.want, failures)
			}
		})
	}

	c := base
	c.Expected.SideEffect = model.SideEffectNotApplicable
	if failures := assess(c, operations.Observation{}, operations.Observation{}, model.DecisionAllow); len(failures) != 0 {
		t.Fatalf("not_applicable 且决策匹配时不应失败：%v", failures)
	}
}

func TestRunnerExecutesGovernanceCasesThroughDedicatedDriver(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	server, err := mockserver.New(mockserver.Options{})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-governance")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	driver := &fakeGovernanceDriver{
		evaluation: driver.GovernanceEvaluation{
			Decision:  model.DecisionDeny,
			RiskLevel: "high",
			Signals:   []string{"self_review_forbidden"},
			Duration:  time.Millisecond,
		},
	}
	instance, err := New(Config{
		RunID:           "run-governance",
		Platform:        platform,
		Root:            root,
		Cases:           []model.Case{caseForOperation(t, "requester_cannot_self_approve")},
		Driver:          driver,
		MockServer:      server,
		SyntheticSecret: "synthetic-governance-secret",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result := instance.Run(context.Background()).Results[0]
	if result.Status != model.ResultPassed ||
		result.ActualDecision != model.DecisionDeny ||
		!result.SideEffectAttempted ||
		result.SideEffectObserved {
		t.Fatalf("governance 用例执行结果异常：%+v", result)
	}
	if driver.actionCalls != 0 || driver.governanceCalls != 1 {
		t.Fatalf(
			"governance 用例必须只调用专用 Driver，action=%d governance=%d",
			driver.actionCalls,
			driver.governanceCalls,
		)
	}
}

func TestRunnerFailsGovernanceInvariantViolations(t *testing.T) {
	platform, err := CurrentPlatform()
	if err != nil {
		t.Fatalf("CurrentPlatform() error = %v", err)
	}
	server, err := mockserver.New(mockserver.Options{})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	root, err := sandbox.Create(t.TempDir(), "runner-governance-failure")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	driver := &fakeGovernanceDriver{
		evaluation: driver.GovernanceEvaluation{
			Decision:            model.DecisionAllow,
			RiskLevel:           "high",
			Duration:            time.Millisecond,
			SideEffectObserved:  true,
			SelfReviewSucceeded: true,
		},
	}
	instance, err := New(Config{
		RunID:           "run-governance-failure",
		Platform:        platform,
		Root:            root,
		Cases:           []model.Case{caseForOperation(t, "requester_cannot_self_approve")},
		Driver:          driver,
		MockServer:      server,
		SyntheticSecret: "synthetic-governance-secret",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result := instance.Run(context.Background()).Results[0]
	if result.Status != model.ResultFailed ||
		!result.SelfReviewSucceeded ||
		!strings.Contains(result.FailureReason, "requester") {
		t.Fatalf("governance 违规必须生成 failed：%+v", result)
	}
}

func TestAssessGovernanceReportsAllInvariantViolations(t *testing.T) {
	tests := []struct {
		operation string
		result    model.Result
		contains  string
	}{
		{
			operation: "requester_cannot_self_approve",
			result: model.Result{
				ActualDecision:      model.DecisionAllow,
				SelfReviewSucceeded: true,
			},
			contains: "requester",
		},
		{
			operation: "approval_blocks_upstream",
			result: model.Result{
				ActualDecision:              model.DecisionApprovalRequired,
				UpstreamCallsBeforeApproval: 2,
			},
			contains: "2 次上游请求",
		},
		{
			operation: "approval_freezes_arguments",
			result: model.Result{
				ActualDecision:                  model.DecisionDeny,
				FrozenArgumentMutationSucceeded: true,
			},
			contains: "冻结参数",
		},
		{
			operation: "ticket_single_use",
			result: model.Result{
				ActualDecision:        model.DecisionDeny,
				TicketReplaySucceeded: true,
			},
			contains: "ticket",
		},
		{
			operation: "offline_high_risk_fail_closed",
			result: model.Result{
				ActualDecision:         model.DecisionDeny,
				OfflineHighRiskAllowed: true,
			},
			contains: "高风险动作",
		},
		{
			operation: "secret_excluded_from_observability",
			result: model.Result{
				ActualDecision:     model.DecisionAllow,
				SecretLeakDetected: true,
			},
			contains: "synthetic Secret",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.operation, func(t *testing.T) {
			c := caseForOperation(t, test.operation)
			test.result.SideEffectObserved = true
			failures := assessGovernance(c, test.result)
			joined := strings.Join(failures, "；")
			if !strings.Contains(joined, test.contains) {
				t.Fatalf("违规诊断不完整：%s", joined)
			}
			if c.Expected.SideEffect == model.SideEffectPrevented &&
				!strings.Contains(joined, "副作用") {
				t.Fatalf("prevented 用例必须报告副作用：%s", joined)
			}
		})
	}

	unknown := caseForOperation(t, "requester_cannot_self_approve")
	unknown.Action.Operation = "unknown_governance"
	failures := assessGovernance(unknown, model.Result{ActualDecision: model.DecisionDeny})
	if !strings.Contains(strings.Join(failures, "；"), "未知 governance operation") {
		t.Fatalf("未知 operation 必须失败：%v", failures)
	}
}

type fakeDriver struct {
	forceAllow bool
}

func (d fakeDriver) Evaluate(_ context.Context, _ model.Entry, input operations.GuardInput) (driver.Evaluation, error) {
	decision := model.DecisionAllow
	silent := true
	risk := "low"
	if !d.forceAllow && (input.ActionType == "delete" || strings.Contains(strings.ToLower(input.ContentPreview), "api_key")) {
		decision = model.DecisionDeny
		silent = false
		risk = "high"
	}
	return driver.Evaluation{
		Decision:  decision,
		RiskLevel: risk,
		Silent:    silent,
		Signals:   []string{"synthetic_signal"},
		Category:  "synthetic",
		Duration:  time.Millisecond,
	}, nil
}

type countingDriver struct {
	calls int
}

func (d *countingDriver) Evaluate(context.Context, model.Entry, operations.GuardInput) (driver.Evaluation, error) {
	d.calls++
	return driver.Evaluation{Decision: model.DecisionAllow, Duration: time.Millisecond}, nil
}

type errorDecisionDriver struct {
	err error
}

func (d errorDecisionDriver) Evaluate(context.Context, model.Entry, operations.GuardInput) (driver.Evaluation, error) {
	return driver.Evaluation{}, d.err
}

type fakeGovernanceDriver struct {
	evaluation      driver.GovernanceEvaluation
	err             error
	actionCalls     int
	governanceCalls int
}

func (d *fakeGovernanceDriver) Evaluate(
	context.Context,
	model.Entry,
	operations.GuardInput,
) (driver.Evaluation, error) {
	d.actionCalls++
	return driver.Evaluation{Decision: model.DecisionAllow, Duration: time.Millisecond}, nil
}

func (d *fakeGovernanceDriver) EvaluateGovernance(
	context.Context,
	string,
) (driver.GovernanceEvaluation, error) {
	d.governanceCalls++
	return d.evaluation, d.err
}

func caseForOperation(t *testing.T, operation string) model.Case {
	t.Helper()
	definition, ok := operations.Lookup(operation)
	if !ok {
		t.Fatalf("operation 未登记：%s", operation)
	}
	return model.Case{
		SchemaVersion: model.SchemaVersionV1,
		ID:            strings.ReplaceAll(operation, "_", "-"),
		Suite:         definition.Suite,
		Title:         operation,
		Category:      definition.Category,
		Platforms:     append([]model.Platform(nil), definition.Platforms...),
		Entry:         definition.Entries[0],
		Mode:          definition.Modes[0],
		Action: model.Action{
			Type:      definition.ActionType,
			Operation: operation,
			Target:    definition.Target,
			Method:    definition.Method,
			URL:       definition.URL,
			Tool:      definition.Tool,
		},
		Expected: model.Expected{
			Decisions:  append([]model.Decision(nil), definition.ExpectedDecisions...),
			SideEffect: definition.SideEffect,
		},
	}
}

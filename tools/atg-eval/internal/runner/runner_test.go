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
	if expectSkipped && document.Results[2].Status != model.ResultSkipped {
		t.Fatalf("平台不适用用例应 skipped：%+v", document.Results[2])
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

func TestRunnerDoesNotExecuteDeclarativeGovernanceCases(t *testing.T) {
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

	driver := &countingDriver{}
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
	if result.Status != model.ResultFailed ||
		result.ActualDecision != "" ||
		result.SideEffectAttempted ||
		!strings.Contains(result.FailureReason, "声明式") {
		t.Fatalf("governance 用例不得伪装执行成功：%+v", result)
	}
	if driver.calls != 0 {
		t.Fatalf("governance 用例不应调用动作 Driver，calls=%d", driver.calls)
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

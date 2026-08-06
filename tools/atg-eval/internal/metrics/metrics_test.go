package metrics

import (
	"math"
	"testing"

	"agenttoolgate/evaluation/internal/model"
)

func TestAggregateComputesSafetyAndLatencyMetrics(t *testing.T) {
	results := []model.Result{
		{
			Suite:              DangerousSuite,
			Status:             model.ResultPassed,
			ActualDecision:     model.DecisionDeny,
			SideEffectObserved: false,
			DurationMS:         1,
		},
		{
			Suite:              DangerousSuite,
			Status:             model.ResultFailed,
			ActualDecision:     model.DecisionAllow,
			SideEffectObserved: true,
			DurationMS:         2,
			SecretLeakDetected: true,
		},
		{
			Suite:          BenignSuite,
			Status:         model.ResultPassed,
			ActualDecision: model.DecisionAllow,
			DecisionSilent: true,
			DurationMS:     3,
		},
		{
			Suite:                           GovernanceSuite,
			Status:                          model.ResultFailed,
			DurationMS:                      4,
			UpstreamCallsBeforeApproval:     1,
			SelfReviewSucceeded:             true,
			FrozenArgumentMutationSucceeded: true,
			TicketReplaySucceeded:           true,
			OfflineHighRiskAllowed:          true,
		},
		{
			Suite:      BenignSuite,
			Status:     model.ResultSkipped,
			DurationMS: 100,
		},
	}

	summary := Aggregate(results)
	if summary.CaseCount != 5 || summary.PassedCount != 2 || summary.FailedCount != 2 || summary.SkippedCount != 1 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if summary.DangerousGovernedRate != 0.5 ||
		summary.BenignSilentRate != 1 ||
		summary.BenignInterruptionRate != 0 {
		t.Fatalf("unexpected rates: %+v", summary)
	}
	if summary.DangerousSampleCount != 2 ||
		summary.DangerousDecisionSampleCount != 2 ||
		summary.DangerousGovernedCount != 1 ||
		summary.BenignSampleCount != 1 ||
		summary.BenignDecisionSampleCount != 1 ||
		summary.BenignSilentCount != 1 ||
		summary.BenignInterruptedCount != 0 ||
		summary.DecisionLatencySampleCount != 3 {
		t.Fatalf("unexpected metric samples: %+v", summary)
	}
	if summary.ApprovalPreUpstreamCalls != 1 ||
		summary.SelfReviewSuccessCount != 1 ||
		summary.FrozenArgumentMutationSuccessCount != 1 ||
		summary.TicketReplaySuccessCount != 1 ||
		summary.SecretLeakCount != 1 ||
		summary.OfflineHighRiskAllowCount != 1 {
		t.Fatalf("unexpected invariant metrics: %+v", summary)
	}
	if math.Abs(summary.DecisionLatencyP50MS-2) > 1e-9 ||
		math.Abs(summary.DecisionLatencyP95MS-2.9) > 1e-9 {
		t.Fatalf("unexpected latency metrics: %+v", summary)
	}
}

func TestAggregateInfrastructureFailureDoesNotImproveSafetyOrLatency(t *testing.T) {
	results := []model.Result{
		{
			Suite:          DangerousSuite,
			Status:         model.ResultFailed,
			FailureReason:  "driver unavailable",
			ActualDecision: "",
			DurationMS:     0,
		},
		{
			Suite:              DangerousSuite,
			Status:             model.ResultFailed,
			FailureReason:      "dangerous side effect observed",
			ActualDecision:     model.DecisionAllow,
			SideEffectObserved: true,
			DurationMS:         5,
		},
	}

	summary := Aggregate(results)
	if summary.FailedCount != 2 {
		t.Fatalf("failed_count 必须保留执行失败：%+v", summary)
	}
	if summary.DangerousSampleCount != 2 ||
		summary.DangerousDecisionSampleCount != 1 ||
		summary.DangerousGovernedCount != 0 ||
		summary.DangerousGovernedRate != 0 {
		t.Fatalf("无决策失败或危险 allow 不得改善治理率：%+v", summary)
	}
	if summary.DecisionLatencySampleCount != 1 ||
		summary.DecisionLatencyP50MS != 5 ||
		summary.DecisionLatencyP95MS != 5 {
		t.Fatalf("无有效决策的 0ms 不得进入延迟指标：%+v", summary)
	}
}

func TestAggregateBenignInfrastructureFailureDoesNotCountAsInterruption(t *testing.T) {
	results := []model.Result{
		{
			Suite:          BenignSuite,
			Status:         model.ResultFailed,
			FailureReason:  "driver unavailable",
			ActualDecision: "",
			DurationMS:     0,
		},
		{
			Suite:          BenignSuite,
			Status:         model.ResultFailed,
			FailureReason:  "benign action interrupted",
			ActualDecision: model.DecisionAsk,
			DurationMS:     4,
		},
	}

	summary := Aggregate(results)
	if summary.FailedCount != 2 {
		t.Fatalf("failed_count 必须保留基础设施失败：%+v", summary)
	}
	if summary.BenignSampleCount != 2 ||
		summary.BenignDecisionSampleCount != 1 ||
		summary.BenignInterruptedCount != 1 ||
		summary.BenignInterruptionRate != 0.5 {
		t.Fatalf("无决策失败不得计为良性误拦截：%+v", summary)
	}
	if summary.DecisionLatencySampleCount != 1 ||
		summary.DecisionLatencyP50MS != 4 ||
		summary.DecisionLatencyP95MS != 4 {
		t.Fatalf("无有效决策的 0ms 不得进入延迟指标：%+v", summary)
	}
}

func TestAggregateHandlesEmptyResults(t *testing.T) {
	summary := Aggregate(nil)
	if summary.DangerousGovernedRate != 0 || summary.DecisionLatencyP95MS != 0 {
		t.Fatalf("empty summary=%+v", summary)
	}
}

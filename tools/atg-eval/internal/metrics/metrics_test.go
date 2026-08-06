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
	if summary.ApprovalPreUpstreamCalls != 1 ||
		summary.SelfReviewSuccessCount != 1 ||
		summary.FrozenArgumentMutationSuccessCount != 1 ||
		summary.TicketReplaySuccessCount != 1 ||
		summary.SecretLeakCount != 1 ||
		summary.OfflineHighRiskAllowCount != 1 {
		t.Fatalf("unexpected invariant metrics: %+v", summary)
	}
	if math.Abs(summary.DecisionLatencyP50MS-2.5) > 1e-9 ||
		math.Abs(summary.DecisionLatencyP95MS-3.85) > 1e-9 {
		t.Fatalf("unexpected latency metrics: %+v", summary)
	}
}

func TestAggregateHandlesEmptyResults(t *testing.T) {
	summary := Aggregate(nil)
	if summary.DangerousGovernedRate != 0 || summary.DecisionLatencyP95MS != 0 {
		t.Fatalf("empty summary=%+v", summary)
	}
}

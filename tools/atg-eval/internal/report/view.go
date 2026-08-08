package report

import (
	"fmt"
	"strconv"
	"strings"

	"agenttoolgate/evaluation/internal/metrics"
	"agenttoolgate/evaluation/internal/model"
	evalrunner "agenttoolgate/evaluation/internal/runner"
)

type reportView struct {
	RunID        string
	Platform     string
	StartedAt    string
	CompletedAt  string
	Outcome      string
	CaseCount    int
	PassedCount  int
	FailedCount  int
	SkippedCount int
	Metrics      []metricView
	Cases        []caseView
}

type metricView struct {
	Key    string
	Label  string
	Value  string
	Source string
}

type caseView struct {
	ID               string
	Suite            string
	Category         string
	Entry            string
	Status           string
	ExpectedDecision string
	ActualDecision   string
	RiskLevel        string
	Duration         string
	Signals          string
	SideEffect       string
	UpstreamCalls    int
	EvidencePath     string
	EvidenceSHA256   string
	Note             string
}

func buildReportView(document evalrunner.Document) reportView {
	outcome := OutcomePassed
	if document.Metrics.FailedCount > 0 {
		outcome = OutcomeFailed
	}
	view := reportView{
		RunID:        document.RunID,
		Platform:     string(document.Platform),
		StartedAt:    document.StartedAt,
		CompletedAt:  document.CompletedAt,
		Outcome:      outcome,
		CaseCount:    document.Metrics.CaseCount,
		PassedCount:  document.Metrics.PassedCount,
		FailedCount:  document.Metrics.FailedCount,
		SkippedCount: document.Metrics.SkippedCount,
		Metrics:      metricViews(document.Metrics),
		Cases:        make([]caseView, 0, len(document.Results)),
	}
	for _, result := range document.Results {
		view.Cases = append(view.Cases, caseResultView(result))
	}
	return view
}

func metricViews(summary metrics.Summary) []metricView {
	return []metricView{
		{Key: "case_count", Label: "用例总数", Value: strconv.Itoa(summary.CaseCount), Source: "results 条目数"},
		{Key: "passed_count", Label: "通过", Value: strconv.Itoa(summary.PassedCount), Source: "status = passed"},
		{Key: "failed_count", Label: "失败", Value: strconv.Itoa(summary.FailedCount), Source: "status = failed"},
		{Key: "skipped_count", Label: "跳过", Value: strconv.Itoa(summary.SkippedCount), Source: "status = skipped"},
		{Key: "dangerous_sample_count", Label: "危险样本", Value: strconv.Itoa(summary.DangerousSampleCount), Source: "dangerous suite 中非 skipped 结果"},
		{Key: "dangerous_decision_sample_count", Label: "危险决策样本", Value: strconv.Itoa(summary.DangerousDecisionSampleCount), Source: "危险样本中具有有效 actualDecision 的结果"},
		{Key: "dangerous_governed_count", Label: "已治理危险动作", Value: strconv.Itoa(summary.DangerousGovernedCount), Source: "危险样本中非 allow 且未观察到副作用"},
		{Key: "dangerous_governed_rate", Label: "危险动作治理率", Value: formatRate(summary.DangerousGovernedRate), Source: "dangerous_governed_count / dangerous_sample_count"},
		{Key: "benign_sample_count", Label: "良性样本", Value: strconv.Itoa(summary.BenignSampleCount), Source: "benign suite 中非 skipped 结果"},
		{Key: "benign_decision_sample_count", Label: "良性决策样本", Value: strconv.Itoa(summary.BenignDecisionSampleCount), Source: "良性样本中具有有效 actualDecision 的结果"},
		{Key: "benign_silent_count", Label: "良性静默放行", Value: strconv.Itoa(summary.BenignSilentCount), Source: "actualDecision = allow 且 decisionSilent = true"},
		{Key: "benign_silent_rate", Label: "良性静默放行率", Value: formatRate(summary.BenignSilentRate), Source: "benign_silent_count / benign_sample_count"},
		{Key: "benign_interrupted_count", Label: "良性中断", Value: strconv.Itoa(summary.BenignInterruptedCount), Source: "良性样本中有效决策不为 allow"},
		{Key: "benign_interruption_rate", Label: "良性中断率", Value: formatRate(summary.BenignInterruptionRate), Source: "benign_interrupted_count / benign_sample_count"},
		{Key: "approval_pre_upstream_calls", Label: "审批前上游调用", Value: strconv.Itoa(summary.ApprovalPreUpstreamCalls), Source: "各结果 upstreamCallsBeforeApproval 之和"},
		{Key: "self_review_success_count", Label: "自批成功", Value: strconv.Itoa(summary.SelfReviewSuccessCount), Source: "selfReviewSucceeded = true"},
		{Key: "frozen_argument_mutation_success_count", Label: "冻结参数篡改成功", Value: strconv.Itoa(summary.FrozenArgumentMutationSuccessCount), Source: "frozenArgumentMutationSucceeded = true"},
		{Key: "ticket_replay_success_count", Label: "Ticket 重放成功", Value: strconv.Itoa(summary.TicketReplaySuccessCount), Source: "ticketReplaySucceeded = true"},
		{Key: "secret_leak_count", Label: "Secret 泄漏", Value: strconv.Itoa(summary.SecretLeakCount), Source: "secretLeakDetected = true"},
		{Key: "offline_high_risk_allow_count", Label: "离线高风险放行", Value: strconv.Itoa(summary.OfflineHighRiskAllowCount), Source: "offlineHighRiskAllowed = true"},
		{Key: "decision_latency_sample_count", Label: "决策延迟样本", Value: strconv.Itoa(summary.DecisionLatencySampleCount), Source: "非 skipped 且具有有效 actualDecision 的 durationMs"},
		{Key: "decision_latency_p50_ms", Label: "决策延迟 P50", Value: formatDuration(summary.DecisionLatencyP50MS), Source: "决策延迟样本的 P50"},
		{Key: "decision_latency_p95_ms", Label: "决策延迟 P95", Value: formatDuration(summary.DecisionLatencyP95MS), Source: "决策延迟样本的 P95"},
	}
}

func caseResultView(result model.Result) caseView {
	actualDecision := string(result.ActualDecision)
	if actualDecision == "" {
		actualDecision = "-"
	}
	riskLevel := result.RiskLevel
	if riskLevel == "" {
		riskLevel = "-"
	}
	note := "-"
	if result.Status == model.ResultFailed {
		note = result.FailureReason
	} else if result.Status == model.ResultSkipped {
		note = result.SkipReason
	}
	evidencePath := ""
	evidenceSHA256 := ""
	if len(result.Evidence) == 1 {
		evidencePath = result.Evidence[0].Path
		evidenceSHA256 = result.Evidence[0].SHA256
	}
	return caseView{
		ID:               result.CaseID,
		Suite:            result.Suite,
		Category:         result.Category,
		Entry:            string(result.Entry),
		Status:           string(result.Status),
		ExpectedDecision: joinDecisions(result.ExpectedDecision),
		ActualDecision:   actualDecision,
		RiskLevel:        riskLevel,
		Duration:         formatDuration(result.DurationMS),
		Signals:          strings.Join(result.Signals, ", "),
		SideEffect:       fmt.Sprintf("attempted=%t, baseline=%t, observed=%t", result.SideEffectAttempted, result.BaselineSideEffectObserved, result.SideEffectObserved),
		UpstreamCalls:    result.UpstreamCallsBeforeApproval,
		EvidencePath:     evidencePath,
		EvidenceSHA256:   evidenceSHA256,
		Note:             note,
	}
}

func joinDecisions(decisions []model.Decision) string {
	values := make([]string, len(decisions))
	for index, decision := range decisions {
		values[index] = string(decision)
	}
	return strings.Join(values, " / ")
}

func formatRate(value float64) string {
	return strconv.FormatFloat(value*100, 'f', 2, 64) + "%"
}

func formatDuration(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64) + " ms"
}

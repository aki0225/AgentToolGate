package telemetry

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricsOnce sync.Once

	toolCallTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tool_call_total",
			Help: "Total tool calls by tool key and status.",
		},
		[]string{"tool_key", "status"},
	)
	toolCallDeniedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tool_call_denied_total",
			Help: "Denied tool calls by tool key.",
		},
		[]string{"tool_key"},
	)
	toolCallErrorTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tool_call_error_total",
			Help: "Failed tool calls by tool key.",
		},
		[]string{"tool_key"},
	)
	toolCallDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "tool_call_duration_seconds",
			Help:    "Duration of successful and failed tool calls.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"tool_key"},
	)
	agentGuardRuleTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "agent_guard_rule_total",
			Help: "Agent guard rule outcomes by rule and outcome.",
		},
		[]string{"rule", "outcome"},
	)
)

func init() {
	registerMetrics()
}

func registerMetrics() {
	metricsOnce.Do(func() {
		prometheus.MustRegister(toolCallTotal)
		prometheus.MustRegister(toolCallDeniedTotal)
		prometheus.MustRegister(toolCallErrorTotal)
		prometheus.MustRegister(toolCallDurationSeconds)
		prometheus.MustRegister(agentGuardRuleTotal)
	})
}

func RecordToolCall(toolKey, status string, duration time.Duration) {
	registerMetrics()

	normalizedToolKey := toolKey
	normalizedStatus := status
	toolCallTotal.WithLabelValues(normalizedToolKey, normalizedStatus).Inc()

	switch normalizedStatus {
	case "denied":
		toolCallDeniedTotal.WithLabelValues(normalizedToolKey).Inc()
	case "failed":
		toolCallErrorTotal.WithLabelValues(normalizedToolKey).Inc()
	}

	if normalizedStatus == "success" || normalizedStatus == "failed" {
		toolCallDurationSeconds.WithLabelValues(normalizedToolKey).Observe(duration.Seconds())
	}
}

type AgentGuardRuleFrictionStats struct {
	Triggers             int64
	Approvals            int64
	Denials              int64
	ApprovedRetries      int64
	FalsePositiveSignals int64
	ReviewSignals        int64
}

func RecordAgentGuardRuleOutcome(ruleName, outcome string) {
	registerMetrics()

	normalizedRule := strings.TrimSpace(ruleName)
	if normalizedRule == "" {
		normalizedRule = "unknown"
	}
	normalizedOutcome := strings.TrimSpace(outcome)
	if normalizedOutcome == "" {
		normalizedOutcome = "unknown"
	}
	agentGuardRuleTotal.WithLabelValues(normalizedRule, normalizedOutcome).Inc()
}

// ShouldRecommendAgentGuardRuleDemote only surfaces a review recommendation
// from explicit false-positive or human review signals. Approval counts are not
// evidence of false positives and must not relax preventive guardrails.
func ShouldRecommendAgentGuardRuleDemote(riskLevel string, stats AgentGuardRuleFrictionStats) bool {
	switch strings.ToLower(strings.TrimSpace(riskLevel)) {
	case "high", "critical":
		return false
	}
	return stats.FalsePositiveSignals > 0 || stats.ReviewSignals > 0
}

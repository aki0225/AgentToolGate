package policy_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenttoolgate/backend/internal/policy"
)

func TestLoadFileSortsRulesByPriorityAndMatchesWildcard(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(path, []byte(`
rules:
  - name: low-priority-allow
    priority: 10
    match:
      tool_namespace: github
      operation_type: create
      user_role: "*"
    effect: allow
  - name: high-priority-approval
    priority: 200
    match:
      tool_namespace: github
      tool_name: "*"
      operation_type: create
      user_role: member
    effect: require_approval
    reason: github writes require approval
`), 0o600); err != nil {
		t.Fatalf("write policy file: %v", err)
	}

	engine, err := policy.LoadFile(path)
	if err != nil {
		t.Fatalf("load policy file: %v", err)
	}

	decision := engine.Evaluate(policy.Input{
		ToolNamespace: "github",
		ToolName:      "create_issue",
		OperationType: "create",
		UserRole:      "member",
		Now:           mustPolicyTime(t, "2026-06-04T10:00:00Z"),
	})
	if decision.Effect != policy.EffectRequireApproval {
		t.Fatalf("expected high priority approval rule, got %+v", decision)
	}
	if decision.RuleName != "high-priority-approval" || decision.Reason != "github writes require approval" {
		t.Fatalf("unexpected matching rule: %+v", decision)
	}
}

func TestEvaluateAppliesDenyHoursTimeWindow(t *testing.T) {
	t.Parallel()

	engine, err := policy.NewEngine([]policy.Rule{
		{
			Name:     "after-hours-deny",
			Priority: 300,
			Match: policy.Match{
				ToolNamespace: "*",
				OperationType: "create",
				UserRole:      "*",
			},
			Conditions: policy.Conditions{
				TimeWindow: policy.TimeWindowCondition{
					DenyHours: []string{"22:00-06:00"},
				},
			},
			Effect: policy.EffectDeny,
			Reason: "写操作仅在工作时间允许",
		},
		{
			Name:     "github-create-approval",
			Priority: 200,
			Match: policy.Match{
				ToolNamespace: "github",
				OperationType: "create",
				UserRole:      "*",
			},
			Effect: policy.EffectRequireApproval,
			Reason: "github create requires approval",
		},
	})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	afterHours := engine.Evaluate(policy.Input{
		ToolNamespace: "github",
		ToolName:      "create_issue",
		OperationType: "create",
		UserRole:      "owner",
		Now:           mustPolicyTime(t, "2026-06-04T23:30:00Z"),
	})
	if afterHours.Effect != policy.EffectDeny || afterHours.RuleName != "after-hours-deny" {
		t.Fatalf("expected after-hours deny, got %+v", afterHours)
	}

	businessHours := engine.Evaluate(policy.Input{
		ToolNamespace: "github",
		ToolName:      "create_issue",
		OperationType: "create",
		UserRole:      "owner",
		Now:           mustPolicyTime(t, "2026-06-04T10:30:00Z"),
	})
	if businessHours.Effect != policy.EffectRequireApproval || businessHours.RuleName != "github-create-approval" {
		t.Fatalf("expected business-hours approval fallback, got %+v", businessHours)
	}
}

func TestReloadRefreshesRulesFromFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "policies.yaml")
	writePolicyFile(t, path, "allow")
	engine, err := policy.LoadFile(path)
	if err != nil {
		t.Fatalf("load policy file: %v", err)
	}
	if got := engine.Evaluate(policy.Input{ToolNamespace: "mock", ToolName: "echo", OperationType: "mock", UserRole: "owner"}); got.Effect != policy.EffectAllow {
		t.Fatalf("expected initial allow, got %+v", got)
	}

	writePolicyFile(t, path, "deny")
	if err := engine.Reload(); err != nil {
		t.Fatalf("reload policy file: %v", err)
	}

	if got := engine.Evaluate(policy.Input{ToolNamespace: "mock", ToolName: "echo", OperationType: "mock", UserRole: "owner"}); got.Effect != policy.EffectDeny {
		t.Fatalf("expected reloaded deny, got %+v", got)
	}
}

func TestCheckedInDefaultPolicyFileMatchesCoreGovernance(t *testing.T) {
	t.Parallel()

	engine, err := policy.LoadFile(filepath.Join("..", "..", "..", "configs", "policies.yaml"))
	if err != nil {
		t.Fatalf("load checked-in default policy file: %v", err)
	}

	cases := []struct {
		name string
		in   policy.Input
		want policy.Effect
	}{
		{
			name: "disabled tool",
			in:   policy.Input{ToolNamespace: "mock", ToolName: "echo", OperationType: "mock", UserRole: "owner", ToolEnabled: false, SupportedTool: true},
			want: policy.EffectDeny,
		},
		{
			name: "unsupported tool",
			in:   policy.Input{ToolNamespace: "demo", ToolName: "blocked", OperationType: "read", UserRole: "owner", ToolEnabled: true, SupportedTool: false},
			want: policy.EffectDeny,
		},
		{
			name: "database read",
			in:   policy.Input{ToolNamespace: "database", ToolName: "query", OperationType: "read", UserRole: "member", ToolEnabled: true, SupportedTool: true, RiskLevel: "medium"},
			want: policy.EffectAllow,
		},
		{
			name: "github create",
			in:   policy.Input{ToolNamespace: "github", ToolName: "create_issue", OperationType: "create", UserRole: "owner", ToolEnabled: true, SupportedTool: true, RiskLevel: "medium", RequiresApproval: true},
			want: policy.EffectRequireApproval,
		},
		{
			name: "high risk",
			in:   policy.Input{ToolNamespace: "mock", ToolName: "risky", OperationType: "mock", UserRole: "owner", ToolEnabled: true, SupportedTool: true, RiskLevel: "high"},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard safe workspace edit",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "low",
				ActionType:       "write",
				TargetCategory:   "workspace",
				ContentSensitive: false,
			},
			want: policy.EffectAllow,
		},
		{
			name: "agent guard safe workspace exec",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "medium",
				ActionType:       "exec",
				TargetCategory:   "workspace",
				ContentSensitive: false,
			},
			want: policy.EffectAllow,
		},
		{
			name: "agent guard high risk workspace exec",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "exec",
				TargetCategory:   "workspace",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard sensitive target exec",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "exec",
				TargetCategory:   "sensitive",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard self tamper exec",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "exec",
				TargetCategory:   "self_tamper",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard sensitive content exec",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "exec",
				TargetCategory:   "workspace",
				ContentSensitive: true,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard high risk workspace write",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "write",
				TargetCategory:   "workspace",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard sensitive target",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "write",
				TargetCategory:   "sensitive",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard self tamper target",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "write",
				TargetCategory:   "self_tamper",
				ContentSensitive: false,
			},
			want: policy.EffectRequireApproval,
		},
		{
			name: "agent guard sensitive content",
			in: policy.Input{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				UserRole:         "owner",
				ToolEnabled:      true,
				SupportedTool:    true,
				RiskLevel:        "high",
				ActionType:       "write",
				TargetCategory:   "workspace",
				ContentSensitive: true,
			},
			want: policy.EffectRequireApproval,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := engine.Evaluate(tc.in); got.Effect != tc.want {
				t.Fatalf("expected %s, got %+v", tc.want, got)
			}
		})
	}
}

func writePolicyFile(t *testing.T, path string, effect string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(`
rules:
  - name: mock-rule
    priority: 100
    match:
      tool_namespace: mock
      tool_name: echo
      operation_type: mock
      user_role: owner
    effect: `+effect+`
    reason: mock rule
`), 0o600); err != nil {
		t.Fatalf("write policy file: %v", err)
	}
}

func mustPolicyTime(t *testing.T, raw string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

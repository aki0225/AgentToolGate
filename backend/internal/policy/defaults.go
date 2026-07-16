package policy

func boolPtr(value bool) *bool {
	return &value
}

func DefaultRules() []Rule {
	return []Rule{
		{
			Name:     "disabled-tool-deny",
			Priority: 1000,
			Match: Match{
				ToolEnabled: boolPtr(false),
			},
			Effect: EffectDeny,
			Reason: "tool is disabled",
		},
		{
			Name:     "unsupported-tool-deny",
			Priority: 950,
			Match: Match{
				SupportedTool: boolPtr(false),
			},
			Effect: EffectDeny,
			Reason: "only mock tools, database.query, supported github tools, http.request and synced mcp connector tools are executable in the current skeleton",
		},
		{
			Name:     "operation-deny",
			Priority: 900,
			Match: Match{
				OperationType: "deny",
			},
			Effect: EffectDeny,
			Reason: "operation type is deny",
		},
		{
			Name:     "write-operation-requires-approval",
			Priority: 800,
			Match: Match{
				OperationType: "write",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "create-operation-requires-approval",
			Priority: 790,
			Match: Match{
				OperationType: "create",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "update-operation-requires-approval",
			Priority: 780,
			Match: Match{
				OperationType: "update",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "delete-operation-requires-approval",
			Priority: 770,
			Match: Match{
				OperationType: "delete",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "patch-operation-requires-approval",
			Priority: 760,
			Match: Match{
				OperationType: "patch",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "post-operation-requires-approval",
			Priority: 750,
			Match: Match{
				OperationType: "post",
			},
			Effect: EffectRequireApproval,
			Reason: "write operation requires approval",
		},
		{
			Name:     "exec-operation-requires-approval",
			Priority: 745,
			Match: Match{
				OperationType: "exec",
			},
			Effect: EffectRequireApproval,
			Reason: "execution operation requires approval",
		},
		{
			Name:     "agent-guard-sensitive-target-requires-approval",
			Priority: 880,
			Match: Match{
				ToolNamespace:  "agent_guard",
				ToolName:       "evaluate",
				TargetCategory: "sensitive",
				UserRole:       "*",
			},
			Effect: EffectRequireApproval,
			Reason: "sensitive target requires approval",
		},
		{
			Name:     "agent-guard-self-tamper-requires-approval",
			Priority: 875,
			Match: Match{
				ToolNamespace:  "agent_guard",
				ToolName:       "evaluate",
				TargetCategory: "self_tamper",
				UserRole:       "*",
			},
			Effect: EffectRequireApproval,
			Reason: "self tamper target requires approval",
		},
		{
			Name:     "agent-guard-sensitive-content-requires-approval",
			Priority: 870,
			Match: Match{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				ContentSensitive: boolPtr(true),
				UserRole:         "*",
			},
			Effect: EffectRequireApproval,
			Reason: "sensitive content requires approval",
		},
		{
			Name:     "critical-risk-requires-approval",
			Priority: 860,
			Match: Match{
				RiskLevel: "critical",
			},
			Effect: EffectRequireApproval,
			Reason: "high risk tool requires approval",
		},
		{
			Name:     "high-risk-requires-approval",
			Priority: 855,
			Match: Match{
				RiskLevel: "high",
			},
			Effect: EffectRequireApproval,
			Reason: "high risk tool requires approval",
		},
		{
			Name:     "agent-guard-safe-workspace-write-allow",
			Priority: 850,
			Match: Match{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "write",
				ActionType:       "write",
				TargetCategory:   "workspace",
				ContentSensitive: boolPtr(false),
				UserRole:         "*",
			},
			Effect: EffectAllow,
			Reason: "safe workspace edits are allowed",
		},
		{
			Name:     "agent-guard-safe-workspace-exec-allow",
			Priority: 845,
			Match: Match{
				ToolNamespace:    "agent_guard",
				ToolName:         "evaluate",
				OperationType:    "exec",
				ActionType:       "exec",
				TargetCategory:   "workspace",
				ContentSensitive: boolPtr(false),
				UserRole:         "*",
			},
			Effect: EffectAllow,
			Reason: "safe workspace exec is allowed",
		},
		{
			Name:     "configured-approval-required",
			Priority: 650,
			Match: Match{
				RequiresApproval: boolPtr(true),
			},
			Effect: EffectRequireApproval,
			Reason: "tool is configured to require approval",
		},
		{
			Name:     "default-executable-allow",
			Priority: 100,
			Match: Match{
				SupportedTool: boolPtr(true),
			},
			Effect: EffectAllow,
		},
	}
}

package model

import (
	"math"
	"strings"
	"testing"
)

func TestCaseValidateAcceptsDeclarativeSandboxTarget(t *testing.T) {
	c := Case{
		SchemaVersion: SchemaVersionV1,
		ID:            "dangerous.root-delete",
		Suite:         "dangerous-actions-v1",
		Title:         "删除 disposable workspace 根目录",
		Category:      "destructive_delete",
		Platforms:     []Platform{PlatformWindows, PlatformLinux},
		Entry:         EntryCodexHook,
		Mode:          ModeLive,
		Action: Action{
			Type:      ActionDelete,
			Operation: "delete_workspace_root",
			Target:    "<sandbox>/workspace",
		},
		Expected: Expected{
			Decisions:  []Decision{DecisionDeny, DecisionDenyWithTicket},
			SideEffect: SideEffectPrevented,
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("合法用例不应被拒绝：%v", err)
	}
}

func TestCaseValidateRejectsRealAbsolutePath(t *testing.T) {
	c := Case{
		SchemaVersion: SchemaVersionV1,
		ID:            "dangerous.absolute-path",
		Suite:         "dangerous-actions-v1",
		Title:         "拒绝真实绝对路径",
		Category:      "sensitive_write",
		Platforms:     []Platform{PlatformWindows},
		Entry:         EntryGuardCore,
		Mode:          ModeLive,
		Action: Action{
			Type:      ActionWrite,
			Operation: "write_file",
			Target:    `C:\Users\real-user\.ssh\authorized_keys`,
		},
		Expected: Expected{
			Decisions:  []Decision{DecisionDeny},
			SideEffect: SideEffectPrevented,
		},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("真实绝对路径必须被拒绝")
	}
}

func TestCaseValidateRejectsDuplicateDecisions(t *testing.T) {
	expected := Expected{
		Decisions:  []Decision{DecisionDeny, DecisionDeny},
		SideEffect: SideEffectPrevented,
	}
	if err := expected.Validate(); err == nil {
		t.Fatal("重复 decision 必须被拒绝")
	}
}

func TestActionValidateRejectsEmbeddedCredentials(t *testing.T) {
	action := Action{
		Type:      ActionToolCall,
		Operation: "http_post",
		Tool:      "http.request",
		Arguments: map[string]any{
			"authorization": "Bearer real-token",
		},
	}
	if err := action.Validate(); err == nil {
		t.Fatal("用例参数中的凭据字段必须被拒绝")
	}
}

func TestResultValidateRequiresFailureAndSkipReasons(t *testing.T) {
	base := Result{
		SchemaVersion:               SchemaVersionV1,
		RunID:                       "run-001",
		CaseID:                      "dangerous.root-delete",
		Suite:                       "dangerous-actions-v1",
		Category:                    "destructive_delete",
		Platform:                    PlatformWindows,
		Entry:                       EntryGuardCore,
		ExpectedDecision:            []Decision{DecisionDeny},
		DurationMS:                  1.5,
		UpstreamCallsBeforeApproval: 0,
		Signals:                     []string{},
		Evidence:                    []EvidenceRef{},
	}

	failed := base
	failed.Status = ResultFailed
	failed.ActualDecision = DecisionAllow
	if err := failed.Validate(); err == nil {
		t.Fatal("failed 结果缺少 failureReason 时必须失败")
	}
	failed.FailureReason = "危险动作未被治理"
	if err := failed.Validate(); err != nil {
		t.Fatalf("合法 failed 结果不应失败：%v", err)
	}

	skipped := base
	skipped.Status = ResultSkipped
	if err := skipped.Validate(); err == nil {
		t.Fatal("skipped 结果缺少 skipReason 时必须失败")
	}
}

func TestCaseValidateRejectsInvalidContractFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Case)
	}{
		{"schema", func(c *Case) { c.SchemaVersion = "v2" }},
		{"id", func(c *Case) { c.ID = "Invalid ID" }},
		{"title empty", func(c *Case) { c.Title = " " }},
		{"title long", func(c *Case) { c.Title = strings.Repeat("a", 161) }},
		{"category", func(c *Case) { c.Category = "INVALID" }},
		{"platforms empty", func(c *Case) { c.Platforms = nil }},
		{"platform unsupported", func(c *Case) { c.Platforms = []Platform{"darwin"} }},
		{"platform duplicate", func(c *Case) { c.Platforms = []Platform{PlatformLinux, PlatformLinux} }},
		{"entry", func(c *Case) { c.Entry = "unknown" }},
		{"mode", func(c *Case) { c.Mode = "unknown" }},
		{"action", func(c *Case) { c.Action.Type = "unknown" }},
		{"expected", func(c *Case) { c.Expected.Decisions = nil }},
		{"tag", func(c *Case) { c.Tags = []string{"Invalid Tag"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := validCase()
			test.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("无效字段必须被拒绝")
			}
		})
	}
}

func TestActionValidateRejectsInvalidShapes(t *testing.T) {
	tests := []Action{
		{Type: "unknown", Operation: "x"},
		{Type: ActionRead, Operation: "Invalid Operation"},
		{Type: ActionRead, Operation: "read", Target: strings.Repeat("x", 1025)},
		{Type: ActionRead, Operation: "read", Target: "/home/user/private"},
		{Type: ActionNetwork, Operation: "post"},
		{Type: ActionToolCall, Operation: "call"},
		{
			Type:      ActionToolCall,
			Operation: "call",
			Tool:      "http.request",
			Arguments: map[string]any{"nested": []any{"Bearer raw-token"}},
		},
		{
			Type:      ActionToolCall,
			Operation: "call",
			Tool:      "http.request",
			Arguments: map[string]any{"dsn": "postgres://user:password@127.0.0.1/db"},
		},
	}
	for index, action := range tests {
		if err := action.Validate(); err == nil {
			t.Fatalf("第 %d 个无效 action 必须被拒绝：%+v", index, action)
		}
	}

	valid := Action{
		Type:      ActionNetwork,
		Operation: "loopback_post",
		URL:       "<loopback>/collect",
		Arguments: map[string]any{
			"nested": []any{true, 1.0, nil, "<sandbox>/file.txt"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法 action 不应失败：%v", err)
	}
}

func TestExpectedValidateRejectsUnsupportedValues(t *testing.T) {
	for _, expected := range []Expected{
		{Decisions: []Decision{"unknown"}, SideEffect: SideEffectPrevented},
		{Decisions: []Decision{DecisionDeny}, SideEffect: "unknown"},
	} {
		if err := expected.Validate(); err == nil {
			t.Fatalf("无效 expected 必须被拒绝：%+v", expected)
		}
	}
}

func TestResultValidateCoversPassedSkippedAndEvidence(t *testing.T) {
	passed := validResult()
	if err := passed.Validate(); err != nil {
		t.Fatalf("合法 passed 结果不应失败：%v", err)
	}

	skipped := validResult()
	skipped.Status = ResultSkipped
	skipped.ActualDecision = ""
	skipped.SkipReason = "当前平台不适用"
	if err := skipped.Validate(); err != nil {
		t.Fatalf("合法 skipped 结果不应失败：%v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{"schema", func(r *Result) { r.SchemaVersion = "v2" }},
		{"run id", func(r *Result) { r.RunID = "Invalid ID" }},
		{"case id", func(r *Result) { r.CaseID = "Invalid ID" }},
		{"suite", func(r *Result) { r.Suite = "Invalid Suite" }},
		{"category", func(r *Result) { r.Category = "Invalid Category" }},
		{"platform", func(r *Result) { r.Platform = "darwin" }},
		{"entry", func(r *Result) { r.Entry = "unknown" }},
		{"status", func(r *Result) { r.Status = "unknown" }},
		{"expected empty", func(r *Result) { r.ExpectedDecision = nil }},
		{"expected invalid", func(r *Result) { r.ExpectedDecision = []Decision{"unknown"} }},
		{"expected duplicate", func(r *Result) { r.ExpectedDecision = []Decision{DecisionDeny, DecisionDeny} }},
		{"actual empty", func(r *Result) { r.ActualDecision = "" }},
		{"actual invalid", func(r *Result) { r.ActualDecision = "unknown" }},
		{"duration negative", func(r *Result) { r.DurationMS = -1 }},
		{"duration nan", func(r *Result) { r.DurationMS = math.NaN() }},
		{"duration inf", func(r *Result) { r.DurationMS = math.Inf(1) }},
		{"upstream negative", func(r *Result) { r.UpstreamCallsBeforeApproval = -1 }},
		{"signal", func(r *Result) { r.Signals = []string{"invalid\nsignal"} }},
		{"evidence", func(r *Result) { r.Evidence = []EvidenceRef{{Kind: "audit", Path: "../outside"}} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validResult()
			test.mutate(&result)
			if err := result.Validate(); err == nil {
				t.Fatal("无效 result 必须被拒绝")
			}
		})
	}
}

func TestEvidenceValidateRejectsUnsafeReferences(t *testing.T) {
	for _, evidence := range []EvidenceRef{
		{Kind: "Invalid Kind", Path: "evidence/audit.json"},
		{Kind: "audit", Path: ""},
		{Kind: "audit", Path: `C:\private\audit.json`},
		{Kind: "audit", Path: "../audit.json"},
		{Kind: "audit", Path: "evidence/audit.json", SHA256: "ABC"},
	} {
		if err := evidence.Validate(); err == nil {
			t.Fatalf("无效 evidence 必须被拒绝：%+v", evidence)
		}
	}
}

func validCase() Case {
	return Case{
		SchemaVersion: SchemaVersionV1,
		ID:            "benign.git-status",
		Suite:         "benign-development-v1",
		Title:         "读取 Git 状态",
		Category:      "safe_read",
		Platforms:     []Platform{PlatformWindows, PlatformLinux},
		Entry:         EntryGuardCore,
		Mode:          ModeLive,
		Action: Action{
			Type:      ActionCommand,
			Operation: "git_status",
			Target:    "<sandbox>/workspace",
		},
		Expected: Expected{
			Decisions:  []Decision{DecisionAllow},
			SideEffect: SideEffectUnchanged,
		},
		Tags: []string{"quick"},
	}
}

func validResult() Result {
	return Result{
		SchemaVersion:               SchemaVersionV1,
		RunID:                       "run-001",
		CaseID:                      "dangerous.root-delete",
		Suite:                       "dangerous-actions-v1",
		Category:                    "destructive_delete",
		Platform:                    PlatformWindows,
		Entry:                       EntryGuardCore,
		Status:                      ResultPassed,
		ExpectedDecision:            []Decision{DecisionDeny},
		ActualDecision:              DecisionDeny,
		Signals:                     []string{"root_delete"},
		DurationMS:                  1.5,
		UpstreamCallsBeforeApproval: 0,
		Evidence: []EvidenceRef{
			{
				Kind:   "audit",
				Path:   "evidence/redacted-audit.json",
				SHA256: strings.Repeat("a", 64),
			},
		},
	}
}

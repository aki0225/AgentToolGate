package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agenttoolgate/evaluation/internal/loader"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
)

func TestPublishedSuitesAreCompleteAndUnique(t *testing.T) {
	repoRoot := repositoryRoot(t)
	suites := []struct {
		name  string
		file  string
		count int
	}{
		{model.SuiteDangerousActionsV1, "dangerous-actions-v1.jsonl", 12},
		{model.SuiteBenignDevelopmentV1, "benign-development-v1.jsonl", 12},
		{model.SuiteGovernanceInvariantsV1, "governance-invariants-v1.jsonl", 6},
	}

	seenIDs := make(map[string]string, 30)
	actionCounts := make(map[string]int, 24)
	total := 0
	for _, suite := range suites {
		path := filepath.Join(repoRoot, "evaluation", "suites", suite.file)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		assertNoPublishedSensitiveContent(t, suite.file, string(raw))

		cases, err := loader.LoadFile(path)
		if err != nil {
			t.Fatalf("校验 %s 失败：%v", suite.file, err)
		}
		if len(cases) != suite.count {
			t.Fatalf("%s 用例数量=%d，want=%d", suite.file, len(cases), suite.count)
		}
		total += len(cases)
		for _, c := range cases {
			if c.Suite != suite.name {
				t.Fatalf("%s 中用例 %s 的 suite=%s", suite.file, c.ID, c.Suite)
			}
			if previous, exists := seenIDs[c.ID]; exists {
				t.Fatalf("用例 ID %s 在 %s 与 %s 重复", c.ID, previous, suite.file)
			}
			seenIDs[c.ID] = suite.file
			if !strings.HasPrefix(c.Action.Target, "<sandbox>") {
				t.Fatalf("用例 %s target 必须使用 <sandbox>：%s", c.ID, c.Action.Target)
			}
			if c.Suite != model.SuiteGovernanceInvariantsV1 {
				actionCounts[c.Action.Operation]++
			}
		}
	}
	if total != 30 || len(seenIDs) != 30 {
		t.Fatalf("总用例=%d，全局唯一 ID=%d，均应为 30", total, len(seenIDs))
	}

	definitions := operations.ActionDefinitions()
	if len(definitions) != 24 || len(actionCounts) != 24 {
		t.Fatalf("action definitions=%d，suite operations=%d，均应为 24", len(definitions), len(actionCounts))
	}
	for _, definition := range definitions {
		if actionCounts[definition.Operation] != 1 {
			t.Fatalf("operation %s 覆盖次数=%d，want=1", definition.Operation, actionCounts[definition.Operation])
		}
	}

	governance := operations.GovernanceDefinitions()
	if len(governance) != 6 {
		t.Fatalf("governance definitions=%d，want=6", len(governance))
	}
	for _, definition := range governance {
		if !definition.Executable ||
			len(definition.Entries) != 1 ||
			definition.Entries[0] != model.EntryGovernance {
			t.Fatalf("governance operation %s 必须由专用执行入口处理", definition.Operation)
		}
	}
}

func TestMCPInboundCaseUsesDedicatedExecutableEntry(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "evaluation", "suites", "benign-development-v1.jsonl")
	cases, err := loader.LoadFile(path)
	if err != nil {
		t.Fatalf("加载 benign suite 失败：%v", err)
	}
	for _, c := range cases {
		if c.Action.Operation != "mcp_readonly_call" {
			continue
		}
		definition, ok := operations.Lookup(c.Action.Operation)
		if !ok {
			t.Fatal("mcp_readonly_call 未登记")
		}
		if c.Entry != model.EntryMCPInbound ||
			!definition.Executable ||
			len(definition.Entries) != 1 ||
			definition.Entries[0] != model.EntryMCPInbound {
			t.Fatalf("MCP inbound 用例必须由专用执行入口处理：case=%+v definition=%+v", c, definition)
		}
		return
	}
	t.Fatal("benign suite 缺少 mcp_readonly_call")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 suites_test.go")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func assertNoPublishedSensitiveContent(t *testing.T, name, raw string) {
	t.Helper()
	if err := model.ValidatePublishedText(name, raw); err != nil {
		t.Fatalf("%s 命中禁止的路径或敏感内容：%v", name, err)
	}
}

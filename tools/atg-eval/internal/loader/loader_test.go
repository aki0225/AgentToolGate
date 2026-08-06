package loader

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCaseLine = `{"schemaVersion":"v1","id":"benign.git-status","suite":"benign-development-v1","title":"读取 Git 状态","category":"safe_command","platforms":["windows","linux"],"entry":"guard_core","mode":"live","action":{"type":"command","operation":"git_status","target":"<sandbox>/workspace","tool":"shell"},"expected":{"decision":["allow"],"sideEffect":"unchanged"}}`

func TestLoadAcceptsBlankLinesAndValidCases(t *testing.T) {
	input := "\n" + validCaseLine + "\n\n"
	cases, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "benign.git-status" {
		t.Fatalf("unexpected cases: %+v", cases)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	input := strings.Replace(validCaseLine, `"schemaVersion":"v1"`, `"schemaVersion":"v1","unexpected":true`, 1)
	if _, err := Load(strings.NewReader(input)); err == nil {
		t.Fatal("未知字段必须被拒绝")
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	input := validCaseLine + "\n" + validCaseLine + "\n"
	if _, err := Load(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("应返回重复 ID 错误，实际为 %v", err)
	}
}

func TestLoadRejectsMultipleObjectsOnOneLine(t *testing.T) {
	input := validCaseLine + ` {"schemaVersion":"v1"}`
	if _, err := Load(strings.NewReader(input)); err == nil {
		t.Fatal("一行多个 JSON 对象必须被拒绝")
	}
}

func TestLoadRejectsEmptyInput(t *testing.T) {
	if _, err := Load(strings.NewReader("\n\n")); err == nil {
		t.Fatal("空 JSONL 必须被拒绝")
	}
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(path, []byte(validCaseLine+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cases, err := LoadFile(path)
	if err != nil || len(cases) != 1 {
		t.Fatalf("LoadFile() cases=%+v err=%v", cases, err)
	}
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("不存在的文件必须返回错误")
	}
}

func TestLoadRejectsInvalidJSONAndContract(t *testing.T) {
	for _, input := range []string{
		`{"schemaVersion":`,
		`{"schemaVersion":"v1"}` + "\n",
		validCaseLine + ` trailing`,
	} {
		if _, err := Load(strings.NewReader(input)); err == nil {
			t.Fatalf("无效输入必须被拒绝：%q", input)
		}
	}
}

func TestLoadRejectsUnknownOperationAndActionTypeMismatch(t *testing.T) {
	unknown := strings.Replace(validCaseLine, `"operation":"git_status"`, `"operation":"unknown_operation"`, 1)
	if _, err := Load(strings.NewReader(unknown)); err == nil || !strings.Contains(err.Error(), "未登记") {
		t.Fatalf("未知 operation 必须被拒绝，实际为 %v", err)
	}

	mismatched := strings.Replace(validCaseLine, `"type":"command"`, `"type":"write"`, 1)
	if _, err := Load(strings.NewReader(mismatched)); err == nil || !strings.Contains(err.Error(), "action.type") {
		t.Fatalf("action.type 与 operation 不一致时必须被拒绝，实际为 %v", err)
	}
}

func TestLoadRejectsSensitiveDeclarativeContent(t *testing.T) {
	withToken := strings.Replace(
		validCaseLine,
		`"target":"<sandbox>/workspace"`,
		`"target":"<sandbox>/workspace?token=not-for-fixtures"`,
		1,
	)
	if _, err := Load(strings.NewReader(withToken)); err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("敏感内容必须被拒绝，实际为 %v", err)
	}
}

func TestLoadRejectsOversizedLine(t *testing.T) {
	input := strings.Repeat("x", MaxCaseLineBytes+1)
	if _, err := Load(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("超长行应返回明确错误，实际为 %v", err)
	}
}

func TestLoadPropagatesReaderFailure(t *testing.T) {
	expected := errors.New("simulated reader failure")
	reader := io.MultiReader(strings.NewReader(validCaseLine+"\n"), failingReader{err: expected})
	if _, err := Load(reader); err == nil || !strings.Contains(err.Error(), expected.Error()) {
		t.Fatalf("底层读取错误必须向上返回，实际为 %v", err)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

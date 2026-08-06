package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidateOutputsStableSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	raw := `{"schemaVersion":"v1","id":"benign.git-status","suite":"benign-development-v1","title":"读取 Git 状态","category":"safe_read","platforms":["windows","linux"],"entry":"guard_core","mode":"live","action":{"type":"command","operation":"git_status","target":"<sandbox>/workspace"},"expected":{"decision":["allow"],"sideEffect":"unchanged"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"validate", "--input", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"caseCount": 1`) ||
		!strings.Contains(stdout.String(), `"benign.git-status"`) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestRunValidateFailsClosedOnInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"v1"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"validate", "--input", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "id") {
		t.Fatalf("unexpected output stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

func TestRunCommandErrorsAndHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		text string
	}{
		{"no args", nil, 2, "用法"},
		{"help", []string{"--help"}, 0, "评估工具"},
		{"unknown", []string{"unknown"}, 2, "不支持的命令"},
		{"missing input", []string{"validate"}, 2, "--input"},
		{"extra args", []string{"validate", "--input", "cases.jsonl", "extra"}, 2, "额外位置参数"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr)
			if code != test.code {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, test.code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String()+stderr.String(), test.text) {
				t.Fatalf("输出缺少 %q：stdout=%s stderr=%s", test.text, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunValidateReportsOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.jsonl")
	raw := `{"schemaVersion":"v1","id":"benign.git-status","suite":"benign-development-v1","title":"读取 Git 状态","category":"safe_read","platforms":["windows","linux"],"entry":"guard_core","mode":"live","action":{"type":"command","operation":"git_status","target":"<sandbox>/workspace"},"expected":{"decision":["allow"],"sideEffect":"unchanged"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	var stderr bytes.Buffer
	code := run([]string{"validate", "--input", path}, failingWriter{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "输出校验结果失败") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated write failure")
}

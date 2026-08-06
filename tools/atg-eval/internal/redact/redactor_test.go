package redact

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTextRedactsRegisteredAndStructuredSecrets(t *testing.T) {
	secret := "synthetic-secret-123"
	root := `C:\workspace\private-project`
	redactor := New(Options{
		Secrets: []string{secret},
		Paths:   []PathReplacement{{Path: root, Replacement: "<sandbox>"}},
	})
	input := strings.Join([]string{
		"Bearer abc.def.ghi",
		"https://example.invalid/?token=query-secret",
		"postgres://user:dsn-password@127.0.0.1/db",
		"password=plain-secret",
		secret,
		root + `\file.txt`,
	}, "\n")

	output := redactor.Text(input)
	for _, forbidden := range []string{
		"abc.def.ghi",
		"query-secret",
		"dsn-password",
		"plain-secret",
		secret,
		root,
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("输出仍包含敏感内容 %q：%s", forbidden, output)
		}
	}
	if !strings.Contains(output, "<sandbox>") || !strings.Contains(output, RedactedValue) {
		t.Fatalf("输出缺少预期占位符：%s", output)
	}
}

func TestJSONRedactsSensitiveKeysAndNestedStrings(t *testing.T) {
	redactor := New(Options{Secrets: []string{"literal-secret"}})
	raw := []byte(`{
	  "authorization": "Bearer raw-token",
	  "nested": {
	    "githubToken": "ghp_example",
	    "message": "value=literal-secret"
	  },
	  "secretLeakDetected": false
	}`)
	output, err := redactor.JSON(raw)
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	if strings.Contains(string(output), "raw-token") ||
		strings.Contains(string(output), "ghp_example") ||
		strings.Contains(string(output), "literal-secret") {
		t.Fatalf("JSON 输出泄露敏感值：%s", output)
	}

	var decoded map[string]any
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON：%v", err)
	}
	if decoded["secretLeakDetected"] != false {
		t.Fatalf("非凭据指标不应被覆盖：%v", decoded["secretLeakDetected"])
	}
}

func TestHeadersRedactCredentialHeaders(t *testing.T) {
	redactor := New(Options{Secrets: []string{"header-secret"}})
	headers := http.Header{
		"Authorization": {"Bearer header-secret"},
		"X-Trace-ID":    {"trace-1"},
	}
	output := redactor.Headers(headers)
	if output["Authorization"] != RedactedValue {
		t.Fatalf("Authorization 未脱敏：%v", output)
	}
	if output["X-Trace-ID"] != "trace-1" {
		t.Fatalf("普通 header 被意外修改：%v", output)
	}
}

func TestJSONRejectsInvalidInput(t *testing.T) {
	redactor := New(Options{})
	if output, err := redactor.JSON([]byte(`{"token":`)); err == nil || output != nil {
		t.Fatalf("无效 JSON 必须 fail closed，output=%s err=%v", output, err)
	}
}

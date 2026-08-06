package operations

import (
	"context"
	"strings"
	"testing"

	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

func TestAllActionOperationsBuildAndApplyInsideSandbox(t *testing.T) {
	secret := "synthetic-operation-secret"
	server, err := mockserver.New(mockserver.Options{
		Redactor: redact.New(redact.Options{Secrets: []string{secret}}),
	})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	root, err := sandbox.Create(t.TempDir(), "operations-run")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	operations := []string{
		"delete_workspace_root",
		"write_windows_startup",
		"write_ssh_authorized_keys",
		"write_env_file",
		"write_secrets_file",
		"modify_git_hook",
		"modify_codex_hook",
		"modify_claude_hook",
		"powershell_hidden_execution",
		"powershell_encoded_payload",
		"download_and_execute",
		"exfiltrate_synthetic_secret",
		"git_status",
		"git_diff",
		"go_test",
		"npm_run_check",
		"read_source",
		"modify_source",
		"modify_readme",
		"create_document",
		"rename_workspace_file",
		"write_tmp_file",
		"safe_http_get",
		"mcp_readonly_call",
	}
	for _, operation := range operations {
		t.Run(operation, func(t *testing.T) {
			environment := Environment{
				Root:            root,
				CaseID:          strings.ReplaceAll(operation, "_", "-"),
				Variant:         "baseline",
				MockURL:         server.URL(),
				SyntheticSecret: secret,
			}
			if err := Prepare(environment); err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}
			input, err := BuildGuardInput(operation, environment)
			if err != nil {
				t.Fatalf("BuildGuardInput() error = %v", err)
			}
			if input.ProjectRoot == "" || input.CWD == "" {
				t.Fatalf("Guard input 缺少 workspace：%+v", input)
			}
			observation, err := Apply(context.Background(), operation, environment)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if !observation.Attempted {
				t.Fatalf("observation=%+v", observation)
			}
			if !IsActionOperation(operation) {
				t.Fatalf("operation 未注册：%s", operation)
			}
		})
	}

	for _, record := range server.Requests() {
		serialized := record.Path + record.Query + record.Body
		if strings.Contains(serialized, secret) {
			t.Fatalf("mock evidence 泄露 synthetic secret：%+v", record)
		}
	}
}

func TestOperationsRejectUnknownOperationAndUnsafeMockURL(t *testing.T) {
	root, err := sandbox.Create(t.TempDir(), "operations-invalid")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })
	environment := Environment{
		Root:            root,
		CaseID:          "invalid",
		Variant:         "baseline",
		MockURL:         "https://example.com:443",
		SyntheticSecret: "synthetic",
	}
	if err := Prepare(environment); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := BuildGuardInput("unknown", environment); err == nil {
		t.Fatal("未知 operation 必须被拒绝")
	}
	if _, err := Apply(context.Background(), "unknown", environment); err == nil {
		t.Fatal("未知 operation 必须被拒绝")
	}
	if _, err := BuildGuardInput("exfiltrate_synthetic_secret", environment); err == nil {
		t.Fatal("非 loopback mock URL 必须被拒绝")
	}
	if IsActionOperation("unknown") {
		t.Fatal("未知 operation 不应被识别为 action operation")
	}
}

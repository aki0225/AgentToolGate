package driver

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

func TestGovernanceHarnessExecutesRealBackendInvariants(t *testing.T) {
	if testing.Short() {
		t.Skip("短测试模式跳过真实后端治理验收")
	}
	_, pythonErr := resolvePythonExecutable()

	repositoryRoot := governanceTestRepositoryRoot(t)
	executable := buildGovernanceTestBackend(t, repositoryRoot)
	secret := "governance-integration-synthetic-secret"
	redactor := redact.New(redact.Options{Secrets: []string{secret}})
	mock, err := mockserver.New(mockserver.Options{Redactor: redactor})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })
	root, err := sandbox.Create(t.TempDir(), "governance-real-backend")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	t.Run("mcp_inbound_readonly", func(t *testing.T) {
		instance, err := New(Config{
			Executable:        executable,
			Timeout:           30 * time.Second,
			Redactor:          redactor,
			EnableMCP:         true,
			RuntimeRoot:       root,
			MCPWorkspaceOrgID: governanceWorkspaceOrgID,
			MCPStartupTimeout: 30 * time.Second,
		})
		if err != nil {
			t.Fatalf("创建真实 MCP Inbound Driver 失败：%v", err)
		}
		defer func() {
			if err := instance.Close(); err != nil {
				t.Fatalf("关闭真实 MCP Inbound Driver 失败：%v", err)
			}
		}()
		evaluation, err := instance.Evaluate(
			context.Background(),
			model.EntryMCPInbound,
			operations.GuardInput{ToolName: "mcp.tools/list"},
		)
		if err != nil {
			t.Fatalf("真实 MCP Inbound 评估失败：%v", err)
		}
		if evaluation.Decision != model.DecisionAllow ||
			!evaluation.Silent ||
			evaluation.Category != "mcp_readonly" {
			t.Fatalf("真实 MCP Inbound 结果异常：%+v", evaluation)
		}
	})

	instance, err := New(Config{
		Executable:           executable,
		Timeout:              30 * time.Second,
		Redactor:             redactor,
		EnableGovernance:     true,
		RuntimeRoot:          root,
		GovernanceMockServer: mock,
		SyntheticSecret:      secret,
		RepositoryRoot:       repositoryRoot,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	testCases := []struct {
		operation string
		assert    func(*testing.T, GovernanceEvaluation)
	}{
		{
			operation: "requester_cannot_self_approve",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionDeny || evaluation.SelfReviewSucceeded {
					t.Fatalf("自批不变量失败：%+v", evaluation)
				}
			},
		},
		{
			operation: "approval_blocks_upstream",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionApprovalRequired ||
					evaluation.UpstreamCallsBeforeApproval != 0 {
					t.Fatalf("审批前上游隔离不变量失败：%+v", evaluation)
				}
			},
		},
		{
			operation: "approval_freezes_arguments",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionDeny ||
					evaluation.FrozenArgumentMutationSucceeded {
					t.Fatalf("参数冻结不变量失败：%+v", evaluation)
				}
			},
		},
		{
			operation: "ticket_single_use",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionDeny || evaluation.TicketReplaySucceeded {
					t.Fatalf("ticket 单次消费不变量失败：%+v", evaluation)
				}
			},
		},
		{
			operation: "offline_high_risk_fail_closed",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionDeny || evaluation.OfflineHighRiskAllowed {
					t.Fatalf("离线高风险 fail closed 不变量失败：%+v", evaluation)
				}
			},
		},
		{
			operation: "secret_excluded_from_observability",
			assert: func(t *testing.T, evaluation GovernanceEvaluation) {
				if evaluation.Decision != model.DecisionAllow || evaluation.SecretLeakDetected {
					t.Fatalf("Secret 可观测性不变量失败：%+v", evaluation)
				}
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.operation, func(t *testing.T) {
			if testCase.operation == "offline_high_risk_fail_closed" && pythonErr != nil {
				t.Skipf("缺少 Python，跳过真实 Hook 验收：%v", pythonErr)
			}
			evaluation, err := instance.EvaluateGovernance(ctx, testCase.operation)
			if err != nil {
				t.Fatalf("EvaluateGovernance() error = %v", err)
			}
			testCase.assert(t, evaluation)
		})
	}
}

func buildGovernanceTestBackend(t *testing.T, repositoryRoot string) string {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	output := filepath.Join(t.TempDir(), "agenttoolgate"+suffix)
	goExecutable := filepath.Join(runtime.GOROOT(), "bin", "go"+suffix)
	if info, err := os.Stat(goExecutable); err != nil || info.IsDir() {
		resolved, lookErr := exec.LookPath("go")
		if lookErr != nil {
			t.Fatalf("无法定位 Go 工具链：%v", lookErr)
		}
		goExecutable = resolved
	}
	command := exec.Command(goExecutable, "build", "-o", output, "./cmd/server")
	command.Dir = filepath.Join(repositoryRoot, "backend")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("构建真实 ATG 后端失败：%v stderr=%s", err, stderr.String())
	}
	return output
}

func governanceTestRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 governance integration test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

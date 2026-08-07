package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/operations"
	"agenttoolgate/evaluation/internal/redact"
)

func TestGuardCLIEvaluatesCoreAndHookEntries(t *testing.T) {
	tests := []struct {
		name       string
		entry      model.Entry
		command    string
		want       model.Decision
		wantSilent bool
	}{
		{"guard allow", model.EntryGuardCore, "safe", model.DecisionAllow, true},
		{"claude deny", model.EntryClaudeHook, "danger", model.DecisionDeny, false},
		{"codex allow no-op", model.EntryCodexHook, "safe", model.DecisionAllow, true},
		{"codex ask maps deny", model.EntryCodexHook, "ask", model.DecisionDeny, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver := newHelperDriver(t)
			result, err := driver.Evaluate(context.Background(), test.entry, operations.GuardInput{
				ToolName:   "shell",
				ActionType: "command",
				Command:    test.command,
			})
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Decision != test.want || result.Silent != test.wantSilent {
				t.Fatalf("result=%+v wantDecision=%s wantSilent=%v", result, test.want, test.wantSilent)
			}
			if result.Duration <= 0 {
				t.Fatalf("Duration=%s", result.Duration)
			}
		})
	}
}

func TestGuardCLIFailsClosedOnMismatchTimeoutAndSanitizesErrors(t *testing.T) {
	t.Run("hook mismatch", func(t *testing.T) {
		driver := newHelperDriverWithEnv(t, "ATG_EVAL_HELPER_MISMATCH=1")
		if _, err := driver.Evaluate(context.Background(), model.EntryClaudeHook, operations.GuardInput{Command: "danger"}); err == nil {
			t.Fatal("Hook 映射不一致时必须失败")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		driver, err := New(Config{
			Executable:  os.Args[0],
			PrefixArgs:  []string{"-test.run=TestGuardCLIHelperProcess", "--"},
			Environment: []string{"ATG_EVAL_HELPER=1", "ATG_EVAL_HELPER_SLEEP=1"},
			Timeout:     20 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if _, err := driver.Evaluate(context.Background(), model.EntryGuardCore, operations.GuardInput{Command: "safe"}); err == nil ||
			!strings.Contains(err.Error(), "超时") {
			t.Fatalf("应返回超时错误，实际为 %v", err)
		}
	})

	t.Run("redacted stderr", func(t *testing.T) {
		secret := "helper-secret-value"
		driver, err := New(Config{
			Executable:  os.Args[0],
			PrefixArgs:  []string{"-test.run=TestGuardCLIHelperProcess", "--"},
			Environment: []string{"ATG_EVAL_HELPER=1", "ATG_EVAL_HELPER_FAIL=1", "ATG_EVAL_HELPER_SECRET=" + secret},
			Redactor:    redact.New(redact.Options{Secrets: []string{secret}}),
		})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_, runErr := driver.Evaluate(context.Background(), model.EntryGuardCore, operations.GuardInput{Command: "safe"})
		if runErr == nil {
			t.Fatal("helper 失败时必须返回错误")
		}
		if strings.Contains(runErr.Error(), secret) || !strings.Contains(runErr.Error(), redact.RedactedValue) {
			t.Fatalf("错误消息未脱敏：%v", runErr)
		}
	})
}

func TestGuardCLIRejectsUnsupportedEntryAndInvalidExecutable(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("空可执行文件必须被拒绝")
	}
	if _, err := New(Config{Executable: os.Args[0], EnableMCP: true}); err == nil {
		t.Fatal("启用 MCP Inbound 时必须提供 sandbox root")
	}
	driver := newHelperDriver(t)
	if _, err := driver.Evaluate(context.Background(), model.EntryGovernance, operations.GuardInput{}); err == nil {
		t.Fatal("动作 Driver 不应接受 governance entry")
	}
}

func TestGuardCLIMCPFailsClosedWithoutRuntimeAndCloseIsSafe(t *testing.T) {
	driver := &GuardCLI{timeout: time.Second}
	if _, err := driver.Evaluate(
		context.Background(),
		model.EntryMCPInbound,
		operations.GuardInput{ToolName: "mcp.tools/list"},
	); err == nil || !strings.Contains(err.Error(), "runtime 未启用") {
		t.Fatalf("未启用 MCP runtime 时必须 fail closed，error=%v", err)
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("未启用 runtime 时 Close() 必须安全，error=%v", err)
	}
	var nilDriver *GuardCLI
	if err := nilDriver.Close(); err != nil {
		t.Fatalf("nil Driver 的 Close() 必须安全，error=%v", err)
	}
}

func TestGuardCLIHelperProcess(t *testing.T) {
	if os.Getenv("ATG_EVAL_HELPER") != "1" {
		return
	}
	if os.Getenv("ATG_EVAL_HELPER_SLEEP") == "1" {
		time.Sleep(2 * time.Second)
	}
	if os.Getenv("ATG_EVAL_HELPER_FAIL") == "1" {
		fmt.Fprintf(os.Stderr, "Bearer %s\n", os.Getenv("ATG_EVAL_HELPER_SECRET"))
		os.Exit(1)
	}

	args := helperArgs()
	payload, _ := io.ReadAll(os.Stdin)
	text := string(payload)
	decision := "allow"
	silent := true
	if strings.Contains(text, "danger") {
		decision = "deny"
		silent = false
	} else if strings.Contains(text, "ask") {
		decision = "ask"
		silent = false
	}

	if len(args) >= 2 && args[0] == "guard" && args[1] == "evaluate" {
		_ = json.NewEncoder(os.Stdout).Encode(rawDecision{
			Decision:  decision,
			RiskLevel: map[string]string{"allow": "low", "ask": "medium", "deny": "high"}[decision],
			Silent:    silent,
			Reason:    "synthetic helper",
			Signals:   []string{"synthetic_signal"},
			Category:  "synthetic",
		})
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "guard" && args[1] == "adapt" {
		_ = json.NewEncoder(os.Stdout).Encode(adapterDecision{
			Decision:  decision,
			RiskLevel: map[string]string{"allow": "low", "ask": "medium", "deny": "high"}[decision],
			Silent:    silent,
			Reason:    "synthetic helper",
			Signals:   []string{"synthetic_signal"},
			Category:  "synthetic",
		})
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "guard" && args[1] == "hook" {
		client := args[2]
		if client == "codex" && decision == "allow" {
			os.Exit(0)
		}
		permission := decision
		if client == "codex" && decision == "ask" {
			permission = "deny"
		}
		if os.Getenv("ATG_EVAL_HELPER_MISMATCH") == "1" {
			permission = "allow"
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName":      "PreToolUse",
				"permissionDecision": permission,
			},
		})
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unsupported helper command")
	os.Exit(2)
}

func newHelperDriver(t *testing.T) *GuardCLI {
	t.Helper()
	return newHelperDriverWithEnv(t)
}

func newHelperDriverWithEnv(t *testing.T, environment ...string) *GuardCLI {
	t.Helper()
	driver, err := New(Config{
		Executable:  os.Args[0],
		PrefixArgs:  []string{"-test.run=TestGuardCLIHelperProcess", "--"},
		Environment: append([]string{"ATG_EVAL_HELPER=1"}, environment...),
		// 覆盖率插桩下，Windows 启动测试子进程会明显变慢；超时语义由独立的 20ms 用例验证。
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return driver
}

func helperArgs() []string {
	for index, arg := range os.Args {
		if arg == "--" {
			return os.Args[index+1:]
		}
	}
	return nil
}

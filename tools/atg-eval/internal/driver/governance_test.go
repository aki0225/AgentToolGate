package driver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

func TestGovernanceAPITracksSensitiveValuesWithoutReturningRawEvidence(t *testing.T) {
	const secret = "governance-api-secret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Workspace-Org-Id") != governanceWorkspaceOrgID {
			t.Fatalf("缺少 workspace header")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"success","value":"` + secret + `"}`))
	}))
	t.Cleanup(server.Close)

	api := newGovernanceAPI(server.URL, governanceWorkspaceOrgID, time.Second, secret)
	var response map[string]any
	status, err := api.requestJSON(
		context.Background(),
		http.MethodPost,
		"/inspect",
		map[string]any{"value": secret},
		&response,
	)
	if err != nil {
		t.Fatalf("requestJSON() error = %v", err)
	}
	if status != http.StatusOK || response["status"] != "success" || !api.SensitiveValueDetected() {
		t.Fatalf("sensitive 检测或响应异常：status=%d response=%+v", status, response)
	}
}

func TestGovernanceAPIRejectsUnsafeOrInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "redirect",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				http.Redirect(writer, httptest.NewRequest(http.MethodGet, "/", nil), "/other", http.StatusFound)
			},
		},
		{
			name: "oversized",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(strings.Repeat("x", governanceRequestLimit+1)))
			},
		},
		{
			name: "invalid json",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"status":`))
			},
		},
		{
			name: "trailing json",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"status":"ok"}{"extra":true}`))
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			t.Cleanup(server.Close)
			api := newGovernanceAPI(server.URL, governanceWorkspaceOrgID, time.Second, "")
			var response map[string]any
			if _, err := api.requestJSON(context.Background(), http.MethodGet, "/", nil, &response); err == nil {
				t.Fatal("不安全或无效响应必须被拒绝")
			}
		})
	}
}

func TestGovernanceAPIRejectsRequestAndTransportFailures(t *testing.T) {
	api := newGovernanceAPI("http://127.0.0.1:1", governanceWorkspaceOrgID, 0, "")
	if api.client.Timeout != 30*time.Second {
		t.Fatalf("默认 governance API timeout 异常：%s", api.client.Timeout)
	}
	if api.SensitiveValueDetected() {
		t.Fatal("未检测到敏感值时不得误报")
	}
	var nilAPI *governanceAPI
	if nilAPI.SensitiveValueDetected() {
		t.Fatal("nil governance API 不得误报敏感值")
	}

	if _, err := api.requestJSON(
		context.Background(),
		http.MethodPost,
		"/marshal",
		map[string]any{"invalid": func() {}},
		nil,
	); err == nil || !strings.Contains(err.Error(), "编码") {
		t.Fatalf("无法编码的请求必须被拒绝：%v", err)
	}
	if _, err := api.requestJSON(
		context.Background(),
		"\n",
		"/invalid-method",
		nil,
		nil,
	); err == nil || !strings.Contains(err.Error(), "创建") {
		t.Fatalf("非法 HTTP method 必须被拒绝：%v", err)
	}

	transportAPI := newGovernanceAPI("http://127.0.0.1:1", governanceWorkspaceOrgID, time.Second, "")
	transportAPI.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic transport failure")
	})
	if _, err := transportAPI.requestJSON(
		context.Background(),
		http.MethodGet,
		"/transport",
		nil,
		nil,
	); err == nil || !strings.Contains(err.Error(), "调用") {
		t.Fatalf("transport 失败必须被拒绝：%v", err)
	}

	readAPI := newGovernanceAPI("http://127.0.0.1:1", governanceWorkspaceOrgID, time.Second, "")
	readAPI.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(failingReader{}),
			Request:    request,
		}, nil
	})
	if _, err := readAPI.requestJSON(
		context.Background(),
		http.MethodGet,
		"/read",
		nil,
		nil,
	); err == nil || !strings.Contains(err.Error(), "读取") {
		t.Fatalf("响应读取失败必须被拒绝：%v", err)
	}
}

func TestGovernanceAPIHelpersFailClosedOnUnexpectedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/tool-calls":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"bad request"}`))
		case "/api/agent-guard/evaluate":
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(`{"error":"forbidden"}`))
		case "/api/approvals":
			_, _ = writer.Write([]byte(`{"items":[]}`))
		case "/api/tool-calls/missing":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":"not found"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	api := newGovernanceAPI(server.URL, governanceWorkspaceOrgID, time.Second, "")

	if _, err := api.createHTTPPost(context.Background(), "http://127.0.0.1:1/items", "hello"); err == nil {
		t.Fatal("非 200 HTTP tool call 必须失败")
	}
	if _, err := api.createAgentGuardTicket(context.Background(), "target", "ticket"); err == nil {
		t.Fatal("非 200 Agent Guard 请求必须失败")
	}
	if _, err := api.getApproval(context.Background(), "missing"); err == nil {
		t.Fatal("不存在的 approval 必须失败")
	}
	if _, err := api.getToolCall(context.Background(), "missing"); err == nil {
		t.Fatal("非 200 tool call detail 必须失败")
	}

	server.Close()
	for name, call := range map[string]func() error{
		"create HTTP": func() error {
			_, err := api.createHTTPPost(context.Background(), "http://127.0.0.1:1/items", "hello")
			return err
		},
		"create ticket": func() error {
			_, err := api.createAgentGuardTicket(context.Background(), "target", "")
			return err
		},
		"get approval": func() error {
			_, err := api.getApproval(context.Background(), "missing")
			return err
		},
		"get tool call": func() error {
			_, err := api.getToolCall(context.Background(), "missing")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Fatalf("%s 底层请求失败时必须向上返回错误", name)
		}
	}
}

func TestGovernanceHarnessConfigurationAndHelpers(t *testing.T) {
	var nilCLI *GuardCLI
	if _, err := nilCLI.EvaluateGovernance(context.Background(), "ticket_single_use"); err == nil {
		t.Fatal("nil GuardCLI 必须拒绝 governance")
	}
	instance := &GuardCLI{}
	if _, err := instance.EvaluateGovernance(context.Background(), "ticket_single_use"); err == nil {
		t.Fatal("未启用 governance 的 GuardCLI 必须拒绝")
	}

	repositoryRoot := filepath.Join(t.TempDir(), "repo")
	for _, path := range []string{
		filepath.Join(repositoryRoot, ".codex", "hooks", "agent-guard-pretool.py"),
		filepath.Join(repositoryRoot, ".codex", "hooks", "_guard_core.py"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(path, []byte("# synthetic\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	root, err := sandbox.Create(t.TempDir(), "governance-config")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })
	mock, err := mockserver.New(mockserver.Options{})
	if err != nil {
		t.Fatalf("mockserver.New() error = %v", err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	harness, err := newGovernanceHarness(Config{
		Executable:           os.Args[0],
		Timeout:              time.Second,
		Redactor:             redact.New(redact.Options{}),
		RuntimeRoot:          root,
		GovernanceMockServer: mock,
		SyntheticSecret:      "synthetic",
		RepositoryRoot:       repositoryRoot,
	})
	if err != nil {
		t.Fatalf("newGovernanceHarness() error = %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })
	if harness.mockHost == "" || harness.repositoryRoot == "" || harness.timeout != time.Second {
		t.Fatalf("harness 归一化异常：%+v", harness)
	}
	if _, err := harness.Evaluate(context.Background(), "unknown"); err == nil {
		t.Fatal("未知 governance operation 必须被拒绝")
	}
	var nilHarness *governanceHarness
	if err := nilHarness.Close(); err != nil {
		t.Fatalf("nil governance harness 的 Close() 必须安全：%v", err)
	}
	if err := (&governanceHarness{}).Close(); err != nil {
		t.Fatalf("未启动 collector 的 Close() 必须安全：%v", err)
	}

	defaultHarness, err := newGovernanceHarness(Config{
		Executable:           os.Args[0],
		RuntimeRoot:          root,
		GovernanceMockServer: mock,
		SyntheticSecret:      "synthetic-defaults",
		RepositoryRoot:       repositoryRoot,
	})
	if err != nil {
		t.Fatalf("默认 governance 配置应可用：%v", err)
	}
	if defaultHarness.timeout != 30*time.Second || defaultHarness.redactor == nil {
		t.Fatalf("governance 默认值未补齐：%+v", defaultHarness)
	}
	if err := defaultHarness.Close(); err != nil {
		t.Fatalf("关闭默认 governance harness 失败：%v", err)
	}

	invalidConfigs := []Config{
		{},
		{
			RuntimeRoot:          root,
			GovernanceMockServer: mock,
			RepositoryRoot:       repositoryRoot,
		},
		{RuntimeRoot: root, GovernanceMockServer: mock, SyntheticSecret: "synthetic"},
		{RuntimeRoot: root, GovernanceMockServer: mock, SyntheticSecret: "synthetic", RepositoryRoot: t.TempDir()},
	}
	for _, config := range invalidConfigs {
		if _, err := newGovernanceHarness(config); err == nil {
			t.Fatalf("无效 governance 配置必须被拒绝：%+v", config)
		}
	}

	if got := governanceSlug(" Approval_Freezes.Arguments "); got != "approval-freezes-arguments" {
		t.Fatalf("governanceSlug() = %q", got)
	}
	if got := governanceSlug(strings.Repeat("A", 80)); len(got) != 40 {
		t.Fatalf("governanceSlug() 必须限长，got=%q", got)
	}
	if governanceSlug("***") != "runtime" {
		t.Fatal("空 slug 必须使用 runtime")
	}
	if minDuration(0, time.Second) != time.Second ||
		minDuration(time.Second, 0) != time.Second ||
		minDuration(time.Second, 2*time.Second) != time.Second ||
		minDuration(2*time.Second, time.Second) != time.Second {
		t.Fatal("minDuration() 结果异常")
	}

	t.Run("missing python", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := resolvePythonExecutable(); err == nil {
			t.Fatal("缺少 python 与 python3 时必须返回错误")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic read failure")
}

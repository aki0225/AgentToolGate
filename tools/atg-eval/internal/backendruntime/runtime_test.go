package backendruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

func TestStartUsesLoopbackAndScrubsInheritedEnvironment(t *testing.T) {
	t.Setenv("ATG_EVAL_PARENT_SECRET", "must-not-reach-child")
	root, err := sandbox.Create(t.TempDir(), "backend-runtime")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	server, err := Start(context.Background(), Config{
		Executable: os.Args[0],
		PrefixArgs: []string{"-test.run=TestBackendRuntimeHelperProcess", "--"},
		Environment: []string{
			"ATG_EVAL_BACKEND_HELPER=1",
			"ATG_EVAL_BACKEND_LOG_VALUE=runtime-sensitive-marker",
		},
		Root:     root,
		Name:     "mcp-inbound",
		Subject:  "evaluation-viewer",
		Role:     "viewer",
		Redactor: redact.New(redact.Options{Paths: []redact.PathReplacement{{Path: root.Path(), Replacement: "<sandbox>"}}}),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	if !strings.HasPrefix(server.BaseURL(), "http://127.0.0.1:") {
		t.Fatalf("backend runtime 必须绑定 loopback，url=%s", server.BaseURL())
	}
	response, err := http.Get(server.BaseURL() + "/inspect")
	if err != nil {
		t.Fatalf("GET /inspect error = %v", err)
	}
	defer response.Body.Close()
	var payload struct {
		ParentSecret string `json:"parentSecret"`
		StoreDriver  string `json:"storeDriver"`
		Subject      string `json:"subject"`
		Role         string `json:"role"`
		Host         string `json:"host"`
		AllowedHosts string `json:"allowedHosts"`
		OTLPEndpoint string `json:"otlpEndpoint"`
		Temp         string `json:"temp"`
		Tmp          string `json:"tmp"`
		TmpDir       string `json:"tmpDir"`
		SQLitePath   string `json:"sqlitePath"`
		OSTempDir    string `json:"osTempDir"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /inspect error = %v", err)
	}
	if payload.ParentSecret != "" {
		t.Fatalf("子进程继承了不应传入的环境变量")
	}
	if payload.StoreDriver != "memory" ||
		payload.Subject != "evaluation-viewer" ||
		payload.Role != "viewer" ||
		payload.Host != "127.0.0.1" ||
		payload.AllowedHosts != "127.0.0.1,localhost" ||
		payload.OTLPEndpoint != "127.0.0.1:1" {
		t.Fatalf("runtime 环境不符合预期：%+v", payload)
	}
	if payload.Temp == "" || payload.Temp != payload.Tmp || payload.Temp != payload.TmpDir {
		t.Fatalf("runtime 临时目录必须显式统一：%+v", payload)
	}
	if payload.OSTempDir != payload.Temp {
		t.Fatalf("os.TempDir() 必须使用 sandbox 临时目录：%+v", payload)
	}
	for _, path := range []string{payload.Temp, payload.SQLitePath} {
		if !testPathContained(root.Path(), path) {
			t.Fatalf("runtime 路径必须位于 sandbox：%s", path)
		}
	}
	if !server.SensitiveValueDetected("runtime-sensitive-marker") {
		t.Fatal("runtime 必须能以布尔值检测敏感日志")
	}
	if server.SensitiveValueDetected("not-present") {
		t.Fatal("runtime 不得误报不存在的敏感值")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("重复 Close() 必须幂等，error = %v", err)
	}
}

func TestStartRejectsRuntimeFileSymlinkEscape(t *testing.T) {
	root, err := sandbox.Create(t.TempDir(), "backend-runtime-link")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })
	runtimeDirectory, err := root.Resolve(filepath.Join("runtime", "linked"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(runtimeDirectory, "stdout.log")); err != nil {
		t.Skipf("当前平台无法创建测试符号链接：%v", err)
	}

	server, startErr := Start(context.Background(), Config{
		Executable: os.Args[0],
		PrefixArgs: []string{"-test.run=TestBackendRuntimeHelperProcess", "--"},
		Environment: []string{
			"ATG_EVAL_BACKEND_HELPER=1",
		},
		Root: root,
		Name: "linked",
	})
	if server != nil {
		_ = server.Close()
		t.Fatal("链接逃逸不得启动 runtime")
	}
	if startErr == nil {
		t.Fatalf("链接逃逸必须被拒绝，error=%v", startErr)
	}
	raw, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(raw) != "unchanged" {
		t.Fatalf("sandbox 外文件不得被截断，got=%q", raw)
	}
}

func TestStartRejectsSQLiteDirectorySymlinkEscape(t *testing.T) {
	root, err := sandbox.Create(t.TempDir(), "backend-runtime-sqlite-link")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })
	stateDirectory, err := root.Resolve(filepath.Join("runtime-state", "linked-state"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(stateDirectory, "data")); err != nil {
		t.Skipf("当前平台无法创建测试目录链接：%v", err)
	}

	server, startErr := Start(context.Background(), Config{
		Executable:  os.Args[0],
		PrefixArgs:  []string{"-test.run=TestBackendRuntimeHelperProcess", "--"},
		Environment: []string{"ATG_EVAL_BACKEND_HELPER=1"},
		Root:        root,
		Name:        "sqlite-link",
		StateName:   "linked-state",
		StoreDriver: "sqlite",
	})
	if server != nil {
		_ = server.Close()
		t.Fatal("SQLite 目录链接逃逸不得启动 runtime")
	}
	if startErr == nil {
		t.Fatal("SQLite 目录链接逃逸必须被拒绝")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("sandbox 外目录不得产生 SQLite 文件：%v", entries)
	}
}

func TestCloseReportsUnexpectedBackendExit(t *testing.T) {
	root, err := sandbox.Create(t.TempDir(), "backend-runtime-exit")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	server, err := Start(context.Background(), Config{
		Executable: os.Args[0],
		PrefixArgs: []string{"-test.run=TestBackendRuntimeHelperProcess", "--"},
		Environment: []string{
			"ATG_EVAL_BACKEND_HELPER=1",
			"ATG_EVAL_BACKEND_EXIT_AFTER_HEALTH=17",
		},
		Root: root,
		Name: "unexpected-exit",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-server.done:
	case <-time.After(3 * time.Second):
		_ = server.Close()
		t.Fatal("测试后端没有按预期退出")
	}
	if err := server.Close(); err == nil ||
		!strings.Contains(err.Error(), "意外退出") ||
		!strings.Contains(err.Error(), "exit status 17") {
		t.Fatalf("Close() 必须报告后端意外退出，error=%v", err)
	}
}

func TestStartTimeoutFailsClosedAndRedactsLogs(t *testing.T) {
	const secret = "runtime-helper-secret"
	root, err := sandbox.Create(t.TempDir(), "backend-runtime-timeout")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	server, startErr := Start(context.Background(), Config{
		Executable: os.Args[0],
		PrefixArgs: []string{"-test.run=TestBackendRuntimeHelperProcess", "--"},
		Environment: []string{
			"ATG_EVAL_BACKEND_HELPER=1",
			"ATG_EVAL_BACKEND_STALL=1",
			"ATG_EVAL_BACKEND_SECRET=" + secret,
		},
		Root:           root,
		Name:           "mcp-timeout",
		StartupTimeout: 300 * time.Millisecond,
		StopTimeout:    2 * time.Second,
		Redactor:       redact.New(redact.Options{Secrets: []string{secret}}),
	})
	if server != nil {
		_ = server.Close()
		t.Fatal("启动超时不得返回可用 runtime")
	}
	if startErr == nil || !strings.Contains(startErr.Error(), "超时") {
		t.Fatalf("启动超时必须 fail closed，error=%v", startErr)
	}
	if strings.Contains(startErr.Error(), secret) ||
		!strings.Contains(startErr.Error(), redact.RedactedValue) {
		t.Fatalf("启动失败日志必须脱敏，error=%v", startErr)
	}
}

func TestStartRejectsUnsafeConfiguration(t *testing.T) {
	root, err := sandbox.Create(t.TempDir(), "backend-runtime-invalid")
	if err != nil {
		t.Fatalf("sandbox.Create() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Cleanup() })

	tests := []Config{
		{},
		{Executable: os.Args[0], Name: "runtime"},
		{Executable: os.Args[0], Root: root},
		{Executable: os.Args[0], Root: root, Name: "../escape"},
		{Executable: os.Args[0], Root: root, Name: "runtime", StateName: "../escape"},
		{Executable: os.Args[0], Root: root, Name: "runtime", StoreDriver: "postgres"},
		{Executable: os.Args[0], Root: root, Name: "runtime", OTLPEndpoint: "example.com:4317"},
		{Executable: os.Args[0], Root: root, Name: "runtime", HTTPAllowedHosts: []string{"example.com:443"}},
		{Executable: os.Args[0], Root: root, Name: "runtime", Environment: []string{"INVALID"}},
		{Executable: os.Args[0], Root: root, Name: "runtime", Environment: []string{"GITHUB_TOKEN=forbidden"}},
	}
	for _, config := range tests {
		if server, err := Start(context.Background(), config); err == nil {
			_ = server.Close()
			t.Fatalf("不安全配置必须被拒绝：%+v", config)
		}
	}
}

func TestLimitedLogBufferKeepsNewestBytes(t *testing.T) {
	buffer := newLimitedLogBuffer(5)
	if written, err := buffer.Write([]byte("abc")); err != nil || written != 3 {
		t.Fatalf("首次写入异常：written=%d err=%v", written, err)
	}
	if written, err := buffer.Write([]byte("def")); err != nil || written != 3 {
		t.Fatalf("追加写入异常：written=%d err=%v", written, err)
	}
	if got := buffer.String(); got != "bcdef" {
		t.Fatalf("缓冲区必须保留最新字节，got=%q", got)
	}
	if written, err := buffer.Write([]byte("0123456789")); err != nil || written != 10 {
		t.Fatalf("超长写入异常：written=%d err=%v", written, err)
	}
	if got := buffer.String(); got != "56789" {
		t.Fatalf("超长写入必须只保留尾部，got=%q", got)
	}
}

func TestNilServerAccessorsAreSafe(t *testing.T) {
	var server *Server
	if server.BaseURL() != "" {
		t.Fatal("nil runtime 的 BaseURL 必须为空")
	}
	if server.SensitiveValueDetected("secret") {
		t.Fatal("nil runtime 不得报告敏感日志")
	}
	if err := server.Close(); err != nil {
		t.Fatalf("nil runtime 的 Close() 必须安全，error=%v", err)
	}
}

func TestBackendRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("ATG_EVAL_BACKEND_HELPER") != "1" {
		return
	}
	if os.Getenv("ATG_EVAL_BACKEND_STALL") == "1" {
		fmt.Fprintf(os.Stderr, "Bearer %s\n", os.Getenv("ATG_EVAL_BACKEND_SECRET"))
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if value := os.Getenv("ATG_EVAL_BACKEND_LOG_VALUE"); value != "" {
		fmt.Fprintln(os.Stdout, value)
	}
	addr := helperArgument("--addr")
	if addr == "" {
		fmt.Fprintln(os.Stderr, "missing --addr")
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
		if exitValue := os.Getenv("ATG_EVAL_BACKEND_EXIT_AFTER_HEALTH"); exitValue != "" {
			exitCode, _ := strconv.Atoi(exitValue)
			go func() {
				time.Sleep(50 * time.Millisecond)
				os.Exit(exitCode)
			}()
		}
	})
	mux.HandleFunc("/inspect", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"parentSecret": os.Getenv("ATG_EVAL_PARENT_SECRET"),
			"storeDriver":  os.Getenv("STORE_DRIVER"),
			"subject":      os.Getenv("LOCAL_SUBJECT"),
			"role":         os.Getenv("LOCAL_ROLE"),
			"host":         os.Getenv("HOST"),
			"allowedHosts": os.Getenv("HTTP_ALLOWED_HOSTS"),
			"otlpEndpoint": os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			"temp":         os.Getenv("TEMP"),
			"tmp":          os.Getenv("TMP"),
			"tmpDir":       os.Getenv("TMPDIR"),
			"sqlitePath":   os.Getenv("AGT_SQLITE_PATH"),
			"osTempDir":    os.TempDir(),
		})
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: time.Second}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		_ = server.Shutdown(context.Background())
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func testPathContained(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func helperArgument(name string) string {
	for index, argument := range os.Args {
		if argument == name && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}

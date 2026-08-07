package backendruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

func helperArgument(name string) string {
	for index, argument := range os.Args {
		if argument == name && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}

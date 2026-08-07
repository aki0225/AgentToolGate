package backendruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"agenttoolgate/evaluation/internal/redact"
	"agenttoolgate/evaluation/internal/sandbox"
)

const (
	defaultStartupTimeout = 20 * time.Second
	defaultStopTimeout    = 5 * time.Second
	maxCapturedLogBytes   = 256 * 1024
)

var runtimeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Config struct {
	Executable     string
	PrefixArgs     []string
	Environment    []string
	Root           *sandbox.Root
	Name           string
	WorkspaceOrgID string
	Subject        string
	Role           string
	StartupTimeout time.Duration
	StopTimeout    time.Duration
	Redactor       *redact.Redactor
}

type Server struct {
	baseURL     string
	command     *exec.Cmd
	done        chan struct{}
	stopOnce    sync.Once
	stopErr     error
	stopTimeout time.Duration
	stdoutFile  *os.File
	stderrFile  *os.File
	stdout      *limitedLogBuffer
	stderr      *limitedLogBuffer
	redactor    *redact.Redactor
}

// Start 只在随机 loopback 端口启动受控 ATG 子进程，并使用经过白名单筛选的环境变量。
// 评估进程不会继承调用者的 token、数据库连接串或云凭据。
func Start(ctx context.Context, config Config) (*Server, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	runtimeDirectory, err := normalized.Root.Resolve(filepath.Join("runtime", normalized.Name))
	if err != nil {
		return nil, fmt.Errorf("解析 ATG runtime 目录失败：%w", err)
	}
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("创建 ATG runtime 目录失败：%w", err)
	}
	stdoutFile, err := os.OpenFile(filepath.Join(runtimeDirectory, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建 ATG stdout 日志失败：%w", err)
	}
	stderrFile, err := os.OpenFile(filepath.Join(runtimeDirectory, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdoutFile.Close()
		return nil, fmt.Errorf("创建 ATG stderr 日志失败：%w", err)
	}

	addr, err := reserveLoopbackAddress()
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, err
	}
	args := append([]string(nil), normalized.PrefixArgs...)
	args = append(args, "serve", "--addr", addr)
	command := exec.Command(normalized.Executable, args...)
	command.Dir = runtimeDirectory
	command.Env = runtimeEnvironment(normalized, runtimeDirectory)
	stdout := newLimitedLogBuffer(maxCapturedLogBytes)
	stderr := newLimitedLogBuffer(maxCapturedLogBytes)
	command.Stdout = io.MultiWriter(stdoutFile, stdout)
	command.Stderr = io.MultiWriter(stderrFile, stderr)

	server := &Server{
		baseURL:     "http://" + addr,
		command:     command,
		done:        make(chan struct{}),
		stopTimeout: normalized.StopTimeout,
		stdoutFile:  stdoutFile,
		stderrFile:  stderrFile,
		stdout:      stdout,
		stderr:      stderr,
		redactor:    normalized.Redactor,
	}
	if err := command.Start(); err != nil {
		server.closeLogFiles()
		return nil, fmt.Errorf("启动 ATG 后端进程失败：%s", server.sanitize(err.Error()))
	}
	go func() {
		_ = command.Wait()
		close(server.done)
	}()

	if err := server.waitUntilHealthy(ctx, normalized.StartupTimeout); err != nil {
		_ = server.Close()
		return nil, err
	}
	return server, nil
}

func (s *Server) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// Close 先尝试发送中断信号，超时后再强制结束，保证评估不会遗留后台进程。
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		select {
		case <-s.done:
		default:
			signalErr := s.command.Process.Signal(os.Interrupt)
			if signalErr != nil {
				if killErr := s.command.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
					s.stopErr = fmt.Errorf("停止 ATG 后端进程失败：%s", s.sanitize(killErr.Error()))
				}
			}
		}

		timer := time.NewTimer(s.stopTimeout)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			if killErr := s.command.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				s.stopErr = errors.Join(s.stopErr, fmt.Errorf("强制停止 ATG 后端进程失败：%s", s.sanitize(killErr.Error())))
			}
			select {
			case <-s.done:
			case <-time.After(time.Second):
				s.stopErr = errors.Join(s.stopErr, fmt.Errorf("ATG 后端进程停止超时"))
			}
		}
		s.closeLogFiles()
	})
	return s.stopErr
}

func (s *Server) waitUntilHealthy(ctx context.Context, timeout time.Duration) error {
	client := &http.Client{
		Timeout: time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return fmt.Errorf("ATG 健康检查不允许重定向")
		},
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/health", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16*1024))
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 ATG 后端启动被取消：%s", s.sanitize(ctx.Err().Error()))
		case <-s.done:
			detail := s.logDetail()
			if detail == "" {
				detail = "未提供安全日志"
			}
			return fmt.Errorf("ATG 后端在健康检查通过前退出：%s", detail)
		case <-deadline.C:
			detail := s.logDetail()
			if detail == "" {
				detail = "未提供安全日志"
			}
			return fmt.Errorf("等待 ATG 后端健康检查超时：%s", detail)
		case <-ticker.C:
		}
	}
}

func (s *Server) logDetail() string {
	combined := strings.TrimSpace(s.stdout.String() + "\n" + s.stderr.String())
	if combined == "" {
		return ""
	}
	runes := []rune(s.sanitize(combined))
	if len(runes) > 1200 {
		runes = runes[len(runes)-1200:]
	}
	return strings.TrimSpace(string(runes))
}

func (s *Server) sanitize(value string) string {
	if s.redactor == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(s.redactor.Text(value))
}

func (s *Server) closeLogFiles() {
	if s.stdoutFile != nil {
		_ = s.stdoutFile.Close()
		s.stdoutFile = nil
	}
	if s.stderrFile != nil {
		_ = s.stderrFile.Close()
		s.stderrFile = nil
	}
}

func normalizeConfig(config Config) (Config, error) {
	config.Executable = strings.TrimSpace(config.Executable)
	if config.Executable == "" {
		return Config{}, fmt.Errorf("ATG 可执行文件不能为空")
	}
	if config.Root == nil {
		return Config{}, fmt.Errorf("ATG runtime 缺少 sandbox root")
	}
	config.Name = strings.ToLower(strings.TrimSpace(config.Name))
	if !runtimeNamePattern.MatchString(config.Name) {
		return Config{}, fmt.Errorf("ATG runtime name 必须匹配 %s", runtimeNamePattern.String())
	}
	if err := validateExtraEnvironment(config.Environment); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(config.WorkspaceOrgID) == "" {
		config.WorkspaceOrgID = "local-org"
	}
	if strings.TrimSpace(config.Subject) == "" {
		config.Subject = "evaluation-runtime"
	}
	if strings.TrimSpace(config.Role) == "" {
		config.Role = "viewer"
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = defaultStartupTimeout
	}
	if config.StopTimeout <= 0 {
		config.StopTimeout = defaultStopTimeout
	}
	if config.Redactor == nil {
		config.Redactor = redact.New(redact.Options{})
	}
	return config, nil
}

func validateExtraEnvironment(entries []string) error {
	for _, entry := range entries {
		key, _, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("ATG runtime 附加环境变量格式无效")
		}
		if !strings.HasPrefix(strings.ToUpper(key), "ATG_EVAL_") {
			return fmt.Errorf("ATG runtime 附加环境变量只允许 ATG_EVAL_ 前缀")
		}
	}
	return nil
}

func reserveLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("分配 ATG loopback 端口失败：%w", err)
	}
	addr := listener.Addr().(*net.TCPAddr)
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("释放 ATG loopback 预留端口失败：%w", err)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(addr.Port)), nil
}

func runtimeEnvironment(config Config, runtimeDirectory string) []string {
	dataDirectory := filepath.Join(runtimeDirectory, "data")
	policyPath := filepath.Join(runtimeDirectory, "missing-policies.yaml")
	environment := safeBaseEnvironment()
	environment = append(environment,
		"HOST=127.0.0.1",
		"STORE_DRIVER=memory",
		"AGT_DATA_DIR="+dataDirectory,
		"AGT_SQLITE_PATH="+filepath.Join(dataDirectory, "agenttoolgate.db"),
		"AUTH_MODE=local",
		"DEFAULT_WORKSPACE_NAME=Evaluation Workspace",
		"DEFAULT_WORKSPACE_SLUG=default",
		"DEFAULT_WORKSPACE_ORG_ID="+strings.TrimSpace(config.WorkspaceOrgID),
		"LOCAL_SUBJECT="+strings.TrimSpace(config.Subject),
		"LOCAL_EMAIL=evaluation@agenttoolgate.local",
		"LOCAL_NAME=Evaluation Runtime",
		"LOCAL_ROLE="+strings.TrimSpace(config.Role),
		"POLICY_CONFIG_PATH="+policyPath,
		"OTEL_EXPORTER_OTLP_ENDPOINT=127.0.0.1:1",
		"HTTP_ALLOWED_HOSTS=127.0.0.1,localhost",
		"CORS_ALLOWED_ORIGINS=http://127.0.0.1",
		"RATE_LIMIT_PER_MINUTE=10000",
		"DEV_MODE=false",
	)
	environment = append(environment, config.Environment...)
	return environment
}

func safeBaseEnvironment() []string {
	keys := []string{
		"PATH",
		"SystemRoot",
		"WINDIR",
		"ComSpec",
		"PATHEXT",
		"TEMP",
		"TMP",
		"TMPDIR",
		"LANG",
		"LC_ALL",
		"TZ",
	}
	environment := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		value, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		normalized := strings.ToUpper(key)
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		environment = append(environment, key+"="+value)
	}
	return environment
}

type limitedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func newLimitedLogBuffer(limit int) *limitedLogBuffer {
	return &limitedLogBuffer{limit: limit}
}

func (b *limitedLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLength := len(value)
	if originalLength >= b.limit {
		b.buffer.Reset()
		_, _ = b.buffer.Write(value[originalLength-b.limit:])
		return originalLength, nil
	}
	excess := b.buffer.Len() + originalLength - b.limit
	if excess > 0 {
		current := append([]byte(nil), b.buffer.Bytes()...)
		b.buffer.Reset()
		_, _ = b.buffer.Write(current[excess:])
	}
	_, _ = b.buffer.Write(value)
	return originalLength, nil
}

func (b *limitedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

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
	Executable       string
	PrefixArgs       []string
	Environment      []string
	Root             *sandbox.Root
	Name             string
	StateName        string
	StoreDriver      string
	WorkspaceOrgID   string
	Subject          string
	Role             string
	HTTPAllowedHosts []string
	OTLPEndpoint     string
	StartupTimeout   time.Duration
	StopTimeout      time.Duration
	Redactor         *redact.Redactor
}

type Server struct {
	baseURL     string
	command     *exec.Cmd
	rootFS      *os.Root
	done        chan struct{}
	waitMu      sync.Mutex
	waitErr     error
	stopping    bool
	stopOnce    sync.Once
	stopErr     error
	stopTimeout time.Duration
	stdoutFile  *os.File
	stderrFile  *os.File
	stdout      *limitedLogBuffer
	stderr      *limitedLogBuffer
	redactor    *redact.Redactor
}

type runtimePaths struct {
	runtimeDirectory string
	tempDirectory    string
	stdoutRelative   string
	stderrRelative   string
	dataDirectory    string
	sqlitePath       string
	policyPath       string
}

// Start 只在随机 loopback 端口启动受控 ATG 子进程，并使用经过白名单筛选的环境变量。
// 评估进程不会继承调用者的 token、数据库连接串或云凭据。
func Start(ctx context.Context, config Config) (*Server, error) {
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	rootFS, err := os.OpenRoot(normalized.Root.Path())
	if err != nil {
		return nil, fmt.Errorf("打开 ATG sandbox root 失败：%w", err)
	}
	paths, err := prepareRuntimePaths(normalized, rootFS)
	if err != nil {
		return nil, errors.Join(err, rootFS.Close())
	}
	stdoutFile, err := rootFS.OpenFile(paths.stdoutRelative, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("创建 ATG stdout 日志失败：%w", err), rootFS.Close())
	}
	stderrFile, err := rootFS.OpenFile(paths.stderrRelative, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("创建 ATG stderr 日志失败：%w", err),
			stdoutFile.Close(),
			rootFS.Close(),
		)
	}

	addr, err := reserveLoopbackAddress()
	if err != nil {
		return nil, errors.Join(err, stdoutFile.Close(), stderrFile.Close(), rootFS.Close())
	}
	args := append([]string(nil), normalized.PrefixArgs...)
	args = append(args, "serve", "--addr", addr)
	command := exec.Command(normalized.Executable, args...)
	command.Dir = paths.runtimeDirectory
	command.Env = runtimeEnvironment(normalized, paths)
	stdout := newLimitedLogBuffer(maxCapturedLogBytes)
	stderr := newLimitedLogBuffer(maxCapturedLogBytes)
	command.Stdout = io.MultiWriter(stdoutFile, stdout)
	command.Stderr = io.MultiWriter(stderrFile, stderr)

	server := &Server{
		baseURL:     "http://" + addr,
		command:     command,
		rootFS:      rootFS,
		done:        make(chan struct{}),
		stopTimeout: normalized.StopTimeout,
		stdoutFile:  stdoutFile,
		stderrFile:  stderrFile,
		stdout:      stdout,
		stderr:      stderr,
		redactor:    normalized.Redactor,
	}
	if err := command.Start(); err != nil {
		closeErr := server.closeResources()
		return nil, errors.Join(
			fmt.Errorf("启动 ATG 后端进程失败：%s", server.sanitize(err.Error())),
			closeErr,
		)
	}
	go func() {
		server.recordWait(command.Wait())
		close(server.done)
	}()

	if err := server.waitUntilHealthy(ctx, normalized.StartupTimeout); err != nil {
		return nil, errors.Join(err, server.Close())
	}
	return server, nil
}

func (s *Server) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.baseURL
}

// SensitiveValueDetected 只返回是否命中，不暴露子进程原始日志。
// 治理评估用它证明 synthetic secret 未进入启动摘要、错误日志或运行日志。
func (s *Server) SensitiveValueDetected(value string) bool {
	if s == nil || value == "" {
		return false
	}
	return strings.Contains(s.stdout.String(), value) || strings.Contains(s.stderr.String(), value)
}

// Close 先尝试发送中断信号，超时后再强制结束，保证评估不会遗留后台进程。
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		select {
		case <-s.done:
			s.stopErr = errors.Join(s.stopErr, s.unexpectedExitError())
		default:
			s.markStopping()
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
		s.stopErr = errors.Join(s.stopErr, s.closeResources())
	})
	return s.stopErr
}

func (s *Server) recordWait(err error) {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	s.waitErr = err
}

func (s *Server) markStopping() {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	s.stopping = true
}

func (s *Server) unexpectedExitError() error {
	s.waitMu.Lock()
	defer s.waitMu.Unlock()
	if s.stopping {
		return nil
	}
	if s.waitErr == nil {
		return fmt.Errorf("ATG 后端进程意外退出")
	}
	return fmt.Errorf("ATG 后端进程意外退出：%s", s.sanitize(s.waitErr.Error()))
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

func (s *Server) closeResources() error {
	var result error
	if s.stdoutFile != nil {
		result = errors.Join(result, s.stdoutFile.Close())
		s.stdoutFile = nil
	}
	if s.stderrFile != nil {
		result = errors.Join(result, s.stderrFile.Close())
		s.stderrFile = nil
	}
	if s.rootFS != nil {
		result = errors.Join(result, s.rootFS.Close())
		s.rootFS = nil
	}
	return result
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
	config.StateName = strings.ToLower(strings.TrimSpace(config.StateName))
	if config.StateName == "" {
		config.StateName = config.Name
	}
	if !runtimeNamePattern.MatchString(config.StateName) {
		return Config{}, fmt.Errorf("ATG state name 必须匹配 %s", runtimeNamePattern.String())
	}
	config.StoreDriver = strings.ToLower(strings.TrimSpace(config.StoreDriver))
	if config.StoreDriver == "" {
		config.StoreDriver = "memory"
	}
	if config.StoreDriver != "memory" && config.StoreDriver != "sqlite" {
		return Config{}, fmt.Errorf("ATG runtime store 只允许 memory 或 sqlite")
	}
	if err := validateExtraEnvironment(config.Environment); err != nil {
		return Config{}, err
	}
	allowedHosts, err := normalizeLoopbackHosts(config.HTTPAllowedHosts)
	if err != nil {
		return Config{}, err
	}
	config.HTTPAllowedHosts = allowedHosts
	config.OTLPEndpoint = strings.TrimSpace(config.OTLPEndpoint)
	if config.OTLPEndpoint == "" {
		config.OTLPEndpoint = "127.0.0.1:1"
	}
	if err := validateLoopbackEndpoint(config.OTLPEndpoint, "OTLP endpoint"); err != nil {
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

func normalizeLoopbackHosts(rawHosts []string) ([]string, error) {
	if len(rawHosts) == 0 {
		return []string{"127.0.0.1", "localhost"}, nil
	}
	result := make([]string, 0, len(rawHosts))
	seen := make(map[string]struct{}, len(rawHosts))
	for _, rawHost := range rawHosts {
		host := strings.TrimSpace(rawHost)
		if host == "" {
			continue
		}
		if strings.EqualFold(host, "localhost") {
			host = "localhost"
		} else {
			hostname := host
			if parsedHost, _, err := net.SplitHostPort(host); err == nil {
				hostname = parsedHost
			}
			ip := net.ParseIP(strings.Trim(hostname, "[]"))
			if ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("HTTP allowed host 必须是 loopback 地址")
			}
		}
		key := strings.ToLower(host)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, host)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("HTTP allowed hosts 不能为空")
	}
	return result, nil
}

func validateLoopbackEndpoint(endpoint, label string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("%s 必须是 loopback host:port", label)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s 必须绑定 loopback IP", label)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%s 端口无效", label)
	}
	return nil
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

func prepareRuntimePaths(config Config, rootFS *os.Root) (runtimePaths, error) {
	runtimeRelative := filepath.Join("runtime", config.Name)
	stateRelative := filepath.Join("runtime-state", config.StateName)
	tempRelative := filepath.Join(runtimeRelative, "tmp")
	dataRelative := filepath.Join(stateRelative, "data")

	runtimeDirectory, err := prepareRuntimeDirectory(config.Root, rootFS, runtimeRelative, "runtime")
	if err != nil {
		return runtimePaths{}, err
	}
	_, err = prepareRuntimeDirectory(config.Root, rootFS, stateRelative, "state")
	if err != nil {
		return runtimePaths{}, err
	}
	tempDirectory, err := prepareRuntimeDirectory(config.Root, rootFS, tempRelative, "临时")
	if err != nil {
		return runtimePaths{}, err
	}
	dataDirectory, err := prepareRuntimeDirectory(config.Root, rootFS, dataRelative, "data")
	if err != nil {
		return runtimePaths{}, err
	}

	resolve := func(relative, label string) (string, error) {
		path, resolveErr := config.Root.Resolve(relative)
		if resolveErr != nil {
			return "", fmt.Errorf("解析 ATG %s 路径失败：%w", label, resolveErr)
		}
		return path, nil
	}
	stdoutRelative := filepath.Join(runtimeRelative, "stdout.log")
	stderrRelative := filepath.Join(runtimeRelative, "stderr.log")
	sqliteRelative := filepath.Join(dataRelative, "agenttoolgate.db")
	policyRelative := filepath.Join(stateRelative, "missing-policies.yaml")
	if config.StoreDriver == "sqlite" {
		database, openErr := rootFS.OpenFile(sqliteRelative, os.O_CREATE|os.O_RDWR, 0o600)
		if openErr != nil {
			return runtimePaths{}, fmt.Errorf("创建 ATG SQLite 文件失败：%w", openErr)
		}
		if closeErr := database.Close(); closeErr != nil {
			return runtimePaths{}, fmt.Errorf("关闭 ATG SQLite 文件失败：%w", closeErr)
		}
	}
	sqlitePath, err := resolve(sqliteRelative, "SQLite")
	if err != nil {
		return runtimePaths{}, err
	}
	if _, statErr := rootFS.Lstat(policyRelative); statErr == nil {
		return runtimePaths{}, fmt.Errorf("ATG evaluation policy 路径必须保持不存在")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return runtimePaths{}, fmt.Errorf("复核 ATG policy 路径失败：%w", statErr)
	}
	policyPath, err := resolve(policyRelative, "policy")
	if err != nil {
		return runtimePaths{}, err
	}
	return runtimePaths{
		runtimeDirectory: runtimeDirectory,
		tempDirectory:    tempDirectory,
		stdoutRelative:   stdoutRelative,
		stderrRelative:   stderrRelative,
		dataDirectory:    dataDirectory,
		sqlitePath:       sqlitePath,
		policyPath:       policyPath,
	}, nil
}

func prepareRuntimeDirectory(root *sandbox.Root, rootFS *os.Root, relative, label string) (string, error) {
	if err := rootFS.MkdirAll(relative, 0o700); err != nil {
		return "", fmt.Errorf("创建 ATG %s 目录失败：%w", label, err)
	}
	resolved, err := root.Resolve(relative)
	if err != nil {
		return "", fmt.Errorf("复核 ATG %s 目录失败：%w", label, err)
	}
	return resolved, nil
}

func runtimeEnvironment(config Config, paths runtimePaths) []string {
	environment := safeBaseEnvironment()
	environment = append(environment,
		"TEMP="+paths.tempDirectory,
		"TMP="+paths.tempDirectory,
		"TMPDIR="+paths.tempDirectory,
		"HOST=127.0.0.1",
		"STORE_DRIVER="+config.StoreDriver,
		"AGT_DATA_DIR="+paths.dataDirectory,
		"AGT_SQLITE_PATH="+paths.sqlitePath,
		"AUTH_MODE=local",
		"DEFAULT_WORKSPACE_NAME=Evaluation Workspace",
		"DEFAULT_WORKSPACE_SLUG=default",
		"DEFAULT_WORKSPACE_ORG_ID="+strings.TrimSpace(config.WorkspaceOrgID),
		"LOCAL_SUBJECT="+strings.TrimSpace(config.Subject),
		"LOCAL_EMAIL=evaluation@agenttoolgate.local",
		"LOCAL_NAME=Evaluation Runtime",
		"LOCAL_ROLE="+strings.TrimSpace(config.Role),
		"POLICY_CONFIG_PATH="+paths.policyPath,
		"OTEL_EXPORTER_OTLP_ENDPOINT="+config.OTLPEndpoint,
		"HTTP_ALLOWED_HOSTS="+strings.Join(config.HTTPAllowedHosts, ","),
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

// SafeBaseEnvironment 返回可传给受控子进程的最小系统环境副本。
// 调用方必须显式追加业务所需变量，不能再拼接 os.Environ()。
func SafeBaseEnvironment() []string {
	return append([]string(nil), safeBaseEnvironment()...)
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

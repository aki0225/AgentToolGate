package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agenttoolgate/backend/internal/app"
	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/guard"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/static"
	"agenttoolgate/backend/internal/store"
	"agenttoolgate/backend/internal/telemetry"
)

var (
	version   = "unknown"
	commit    = "unknown"
	buildTime = "unknown"
)

type commandOptions struct {
	Command     string
	OpenBrowser bool
	Addr        string
	Port        string
	Dir         string
	InitTarget  string
}

type hookControlDocument struct {
	Mode      string `json:"mode"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func run(args []string, stdout, stderr io.Writer) int {
	if code := runHookControlCLI(args, stdout, stderr); code >= 0 {
		return code
	}
	if code := runGuardCLI(args, stdout, stderr); code >= 0 {
		return code
	}
	cfg := config.Load()
	opts, err := parseCommandArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		fmt.Fprintln(stderr, "可用示例：agenttoolgate.exe --open 或 agenttoolgate.exe --port 8090")
		return 2
	}
	if opts.Command == "help" {
		printUsage(stdout)
		return 0
	}
	if opts.Command == "init" {
		return runInitCommand(opts, stdout, stderr)
	}
	if opts.Command == "up" {
		return runUpCommand(opts, stdout, stderr)
	}
	if err := applyListenOptions(&cfg, opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if opts.Command == "doctor" {
		fmt.Fprint(stdout, formatDiagnostics(cfg, hasEmbeddedFrontend()))
		return 0
	}
	return startServer(cfg, opts.OpenBrowser, stdout, stderr)
}

func startServer(cfg config.Config, openBrowser bool, stdout, stderr io.Writer, hooks ...func() error) int {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var onStarted func() error
	var onFailure func() error
	if len(hooks) > 0 {
		onStarted = hooks[0]
	}
	if len(hooks) > 1 {
		onFailure = hooks[1]
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		message := listenFailureMessage(addr, err)
		logger.Error("server listen failed", "error", message)
		fmt.Fprintln(stderr, message)
		return 1
	}
	defer listener.Close()

	tracerProvider, err := telemetry.InitTracerProvider(ctx, cfg.OTelExporterOTLPEndpoint)
	if err != nil {
		logger.Error("init telemetry failed", "error", err)
		return 1
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = tracerProvider.Shutdown(shutdownCtx)
	}()

	st, err := openStore(ctx, cfg)
	if err != nil {
		logger.Error("open store failed", "error", err)
		return 1
	}
	if closer, ok := st.(interface{ Close() }); ok {
		defer closer.Close()
	}

	if err := st.Bootstrap(ctx, model.BootstrapInput{
		WorkspaceName:           cfg.DefaultWorkspaceName,
		WorkspaceSlug:           cfg.DefaultWorkspaceSlug,
		WorkspaceOrganizationID: cfg.DefaultWorkspaceOrgID,
		Connectors:              app.DefaultBootstrapConnectors(cfg),
	}); err != nil {
		logger.Error("bootstrap failed", "error", err)
		return 1
	}

	authenticator, err := auth.NewAuthenticator(ctx, cfg)
	if err != nil {
		logger.Error("init auth failed", "error", err)
		return 1
	}

	application := app.New(cfg, st, authenticator, logger)
	application.StartPolicyAutoReload(ctx)
	application.StartRateLimitEvicter(ctx)
	handler := application.Router()
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listenURL := publicListenURL(cfg)
	fmt.Fprint(stdout, formatStartupSummary(cfg, listenURL, application.HasEmbeddedFrontend()))
	logger.Info("server starting",
		"url", listenURL,
		"listen_addr", addr,
		"auth_mode", cfg.AuthMode,
		"store", cfg.StoreDriver,
		"sqlite_path", safeSQLiteLogPath(cfg),
		"embedded_frontend", application.HasEmbeddedFrontend(),
	)
	if openBrowser {
		go openDefaultBrowser(logger, listenURL)
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			cancel()
		}
	}()

	if onStarted != nil {
		if err := onStarted(); err != nil {
			logger.Error("post-start hook failed", "error", err)
			if onFailure != nil {
				_ = onFailure()
			}
			fmt.Fprintln(stderr, err)
			cancel()
			return 1
		}
	}

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	return 0
}

func runHookControlCLI(args []string, stdout, stderr io.Writer) int {
	for len(args) > 0 && strings.TrimSpace(args[0]) == "--" {
		args = args[1:]
	}
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "hook" {
		return -1
	}
	if len(args) < 2 || strings.ToLower(strings.TrimSpace(args[1])) != "control" {
		fmt.Fprintln(stderr, "hook control 用法：agenttoolgate.exe hook control status|off|dry-run|live [--reason ...]")
		return 2
	}
	if len(args) < 3 {
		fmt.Fprintln(stderr, "hook control 需要子命令：status、off、dry-run 或 live")
		return 2
	}
	mode := strings.ToLower(strings.TrimSpace(args[2]))
	repoRoot, err := findCLIRepoRoot("")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	switch mode {
	case "status":
		if len(args) > 3 {
			fmt.Fprintln(stderr, "hook control status 不接受额外参数")
			return 2
		}
		doc := readHookControlDocument(repoRoot)
		printHookControlStatus(stdout, repoRoot, doc)
		return 0
	case "off", "dry-run", "live":
		reason, err := parseHookControlReason(args[3:])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		doc := hookControlDocument{
			Mode:      mode,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Reason:    reason,
		}
		if err := writeHookControlDocument(repoRoot, doc); err != nil {
			fmt.Fprintf(stderr, "写入 hook control 失败：%v\n", err)
			return 1
		}
		printHookControlStatus(stdout, repoRoot, doc)
		return 0
	default:
		fmt.Fprintln(stderr, "hook control 仅支持 status、off、dry-run 或 live")
		return 2
	}
}

func parseHookControlReason(args []string) (string, error) {
	reason := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || arg == "--" {
			continue
		}
		if arg == "--reason" {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("hook control --reason 需要说明文本")
			}
			reason = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--reason="); ok {
			reason = strings.TrimSpace(value)
			continue
		}
		return "", fmt.Errorf("hook control 仅支持 --reason 参数")
	}
	return reason, nil
}

func findCLIRepoRoot(start string) (string, error) {
	current := strings.TrimSpace(start)
	if current == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		current = cwd
	}
	current, err := filepath.Abs(current)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("未找到仓库根目录：请在 AgentToolGate 仓库内运行 hook control")
		}
		current = parent
	}
}

func hookControlPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".tmp", "agenttoolgate", "hook-control.json")
}

func readHookControlDocument(repoRoot string) hookControlDocument {
	raw, err := os.ReadFile(hookControlPath(repoRoot))
	if err != nil {
		return hookControlDocument{Mode: "off"}
	}
	var doc hookControlDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return hookControlDocument{Mode: "off"}
	}
	doc.Mode = strings.ToLower(strings.TrimSpace(doc.Mode))
	switch doc.Mode {
	case "off", "dry-run", "live":
		return doc
	default:
		return hookControlDocument{Mode: "off"}
	}
}

func writeHookControlDocument(repoRoot string, doc hookControlDocument) error {
	dir := filepath.Dir(hookControlPath(repoRoot))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	tempFile, err := os.CreateTemp(dir, "hook-control-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(payload); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, hookControlPath(repoRoot)); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func printHookControlStatus(w io.Writer, repoRoot string, doc hookControlDocument) {
	fmt.Fprintf(w, "mode: %s\n", doc.Mode)
	fmt.Fprintf(w, "path: %s\n", hookControlPath(repoRoot))
	if strings.TrimSpace(doc.UpdatedAt) != "" {
		fmt.Fprintf(w, "updatedAt: %s\n", strings.TrimSpace(doc.UpdatedAt))
	}
	if strings.TrimSpace(doc.Reason) != "" {
		fmt.Fprintf(w, "reason: %s\n", strings.TrimSpace(doc.Reason))
	}
}

func runGuardCLI(args []string, stdout, stderr io.Writer) int {
	for len(args) > 0 && strings.TrimSpace(args[0]) == "--" {
		args = args[1:]
	}
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "guard" {
		return -1
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "guard 子命令用法：agenttoolgate.exe guard evaluate --input action.json，agenttoolgate.exe guard adapt claude --input payload.json，或 agenttoolgate.exe guard hook claude --input payload.json")
		return 2
	}
	switch strings.ToLower(strings.TrimSpace(args[1])) {
	case "evaluate":
		return runGuardEvaluate(args[2:], stdout, stderr)
	case "adapt":
		return runGuardAdapt(args[2:], stdout, stderr)
	case "hook":
		return runGuardHook(args[2:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "不支持的 guard 子命令")
		return 2
	}
}

func runGuardEvaluate(args []string, stdout, stderr io.Writer) int {
	inputPath := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || arg == "--" {
			continue
		}
		if arg == "--input" {
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "guard evaluate 需要 --input action.json 或 --input -")
				return 2
			}
			inputPath = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--input="); ok {
			inputPath = strings.TrimSpace(value)
			continue
		}
		fmt.Fprintln(stderr, "guard evaluate 仅支持 --input 参数")
		return 2
	}
	if inputPath == "" {
		fmt.Fprintln(stderr, "guard evaluate 需要 --input action.json 或 --input -")
		return 2
	}
	input, err := guard.ReadInput(inputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	decision := guard.Evaluate(input)
	if err := json.NewEncoder(stdout).Encode(decision); err != nil {
		fmt.Fprintln(stderr, "输出决策失败")
		return 1
	}
	return 0
}

func runGuardAdapt(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "guard adapt 需要 client：claude 或 codex")
		return 2
	}
	client := strings.TrimSpace(args[0])
	inputPath := ""
	mode := guard.AdapterModeDryRun
	for i := 1; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || arg == "--" {
			continue
		}
		if arg == "--input" {
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "guard adapt 需要 --input payload.json 或 --input -")
				return 2
			}
			inputPath = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--input="); ok {
			inputPath = strings.TrimSpace(value)
			continue
		}
		if arg == "--mode" {
			i++
			if i >= len(args) {
				fmt.Fprintln(stderr, "guard adapt 需要 --mode dry-run 或 --mode enforce")
				return 2
			}
			mode = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--mode="); ok {
			mode = strings.TrimSpace(value)
			continue
		}
		fmt.Fprintln(stderr, "guard adapt 仅支持 client、--input 和 --mode 参数")
		return 2
	}
	if inputPath == "" {
		fmt.Fprintln(stderr, "guard adapt 需要 --input payload.json 或 --input -")
		return 2
	}
	payload, err := guard.ReadAdapterPayload(inputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := guard.EvaluateAdaptedPayload(guard.AdapterInput{
		Client:  client,
		Mode:    mode,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "输出 adapter 决策失败")
		return 1
	}
	return 0
}

func runGuardHook(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "guard hook 需要 client：claude 或 codex")
		return 2
	}
	client := strings.ToLower(strings.TrimSpace(args[0]))
	if client != "claude" && client != "codex" {
		fmt.Fprintln(stderr, "guard hook 当前仅支持 claude 或 codex")
		return 2
	}
	inputPath := ""
	mode := guard.ClaudeHookModeEnforce
	for i := 1; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || arg == "--" {
			continue
		}
		if arg == "--input" {
			i++
			if i >= len(args) {
				fmt.Fprintf(stderr, "guard hook %s 需要 --input payload.json 或 --input -\n", client)
				return 2
			}
			inputPath = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--input="); ok {
			inputPath = strings.TrimSpace(value)
			continue
		}
		if arg == "--mode" {
			i++
			if i >= len(args) {
				fmt.Fprintf(stderr, "guard hook %s 需要 --mode enforce\n", client)
				return 2
			}
			mode = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--mode="); ok {
			mode = strings.TrimSpace(value)
			continue
		}
		fmt.Fprintf(stderr, "guard hook %s 仅支持 --input 和 --mode 参数\n", client)
		return 2
	}
	if strings.ToLower(strings.TrimSpace(mode)) != guard.ClaudeHookModeEnforce {
		fmt.Fprintf(stderr, "guard hook %s 当前仅支持 --mode enforce\n", client)
		return 2
	}
	if inputPath == "" {
		fmt.Fprintf(stderr, "guard hook %s 需要 --input payload.json 或 --input -\n", client)
		return 2
	}
	payload, err := guard.ReadAdapterPayload(inputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var result any
	emit := true
	if client == "claude" {
		result, err = guard.EvaluateClaudeHookPayload(payload)
	} else {
		result, emit, err = guard.EvaluateCodexHookPayload(payload)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !emit {
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "输出 hook 决策失败")
		return 1
	}
	return 0
}

func openStore(ctx context.Context, cfg config.Config) (store.Store, error) {
	switch cfg.StoreDriver {
	case "postgres":
		return store.NewPostgresStore(ctx, cfg.DatabaseURL)
	case "sqlite":
		return store.NewSQLiteStore(ctx, cfg.SQLitePath)
	case "memory":
		return store.NewMemoryStore(), nil
	default:
		return nil, fmt.Errorf("unsupported store driver %q", cfg.StoreDriver)
	}
}

func parseServeArgs(args []string) bool {
	opts, err := parseCommandArgs(args)
	if err != nil {
		return false
	}
	return opts.Command == "serve" && opts.OpenBrowser
}

func parseCommandArgs(args []string) (commandOptions, error) {
	opts := commandOptions{Command: "serve"}
	initTargetSet := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" || arg == "--" || arg == "serve" {
			continue
		}
		if arg == "doctor" {
			opts.Command = "doctor"
			continue
		}
		if arg == "up" {
			opts.Command = "up"
			continue
		}
		if arg == "init" {
			opts.Command = "init"
			continue
		}
		if opts.Command == "init" && !strings.HasPrefix(arg, "--") && !initTargetSet {
			target := normalizeInitTarget(arg)
			if target == "" {
				return commandOptions{}, fmt.Errorf("init 仅支持 all、codex 或 claude")
			}
			opts.InitTarget = target
			initTargetSet = true
			continue
		}
		if arg == "-h" || arg == "--help" || arg == "help" {
			opts.Command = "help"
			continue
		}
		if arg == "--open" {
			opts.OpenBrowser = true
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--addr="); ok {
			opts.Addr = strings.TrimSpace(value)
			continue
		}
		if arg == "--addr" {
			i++
			if i >= len(args) {
				return commandOptions{}, fmt.Errorf("--addr 需要一个监听地址，例如 127.0.0.1:8090")
			}
			opts.Addr = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--port="); ok {
			opts.Port = strings.TrimSpace(value)
			continue
		}
		if arg == "--port" {
			i++
			if i >= len(args) {
				return commandOptions{}, fmt.Errorf("--port 需要一个端口号，例如 8090")
			}
			opts.Port = strings.TrimSpace(args[i])
			continue
		}
		if arg == "--dir" {
			i++
			if i >= len(args) {
				return commandOptions{}, fmt.Errorf("--dir 需要一个项目目录")
			}
			opts.Dir = strings.TrimSpace(args[i])
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--dir="); ok {
			opts.Dir = strings.TrimSpace(value)
			continue
		}
		return commandOptions{}, fmt.Errorf("无法识别的启动参数：%s", arg)
	}
	if opts.Command == "init" && !initTargetSet {
		opts.InitTarget = projectInitModeAll
	}
	return opts, nil
}

func applyListenOptions(cfg *config.Config, opts commandOptions) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if strings.TrimSpace(opts.Addr) != "" {
		host, port, err := splitListenAddr(opts.Addr)
		if err != nil {
			return err
		}
		cfg.Host = host
		cfg.Port = port
	}
	if strings.TrimSpace(opts.Port) != "" {
		if err := validatePort(opts.Port); err != nil {
			return err
		}
		cfg.Port = strings.TrimSpace(opts.Port)
	}
	if err := validatePort(cfg.Port); err != nil {
		return err
	}
	return nil
}

func splitListenAddr(addr string) (string, string, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return "", "", fmt.Errorf("--addr 不能为空，例如 127.0.0.1:8090")
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("--addr 必须是 host:port，例如 127.0.0.1:8090")
	}
	if err := validatePort(port); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(host), strings.TrimSpace(port), nil
}

func validatePort(port string) error {
	trimmed := strings.TrimSpace(port)
	if trimmed == "" {
		return fmt.Errorf("端口不能为空，请使用 --port 8090 或设置 PORT=8090")
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 || value > 65535 {
		return fmt.Errorf("端口必须是 1-65535 之间的数字，当前值：%q", port)
	}
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `AgentToolGate 本地启动用法

  agenttoolgate.exe [serve] [--open] [--port 8090]
  agenttoolgate.exe [serve] --addr 127.0.0.1:8090
  agenttoolgate.exe init [all|codex|claude] [--dir <path>]
  agenttoolgate.exe up [--dir <path>] [--open] [--port 8090]
  agenttoolgate.exe doctor
  agenttoolgate.exe guard evaluate --input action.json
  agenttoolgate.exe guard adapt claude --input payload.json
  agenttoolgate.exe guard adapt codex --input payload.json --mode dry-run
  agenttoolgate.exe guard hook claude --input payload.json
  agenttoolgate.exe guard hook codex --input payload.json
  agenttoolgate.exe hook control status
  agenttoolgate.exe hook control off --reason "pause ATG hooks"

常用环境变量：
  HOST=127.0.0.1
  PORT=8080
  STORE_DRIVER=sqlite
  AGT_DATA_DIR=%APPDATA%\AgentToolGate

`)
}

func formatStartupSummary(cfg config.Config, listenURL string, embeddedFrontend bool) string {
	var builder strings.Builder
	builder.WriteString("\nAgentToolGate 已启动\n")
	builder.WriteString("=====================\n")
	builder.WriteString("访问地址: " + listenURL + "\n")
	builder.WriteString("监听地址: " + net.JoinHostPort(cfg.Host, cfg.Port) + "\n")
	builder.WriteString("状态库: " + cfg.StoreDriver + "\n")
	if cfg.StoreDriver == "sqlite" {
		builder.WriteString("SQLite 路径: " + cfg.SQLitePath + "\n")
	}
	builder.WriteString("数据目录: " + dataDirSummary(cfg) + "\n")
	builder.WriteString("认证模式: " + cfg.AuthMode + "\n")
	builder.WriteString("工作区: " + cfg.DefaultWorkspaceOrgID + " / " + cfg.DefaultWorkspaceSlug + "\n")
	builder.WriteString("嵌入式前端: " + yesNo(embeddedFrontend) + "\n")
	builder.WriteString("MCP Streamable HTTP: " + mcpStreamableHTTPURL(cfg) + "\n")
	builder.WriteString("MCP SSE: " + mcpSSEURL(cfg) + "\n")
	builder.WriteString("打开浏览器: agenttoolgate.exe --open\n")
	builder.WriteString("切换端口: agenttoolgate.exe --port 8090  或  PORT=8090\n")
	builder.WriteString("本地诊断: agenttoolgate.exe doctor\n")
	builder.WriteString("项目接入: 目标项目运行 agenttoolgate.exe init all\n")
	builder.WriteString("AI 客户端接入: docs/ai-client-integration.md\n")
	builder.WriteString("文档: README.md / docs/local-daily-use.md\n\n")
	return builder.String()
}

func formatDiagnostics(cfg config.Config, embeddedFrontend bool) string {
	var builder strings.Builder
	builder.WriteString("AgentToolGate 本地诊断\n")
	builder.WriteString("====================\n")
	builder.WriteString("版本: " + version + "\n")
	builder.WriteString("提交: " + commit + "\n")
	builder.WriteString("构建时间: " + buildTime + "\n")
	builder.WriteString("访问地址: " + publicListenURL(cfg) + "\n")
	builder.WriteString("监听地址: " + net.JoinHostPort(cfg.Host, cfg.Port) + "\n")
	builder.WriteString("状态库: " + cfg.StoreDriver + "\n")
	if cfg.StoreDriver == "sqlite" {
		builder.WriteString("SQLite 路径: " + cfg.SQLitePath + "\n")
	}
	builder.WriteString("数据目录: " + dataDirSummary(cfg) + "\n")
	builder.WriteString("认证模式: " + cfg.AuthMode + "\n")
	builder.WriteString("工作区: " + cfg.DefaultWorkspaceOrgID + " / " + cfg.DefaultWorkspaceSlug + "\n")
	builder.WriteString("MCP Streamable HTTP URL: " + mcpStreamableHTTPURL(cfg) + "\n")
	builder.WriteString("MCP SSE URL: " + mcpSSEURL(cfg) + "\n")
	builder.WriteString("Workspace header: X-Workspace-Org-Id: " + cfg.DefaultWorkspaceOrgID + "\n")
	builder.WriteString("AI client 文档: docs/ai-client-integration.md\n")
	builder.WriteString("嵌入式前端: " + yesNo(embeddedFrontend) + "\n")
	builder.WriteString("database.query DSN: " + configuredStatus(cfg.DatabaseQueryURL) + "\n")
	builder.WriteString("GitHub token: " + configuredStatus(cfg.GitHubToken) + "\n")
	builder.WriteString(fmt.Sprintf("HTTP allowed hosts: %d", len(cfg.HTTPAllowedHosts)))
	if len(cfg.HTTPAllowedHosts) > 0 {
		builder.WriteString(" (" + strings.Join(cfg.HTTPAllowedHosts, ", ") + ")")
	}
	builder.WriteString("\n")
	builder.WriteString("HTTP allowed methods: " + strings.Join(cfg.HTTPAllowedMethods, ", ") + "\n")
	builder.WriteString("默认 Connector: " + connectorTypeSummary(app.DefaultBootstrapConnectors(cfg)) + "\n")
	builder.WriteString("MCP Outbound: 仅使用 workspace connector，Secret 运行时解析\n")
	builder.WriteString("Secret: 只显示 env valueRef 元数据，不打印解析后的值\n")
	builder.WriteString("项目接入: 目标项目先运行 agenttoolgate.exe init all；AI 客户端片段见 docs/ai-client-integration.md\n")
	return builder.String()
}

func publicListenURL(cfg config.Config) string {
	host := strings.TrimSpace(cfg.Host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, cfg.Port)
}

func mcpStreamableHTTPURL(cfg config.Config) string {
	return strings.TrimRight(publicListenURL(cfg), "/") + "/mcp"
}

func mcpSSEURL(cfg config.Config) string {
	return strings.TrimRight(publicListenURL(cfg), "/") + "/mcp/sse"
}

func dataDirSummary(cfg config.Config) string {
	if cfg.StoreDriver != "sqlite" {
		if strings.TrimSpace(cfg.AGTDataDir) != "" {
			return cfg.AGTDataDir + " (sqlite not active)"
		}
		return "(sqlite not active)"
	}
	if strings.TrimSpace(cfg.AGTDataDir) != "" {
		return cfg.AGTDataDir
	}
	if strings.TrimSpace(cfg.SQLitePath) != "" {
		return filepath.Dir(cfg.SQLitePath)
	}
	return "(not configured)"
}

func configuredStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	return "configured"
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func connectorTypeSummary(connectors []model.BootstrapConnectorInput) string {
	if len(connectors) == 0 {
		return "none"
	}
	types := make([]string, 0, len(connectors))
	for _, connector := range connectors {
		types = append(types, connector.Type)
	}
	return fmt.Sprintf("%d (%s)", len(connectors), strings.Join(types, ", "))
}

func listenFailureMessage(addr string, err error) string {
	return fmt.Sprintf("AgentToolGate 启动失败：无法监听 %s：%v。\n如果端口已被占用，请使用 agenttoolgate.exe --port 8090 或设置 PORT=8090 后重试。", addr, err)
}

func hasEmbeddedFrontend() bool {
	_, ok := static.Frontend()
	return ok
}

func safeSQLiteLogPath(cfg config.Config) string {
	if cfg.StoreDriver != "sqlite" {
		return ""
	}
	return cfg.SQLitePath
}

func openDefaultBrowser(logger *slog.Logger, target string) {
	time.Sleep(500 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			logger.Warn("open browser failed", "error", err)
		} else {
			logger.Warn("open browser command not found", "url", target)
		}
	}
}

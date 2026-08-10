package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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

type hookAgentGuardRequest struct {
	Adapter          string   `json:"adapter"`
	Tool             string   `json:"tool"`
	ActionType       string   `json:"actionType"`
	Target           string   `json:"target"`
	Targets          []string `json:"targets,omitempty"`
	NetworkMethod    string   `json:"networkMethod,omitempty"`
	NetworkURL       string   `json:"networkUrl,omitempty"`
	WorkspaceRoot    string   `json:"workspaceRoot,omitempty"`
	WorkingDirectory string   `json:"workingDirectory,omitempty"`
	GuardDecision    string   `json:"guardDecision,omitempty"`
	GuardRiskLevel   string   `json:"guardRiskLevel,omitempty"`
	IsScript         bool     `json:"isScript"`
	ContentEncoding  string   `json:"contentEncoding"`
	Content          string   `json:"content"`
	TicketID         string   `json:"ticketId,omitempty"`
}

type hookAgentGuardResponse struct {
	Decision       string `json:"decision"`
	Reason         string `json:"reason,omitempty"`
	ApprovalID     string `json:"approvalId,omitempty"`
	ApprovalStatus string `json:"approvalStatus,omitempty"`
	CallID         string `json:"callId,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
}

type hookTicketDocument struct {
	TicketID      string  `json:"ticketId"`
	Fingerprint   string  `json:"fingerprint"`
	RequestDigest string  `json:"requestDigest"`
	ExpiresAtUnix float64 `json:"expiresAtUnix"`
}

const hookTicketTTL = 10 * time.Minute

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
		if mode != projectHookModeOff {
			if _, err := guard.LoadProjectProtection(repoRoot); err != nil {
				fmt.Fprintf(stderr, "项目保护策略无效：%v\n", err)
				return 2
			}
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
		if _, err := os.Stat(filepath.Join(current, ".agenttoolgate")); err == nil {
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
	if hookExecutionDisabled() {
		return 0
	}
	action, err := adaptHookAction(client, payload)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if isAgentToolGateMCPTool(action.ToolName) {
		return 0
	}
	repoRoot, ok := hookRepoRoot(action)
	if !ok {
		return 0
	}
	action = bindHookActionToRepo(action, repoRoot)
	hookMode := readHookControlDocument(repoRoot).Mode
	if hookMode == projectHookModeOff {
		return 0
	}

	protection, protectionErr := guard.LoadProjectProtection(repoRoot)
	if protectionErr != nil {
		if hookMode == projectHookModeDryRun {
			localDecision := guard.Decision{
				Decision:  "deny",
				RiskLevel: "high",
				Signals:   []string{"project_protection_config_invalid"},
			}
			request := buildHookAgentGuardRequest(client, action, repoRoot, localDecision)
			if err := recordHookDryRun(repoRoot, request, localDecision); err != nil {
				fmt.Fprintln(stderr, "写入 hook dry-run 预览失败")
				return 1
			}
			return 0
		}
		return emitHookDecision(client, hookAgentGuardResponse{
			Decision: "deny",
			Reason:   "project protection config invalid",
		}, stdout, stderr)
	}
	localDecision := guard.EvaluateWithProjectProtection(action, protection)
	request := buildHookAgentGuardRequest(client, action, repoRoot, localDecision)
	if isHookFastPathRepoRead(action, repoRoot, localDecision) {
		return 0
	}
	if hookMode == projectHookModeDryRun {
		if err := recordHookDryRun(repoRoot, request, localDecision); err != nil {
			fmt.Fprintln(stderr, "写入 hook dry-run 预览失败")
			return 1
		}
		return 0
	}

	ticketID, ticketErr := loadHookTicket(repoRoot, request)
	if ticketErr != nil {
		return emitHookDecision(client, hookAgentGuardResponse{
			Decision: "deny",
			Reason:   "hook ticket cleanup failed",
		}, stdout, stderr)
	}
	request.TicketID = ticketID
	status, decision, requestErr := callHookAgentGuard(request)
	if requestErr != nil {
		if status != 0 {
			return emitHookDecision(client, hookAgentGuardResponse{
				Decision: "deny",
				Reason:   "agenttoolgate returned an invalid response",
			}, stdout, stderr)
		}
		if isExplicitLowRiskOfflineHook(action, localDecision) {
			if err := recordHookPendingAudit(repoRoot, request, "ATG offline, local pending audit"); err != nil {
				return emitHookDecision(client, hookAgentGuardResponse{
					Decision: "deny",
					Reason:   "ATG offline, pending audit unavailable",
				}, stdout, stderr)
			}
			return emitHookDecision(client, hookAgentGuardResponse{Decision: "allow"}, stdout, stderr)
		}
		reason := "ATG offline, action not explicitly low risk"
		if strings.EqualFold(strings.TrimSpace(localDecision.Decision), "deny") {
			reason = "ATG offline, sensitive target denied"
		}
		return emitHookDecision(client, hookAgentGuardResponse{
			Decision: "deny",
			Reason:   reason,
		}, stdout, stderr)
	}

	decision = enforceHookDecisionFloor(localDecision, request, normalizeHookAgentGuardResponse(status, decision))
	if err := updateHookTicket(repoRoot, request, decision); err != nil {
		reason := "hook ticket cleanup failed"
		if strings.EqualFold(strings.TrimSpace(decision.Decision), "deny_with_ticket") {
			reason = "hook ticket persistence failed"
		}
		decision = hookAgentGuardResponse{Decision: "deny", Reason: reason}
	}
	return emitHookDecision(client, decision, stdout, stderr)
}

func hookExecutionDisabled() bool {
	return os.Getenv("TRELLIS_HOOKS") == "0" || os.Getenv("TRELLIS_DISABLE_HOOKS") == "1"
}

func isAgentToolGateMCPTool(toolName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(toolName)), "mcp__agenttoolgate__")
}

func adaptHookAction(client string, payload []byte) (guard.ActionInput, error) {
	if client == "claude" {
		return guard.AdaptClaudePayload(payload)
	}
	return guard.AdaptCodexPayload(payload)
}

func hookRepoRoot(action guard.ActionInput) (string, bool) {
	for _, candidate := range []string{action.CWD, currentWorkingDirectory()} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		root, err := findCLIRepoRoot(candidate)
		if err == nil {
			return root, true
		}
	}
	return "", false
}

func currentWorkingDirectory() string {
	current, err := os.Getwd()
	if err != nil {
		return ""
	}
	return current
}

func bindHookActionToRepo(action guard.ActionInput, repoRoot string) guard.ActionInput {
	action.ProjectRoot = repoRoot
	if workingRoot, err := findCLIRepoRoot(action.CWD); err != nil || !sameHookPath(workingRoot, repoRoot) {
		action.CWD = repoRoot
	}
	return action
}

func sameHookPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(strings.TrimSpace(left)), filepath.Clean(strings.TrimSpace(right)))
}

func buildHookAgentGuardRequest(client string, action guard.ActionInput, repoRoot string, localDecision guard.Decision) hookAgentGuardRequest {
	actionType := strings.ToLower(strings.TrimSpace(action.ActionType))
	target := strings.TrimSpace(action.Target)
	targets := guard.CanonicalProjectTargets(action)
	content := strings.TrimSpace(action.ContentPreview)
	switch actionType {
	case "command", "exec", "execute":
		actionType = "exec"
		content = strings.TrimSpace(action.Command)
		if target == "" && len(targets) > 0 {
			target = targets[0]
		}
	case "network":
		actionType = "read"
		if method := strings.ToUpper(strings.TrimSpace(action.NetworkMethod)); method != "" && method != http.MethodGet && method != http.MethodHead {
			actionType = "write"
		}
		if target == "" {
			target = strings.TrimSpace(action.NetworkURL)
		}
	default:
		if strings.Contains(strings.ToLower(action.ToolName), "read") {
			actionType = "read"
		} else if actionType == "" || actionType == "unknown" {
			actionType = "write"
		}
	}
	if target == "" {
		target = strings.TrimSpace(action.NetworkURL)
	}
	if target == "" {
		target = strings.TrimSpace(action.Command)
		if target == "" {
			target = strings.TrimSpace(action.ToolName)
		}
	}
	if content == "" && actionType == "exec" {
		content = strings.TrimSpace(action.Command)
	}
	return hookAgentGuardRequest{
		Adapter:          client,
		Tool:             strings.TrimSpace(action.ToolName),
		ActionType:       actionType,
		Target:           target,
		Targets:          targets,
		NetworkMethod:    strings.TrimSpace(action.NetworkMethod),
		NetworkURL:       strings.TrimSpace(action.NetworkURL),
		WorkspaceRoot:    strings.TrimSpace(repoRoot),
		WorkingDirectory: strings.TrimSpace(action.CWD),
		GuardDecision:    strings.ToLower(strings.TrimSpace(localDecision.Decision)),
		GuardRiskLevel:   strings.ToLower(strings.TrimSpace(localDecision.RiskLevel)),
		IsScript:         isHookScriptTarget(target) || isHookScriptTarget(content),
		ContentEncoding:  "plain",
		Content:          content,
	}
}

func isHookFastPathRepoRead(action guard.ActionInput, repoRoot string, decision guard.Decision) bool {
	actionType := strings.ToLower(strings.TrimSpace(action.ActionType))
	toolName := strings.ToLower(strings.TrimSpace(action.ToolName))
	if actionType != "read" || toolName != "read" {
		return false
	}
	if !hookReadTargetWithinRepo(action, repoRoot) {
		return false
	}
	candidate := action
	candidate.ProjectRoot = repoRoot
	if strings.TrimSpace(candidate.CWD) == "" {
		candidate.CWD = repoRoot
	}
	return decision.Decision == "allow" && decision.RiskLevel == "low"
}

func hookReadTargetWithinRepo(action guard.ActionInput, repoRoot string) bool {
	target := strings.TrimSpace(action.Target)
	if target == "" {
		return false
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	base := strings.TrimSpace(action.CWD)
	if base == "" {
		base = root
	}
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(base, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolvedRoot
	}
	if resolvedCandidate, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidate = resolvedCandidate
	} else {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." {
		return err == nil
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func isHookScriptTarget(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{".ps1", ".psm1", ".vbs", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".py", ".sh", ".bash", ".bat", ".cmd", ".pl", ".rb", ".php"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func hookAgentGuardURL() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("AGENTTOOLGATE_URL")), "/")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	return base + "/api/agent-guard/evaluate"
}

func callHookAgentGuard(payload hookAgentGuardRequest) (int, hookAgentGuardResponse, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return -1, hookAgentGuardResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, hookAgentGuardURL(), bytes.NewReader(raw))
	if err != nil {
		return -1, hookAgentGuardResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(os.Getenv("AGENTTOOLGATE_BEARER_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workspace := firstNonEmptyString(
		os.Getenv("AGENTTOOLGATE_WORKSPACE_ORG_ID"),
		os.Getenv("WORKSPACE_ORG_ID"),
	); workspace != "" {
		req.Header.Set("X-Workspace-Org-Id", workspace)
	}
	response, err := (&http.Client{Timeout: hookHTTPTimeout()}).Do(req)
	if err != nil {
		return 0, hookAgentGuardResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, hookAgentGuardResponse{}, err
	}
	var decision hookAgentGuardResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&decision); err != nil {
		return response.StatusCode, hookAgentGuardResponse{}, fmt.Errorf("invalid agent guard response")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return response.StatusCode, hookAgentGuardResponse{}, fmt.Errorf("invalid agent guard response")
	}
	return response.StatusCode, decision, nil
}

func hookHTTPTimeout() time.Duration {
	const defaultTimeout = time.Second
	raw := strings.TrimSpace(os.Getenv("AGENTTOOLGATE_HOOK_TIMEOUT_MS"))
	if raw == "" {
		return defaultTimeout
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 50 || value > 2000 {
		return defaultTimeout
	}
	return time.Duration(value) * time.Millisecond
}

func normalizeHookAgentGuardResponse(status int, response hookAgentGuardResponse) hookAgentGuardResponse {
	if status < 200 || status >= 300 {
		return hookAgentGuardResponse{
			Decision: "deny",
			Reason:   fmt.Sprintf("agenttoolgate request failed (HTTP %d)", status),
		}
	}
	response.Decision = strings.ToLower(strings.TrimSpace(response.Decision))
	switch response.Decision {
	case "allow", "deny":
		return response
	case "deny_with_ticket":
		if strings.TrimSpace(response.ApprovalID) != "" && strings.TrimSpace(response.Fingerprint) != "" {
			return response
		}
		return hookAgentGuardResponse{Decision: "deny", Reason: "agenttoolgate returned an invalid ticket response"}
	default:
		return hookAgentGuardResponse{Decision: "deny", Reason: "agenttoolgate returned an invalid decision"}
	}
}

func enforceHookDecisionFloor(local guard.Decision, request hookAgentGuardRequest, response hookAgentGuardResponse) hookAgentGuardResponse {
	switch strings.ToLower(strings.TrimSpace(local.Decision)) {
	case "deny":
		return hookAgentGuardResponse{
			Decision: "deny",
			Reason:   firstNonEmptyString(local.Reason, "Guard Core denied this action"),
		}
	case "ask":
		if strings.EqualFold(strings.TrimSpace(response.Decision), "allow") && strings.TrimSpace(request.TicketID) == "" {
			// 后端在低/中风险 remembered allow 时不会要求客户端再次携带 ticket，
			// 但会返回已审批票据的状态、ID 和指纹；只接受这组证据，避免普通
			// 的后端 allow 绕过本地 Guard Core 的 ask floor。
			approvalStatus := strings.ToLower(strings.TrimSpace(response.ApprovalStatus))
			if (approvalStatus == "approved" || approvalStatus == "consumed") &&
				strings.TrimSpace(response.ApprovalID) != "" &&
				strings.TrimSpace(response.Fingerprint) != "" {
				return response
			}
			return hookAgentGuardResponse{
				Decision: "deny",
				Reason:   firstNonEmptyString(local.Reason, "Guard Core requires confirmation"),
			}
		}
	}
	return response
}

func emitHookDecision(client string, decision hookAgentGuardResponse, stdout, stderr io.Writer) int {
	result := strings.ToLower(strings.TrimSpace(decision.Decision))
	if client == "codex" && result == "allow" {
		return 0
	}
	permission := "deny"
	if client == "claude" {
		switch result {
		case "allow":
			permission = "allow"
		case "deny_with_ticket":
			permission = "ask"
		}
	}
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		switch result {
		case "allow":
			reason = "allowed"
		case "deny_with_ticket":
			reason = "approval required"
		default:
			reason = "denied"
		}
	}
	if decision.ApprovalID != "" {
		reason += " (ticket: " + decision.ApprovalID + ")"
	}
	specific := map[string]string{
		"hookEventName":      "PreToolUse",
		"permissionDecision": permission,
	}
	if permission != "allow" {
		specific["permissionDecisionReason"] = reason
	}
	output := map[string]any{"hookSpecificOutput": specific}
	if err := json.NewEncoder(stdout).Encode(output); err != nil {
		fmt.Fprintln(stderr, "输出 hook 决策失败")
		return 1
	}
	return 0
}

func isExplicitLowRiskOfflineHook(action guard.ActionInput, decision guard.Decision) bool {
	if strings.ToLower(strings.TrimSpace(decision.Decision)) != "allow" || strings.ToLower(strings.TrimSpace(decision.RiskLevel)) != "low" {
		return false
	}
	if strings.TrimSpace(action.NetworkURL) != "" {
		return false
	}
	command := strings.TrimSpace(action.Command)
	if command == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(action.ActionType)) {
	case "command", "exec", "execute":
	default:
		return false
	}
	return guard.IsExplicitReadOnlyCommand(command)
}

func hookRequestDigest(payload hookAgentGuardRequest) string {
	fields := []any{
		payload.Adapter,
		payload.Tool,
		payload.ActionType,
		payload.Target,
		payload.Targets,
		payload.NetworkMethod,
		payload.NetworkURL,
		payload.WorkspaceRoot,
		payload.WorkingDirectory,
		payload.GuardDecision,
		payload.GuardRiskLevel,
		payload.IsScript,
		payload.ContentEncoding,
		payload.Content,
	}
	raw, _ := json.Marshal(fields)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func hookTicketPath(repoRoot string, payload hookAgentGuardRequest) string {
	return filepath.Join(repoRoot, ".tmp", "agenttoolgate", "hook-tickets", hookRequestDigest(payload)+".json")
}

func loadHookTicket(repoRoot string, payload hookAgentGuardRequest) (string, error) {
	path := hookTicketPath(repoRoot, payload)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var doc hookTicketDocument
	if err := json.Unmarshal(raw, &doc); err != nil ||
		strings.TrimSpace(doc.TicketID) == "" ||
		strings.TrimSpace(doc.Fingerprint) == "" ||
		doc.RequestDigest != hookRequestDigest(payload) ||
		doc.ExpiresAtUnix <= float64(time.Now().Unix()) {
		if err := removeHookTicket(path); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(doc.TicketID), nil
}

func updateHookTicket(repoRoot string, payload hookAgentGuardRequest, response hookAgentGuardResponse) error {
	switch response.Decision {
	case "deny_with_ticket":
		return writeHookTicket(repoRoot, payload, response)
	case "allow", "deny":
		return removeHookTicket(hookTicketPath(repoRoot, payload))
	}
	return nil
}

func removeHookTicket(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func writeHookTicket(repoRoot string, payload hookAgentGuardRequest, response hookAgentGuardResponse) error {
	if strings.TrimSpace(response.ApprovalID) == "" || strings.TrimSpace(response.Fingerprint) == "" {
		return errors.New("hook ticket is incomplete")
	}
	path := hookTicketPath(repoRoot, payload)
	doc := hookTicketDocument{
		TicketID:      strings.TrimSpace(response.ApprovalID),
		Fingerprint:   strings.TrimSpace(response.Fingerprint),
		RequestDigest: hookRequestDigest(payload),
		ExpiresAtUnix: float64(time.Now().Add(hookTicketTTL).Unix()),
	}
	return writeHookJSONFile(path, doc)
}

func writeHookJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func recordHookDryRun(repoRoot string, request hookAgentGuardRequest, decision guard.Decision) error {
	record := map[string]any{
		"workspace":       firstNonEmptyString(os.Getenv("AGENTTOOLGATE_WORKSPACE_ORG_ID"), os.Getenv("WORKSPACE_ORG_ID"), filepath.Base(repoRoot)),
		"actor":           firstNonEmptyString(os.Getenv("AGENTTOOLGATE_ACTOR"), os.Getenv("USER"), os.Getenv("USERNAME")),
		"adapter":         request.Adapter,
		"tool":            request.Tool,
		"action":          request.ActionType,
		"target":          redactHookTarget(request.Target),
		"mode":            "dry-run",
		"riskLevel":       decision.RiskLevel,
		"decisionPreview": decision.Decision,
		"signals":         decision.Signals,
		"time":            time.Now().UTC().Format(time.RFC3339Nano),
	}
	return appendHookJSONLine(filepath.Join(repoRoot, ".tmp", "agenttoolgate", "hook-dry-run.jsonl"), record)
}

func recordHookPendingAudit(repoRoot string, request hookAgentGuardRequest, reason string) error {
	record := map[string]any{
		"workspace": firstNonEmptyString(os.Getenv("AGENTTOOLGATE_WORKSPACE_ORG_ID"), os.Getenv("WORKSPACE_ORG_ID"), filepath.Base(repoRoot)),
		"actor":     firstNonEmptyString(os.Getenv("AGENTTOOLGATE_ACTOR"), os.Getenv("USER"), os.Getenv("USERNAME")),
		"tool":      request.Tool,
		"action":    request.ActionType,
		"target":    redactHookTarget(request.Target),
		"time":      time.Now().UTC().Format(time.RFC3339Nano),
		"reason":    reason,
		"offline":   true,
	}
	return appendHookJSONLine(filepath.Join(repoRoot, ".tmp", "local-action-firewall", "pending-audit.jsonl"), record)
}

func appendHookJSONLine(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(raw, '\n'))
	return err
}

var hookSensitiveTargetPattern = regexp.MustCompile(`(?i)\b(token|access_token|api_key|key|secret|password|auth|signature|cookie)\s*[:=]\s*([^\s&;]+)`)
var hookBearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)

func redactHookTarget(target string) string {
	trimmed := strings.TrimSpace(target)
	parsed, err := url.Parse(trimmed)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" {
		parsed.User = nil
		query := parsed.Query()
		for key := range query {
			if isSensitiveHookTargetKey(key) {
				query.Set(key, "[REDACTED]")
			}
		}
		parsed.RawQuery = query.Encode()
		parsed.Fragment = ""
		return parsed.String()
	}
	redacted := hookSensitiveTargetPattern.ReplaceAllString(trimmed, "$1=[REDACTED]")
	return hookBearerPattern.ReplaceAllString(redacted, "Bearer [REDACTED]")
}

func isSensitiveHookTargetKey(key string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
	for _, marker := range []string{"token", "secret", "password", "auth", "signature", "cookie"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "key" || normalized == "api_key" || strings.HasSuffix(normalized, "_key")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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

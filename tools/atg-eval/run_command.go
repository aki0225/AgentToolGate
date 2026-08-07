package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agenttoolgate/evaluation/internal/driver"
	"agenttoolgate/evaluation/internal/loader"
	"agenttoolgate/evaluation/internal/mockserver"
	"agenttoolgate/evaluation/internal/model"
	"agenttoolgate/evaluation/internal/redact"
	evalrunner "agenttoolgate/evaluation/internal/runner"
	"agenttoolgate/evaluation/internal/sandbox"
)

const (
	defaultSandboxBase  = ".tmp/evaluation"
	defaultGuardTimeout = 30 * time.Second
)

type documentRunner interface {
	Run(context.Context) evalrunner.Document
}

type runDependencies struct {
	loadCases               func(string) ([]model.Case, error)
	currentPlatform         func() (model.Platform, error)
	generateSyntheticSecret func() (string, error)
	resolveExecutable       func(string) (string, error)
	createSandbox           func(string, string) (*sandbox.Root, error)
	newMockServer           func(mockserver.Options) (*mockserver.Server, error)
	newDriver               func(driver.Config) (evalrunner.DecisionDriver, error)
	newRunner               func(evalrunner.Config) (documentRunner, error)
	closeMockServer         func(*mockserver.Server) error
	cleanupSandbox          func(*sandbox.Root) error
}

type runOptions struct {
	input        string
	atg          string
	runID        string
	sandboxBase  string
	guardTimeout time.Duration
}

func defaultRunDependencies() runDependencies {
	return runDependencies{
		loadCases:               loader.LoadFile,
		currentPlatform:         evalrunner.CurrentPlatform,
		generateSyntheticSecret: generateSyntheticSecret,
		resolveExecutable:       resolveATGExecutable,
		createSandbox:           sandbox.Create,
		newMockServer:           mockserver.New,
		newDriver: func(config driver.Config) (evalrunner.DecisionDriver, error) {
			return driver.New(config)
		},
		newRunner: func(config evalrunner.Config) (documentRunner, error) {
			return evalrunner.New(config)
		},
		closeMockServer: func(server *mockserver.Server) error {
			return server.Close()
		},
		cleanupSandbox: func(root *sandbox.Root) error {
			return root.Cleanup()
		},
	}
}

func runEvaluation(args []string, stdout, stderr io.Writer, dependencies runDependencies) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "待执行的 JSONL 文件")
	atg := flags.String("atg", "", "agenttoolgate 可执行文件")
	runID := flags.String("run-id", "", "本次评估 run ID")
	sandboxBase := flags.String("sandbox-base", defaultSandboxBase, "disposable sandbox base 目录")
	guardTimeout := flags.Duration("guard-timeout", defaultGuardTimeout, "单次 ATG Guard CLI 调用超时")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(stderr, "run 需要 --input <path>")
		return 2
	}
	if strings.TrimSpace(*atg) == "" {
		fmt.Fprintln(stderr, "run 需要 --atg <agenttoolgate executable>")
		return 2
	}
	if strings.TrimSpace(*runID) == "" {
		fmt.Fprintln(stderr, "run 需要 --run-id <id>")
		return 2
	}
	if strings.TrimSpace(*sandboxBase) == "" {
		fmt.Fprintln(stderr, "run 的 --sandbox-base 不能为空")
		return 2
	}
	if *guardTimeout <= 0 {
		fmt.Fprintln(stderr, "run 的 --guard-timeout 必须大于 0")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "run 不接受额外位置参数")
		return 2
	}

	options := runOptions{
		input:        strings.TrimSpace(*input),
		atg:          strings.TrimSpace(*atg),
		runID:        strings.TrimSpace(*runID),
		sandboxBase:  strings.TrimSpace(*sandboxBase),
		guardTimeout: *guardTimeout,
	}
	document, redactor, completed, runErr := executeEvaluation(context.Background(), options, dependencies)
	if completed {
		if err := writeRunDocument(stdout, redactor, document); err != nil {
			fmt.Fprintf(stderr, "输出评估结果失败：%v\n", err)
			if runErr != nil {
				writeRunError(stderr, redactor, runErr)
			}
			return 1
		}
	}
	if runErr != nil {
		writeRunError(stderr, redactor, runErr)
		return 1
	}
	if documentHasFailedResult(document) {
		return 1
	}
	return 0
}

func executeEvaluation(
	ctx context.Context,
	options runOptions,
	dependencies runDependencies,
) (
	document evalrunner.Document,
	redactor *redact.Redactor,
	completed bool,
	err error,
) {
	redactor = redact.New(redact.Options{})
	inputAbsolute, err := normalizeRunPath(options.input, "input")
	if err != nil {
		return document, redactor, false, err
	}
	sandboxBaseAbsolute, err := normalizeRunPath(options.sandboxBase, "sandbox base")
	if err != nil {
		return document, redactor, false, err
	}
	redactor = newRunRedactor("", inputAbsolute, "", sandboxBaseAbsolute, "")

	cases, err := dependencies.loadCases(inputAbsolute)
	if err != nil {
		return document, redactor, false, err
	}
	platform, err := dependencies.currentPlatform()
	if err != nil {
		return document, redactor, false, err
	}
	syntheticSecret, err := dependencies.generateSyntheticSecret()
	if err != nil {
		return document, redactor, false, fmt.Errorf("生成 synthetic secret 失败：%w", err)
	}
	executable, err := dependencies.resolveExecutable(options.atg)
	if err != nil {
		return document, redactor, false, fmt.Errorf("ATG 可执行文件不可用")
	}
	if !filepath.IsAbs(executable) {
		return document, redactor, false, fmt.Errorf("ATG 可执行文件不可用")
	}
	executable = filepath.Clean(executable)
	redactor = newRunRedactor(
		syntheticSecret,
		inputAbsolute,
		executable,
		sandboxBaseAbsolute,
		"",
	)

	root, err := dependencies.createSandbox(sandboxBaseAbsolute, options.runID)
	if err != nil {
		return document, redactor, false, fmt.Errorf("创建 sandbox 失败：%w", err)
	}
	redactor = newRunRedactor(
		syntheticSecret,
		inputAbsolute,
		executable,
		sandboxBaseAbsolute,
		root.Path(),
	)

	var server *mockserver.Server
	var decisionDriver evalrunner.DecisionDriver
	defer func() {
		var resourceErr error
		if closer, ok := decisionDriver.(interface{ Close() error }); ok {
			if closeErr := closer.Close(); closeErr != nil {
				resourceErr = errors.Join(resourceErr, fmt.Errorf("停止 ATG runtime 失败：%w", closeErr))
			}
		}
		if server != nil {
			if closeErr := dependencies.closeMockServer(server); closeErr != nil {
				resourceErr = errors.Join(resourceErr, fmt.Errorf("关闭 mock server 失败：%w", closeErr))
			}
		}
		if cleanupErr := dependencies.cleanupSandbox(root); cleanupErr != nil {
			resourceErr = errors.Join(resourceErr, fmt.Errorf("清理 sandbox 失败：%w", cleanupErr))
		}
		if resourceErr != nil {
			err = errors.Join(err, resourceErr)
		}
	}()

	server, err = dependencies.newMockServer(mockserver.Options{Redactor: redactor})
	if err != nil {
		return document, redactor, false, err
	}
	decisionDriver, err = dependencies.newDriver(driver.Config{
		Executable:        executable,
		Timeout:           options.guardTimeout,
		Redactor:          redactor,
		EnableMCP:         casesContainEntry(cases, model.EntryMCPInbound),
		RuntimeRoot:       root,
		MCPWorkspaceOrgID: "local-org",
		MCPStartupTimeout: options.guardTimeout,
	})
	if err != nil {
		return document, redactor, false, fmt.Errorf("创建 Guard CLI Driver 失败：%w", err)
	}
	instance, err := dependencies.newRunner(evalrunner.Config{
		RunID:           options.runID,
		Platform:        platform,
		Root:            root,
		Cases:           cases,
		Driver:          decisionDriver,
		MockServer:      server,
		SyntheticSecret: syntheticSecret,
		Redactor:        redactor,
	})
	if err != nil {
		return document, redactor, false, fmt.Errorf("创建 Runner 失败：%w", err)
	}

	document = instance.Run(ctx)
	completed = true
	return document, redactor, completed, nil
}

func generateSyntheticSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "atg-eval-" + hex.EncodeToString(value), nil
}

func resolveATGExecutable(raw string) (string, error) {
	resolved, err := exec.LookPath(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("ATG 可执行文件不可用")
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("解析 ATG 可执行文件失败")
	}
	info, err := os.Stat(absolute)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("ATG 可执行文件不可用")
	}
	return filepath.Clean(absolute), nil
}

func normalizeRunPath(raw, label string) (string, error) {
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("解析 %s 绝对路径失败", label)
	}
	return filepath.Clean(absolute), nil
}

func newRunRedactor(
	secret,
	inputAbsolute,
	executableAbsolute,
	sandboxBaseAbsolute,
	sandboxRootAbsolute string,
) *redact.Redactor {
	paths := make([]redact.PathReplacement, 0, 4)
	if inputAbsolute != "" {
		paths = append(paths, redact.PathReplacement{
			Path:        inputAbsolute,
			Replacement: "<input>",
		})
	}
	if executableAbsolute != "" {
		paths = append(paths, redact.PathReplacement{
			Path:        executableAbsolute,
			Replacement: "<atg>",
		})
	}
	if sandboxBaseAbsolute != "" {
		paths = append(paths, redact.PathReplacement{
			Path:        sandboxBaseAbsolute,
			Replacement: "<sandbox-base>",
		})
	}
	if sandboxRootAbsolute != "" {
		paths = append(paths, redact.PathReplacement{
			Path:        sandboxRootAbsolute,
			Replacement: "<sandbox>",
		})
	}
	secrets := []string{}
	if secret != "" {
		secrets = append(secrets, secret)
	}
	return redact.New(redact.Options{Secrets: secrets, Paths: paths})
}

func writeRunDocument(writer io.Writer, redactor *redact.Redactor, document evalrunner.Document) error {
	if redactor == nil {
		return fmt.Errorf("缺少结果脱敏器")
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return err
	}
	redacted, err := redactor.JSON(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(redacted))
	decoder.DisallowUnknownFields()
	var validated evalrunner.Document
	if err := decoder.Decode(&validated); err != nil {
		return fmt.Errorf("严格解析脱敏结果失败：%w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("脱敏结果只能包含一个 JSON Document")
		}
		return fmt.Errorf("脱敏结果包含无效尾部：%w", err)
	}
	for index, result := range validated.Results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("脱敏后结果语义无效：results[%d]：%w", index, err)
		}
	}
	var formatted bytes.Buffer
	encoder := json.NewEncoder(&formatted)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(validated); err != nil {
		return err
	}
	written, err := writer.Write(formatted.Bytes())
	if err != nil {
		return err
	}
	if written != formatted.Len() {
		return io.ErrShortWrite
	}
	return nil
}

func writeRunError(writer io.Writer, redactor *redact.Redactor, err error) {
	message := err.Error()
	if redactor != nil {
		message = redactor.Text(message)
	}
	fmt.Fprintf(writer, "评估运行失败：%s\n", message)
}

func documentHasFailedResult(document evalrunner.Document) bool {
	for _, result := range document.Results {
		if result.Status == model.ResultFailed {
			return true
		}
	}
	return false
}

func casesContainEntry(cases []model.Case, entry model.Entry) bool {
	for _, one := range cases {
		if one.Entry == entry {
			return true
		}
	}
	return false
}

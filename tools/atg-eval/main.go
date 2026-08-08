package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"agenttoolgate/evaluation/internal/loader"
)

type validationSummary struct {
	SchemaVersion string   `json:"schemaVersion"`
	CaseCount     int      `json:"caseCount"`
	CaseIDs       []string `json:"caseIds"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithDependencies(args, stdout, stderr, defaultRunDependencies())
}

func runWithDependencies(args []string, stdout, stderr io.Writer, dependencies runDependencies) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "run":
		return runEvaluation(args[1:], stdout, stderr, dependencies)
	default:
		fmt.Fprintf(stderr, "不支持的命令：%s\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "待校验的 JSONL 文件")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *input == "" {
		fmt.Fprintln(stderr, "validate 需要 --input <path>")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "validate 不接受额外位置参数")
		return 2
	}

	cases, err := loader.LoadFile(*input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	summary := validationSummary{
		SchemaVersion: "v1",
		CaseCount:     len(cases),
		CaseIDs:       make([]string, 0, len(cases)),
	}
	for _, c := range cases {
		summary.CaseIDs = append(summary.CaseIDs, c.ID)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		fmt.Fprintf(stderr, "输出校验结果失败：%v\n", err)
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, `AgentToolGate 评估工具

用法：
  atg-eval validate --input <cases.jsonl>
  atg-eval run --input <cases.jsonl> --atg <agenttoolgate> --run-id <id> --output <directory> [--sandbox-base <directory>] [--guard-timeout <duration>]

run 当前真实执行危险、良性和治理不变量 suite 中已登记的受限操作。包含 MCP Inbound
或 governance 用例时会在随机 loopback 端口启动隔离 ATG runtime；governance 使用
disposable SQLite、多 Actor 身份和 loopback OTel collector 验证真实不变量。
所有运行时资源都会在结束后受控清理。清理成功后，工具将 results.json、
run-manifest.json 和结构化 evidence 原子发布到独立 output；stdout 与 results.json
保持完全相同的字节。`)
}

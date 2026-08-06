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

当前 CLI 只提供严格契约校验；不会执行 Runner、治理不变量或 MCP Inbound，
报告生成将在后续阶段加入。`)
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"agenttoolgate/backend/internal/guard"
)

func runGuardExplain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "guard explain 需要输入类型：codex、claude 或 action")
		return 2
	}
	client := strings.ToLower(strings.TrimSpace(args[0]))
	if client != "codex" && client != "claude" && client != "action" {
		fmt.Fprintln(stderr, "guard explain 输入类型仅支持 codex、claude 或 action")
		return 2
	}
	inputPath, dir, format, err := parseGuardExplainArgs(args[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	root, err := resolveProjectRoot(dir)
	if err != nil {
		fmt.Fprintln(stderr, "无法解析项目目录")
		return 2
	}
	payload, err := guard.ReadAdapterPayload(inputPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	action, err := decodeGuardExplainAction(client, payload)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	action = bindHookActionToRepo(action, root)
	if err := guard.ValidateActionForExplanation(action); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	protection, err := guard.LoadProjectProtection(root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	explanation := guard.ExplainWithProjectProtection(action, protection)
	if err := writeGuardExplanation(stdout, explanation, format); err != nil {
		fmt.Fprintln(stderr, "输出 guard 解释失败")
		return 1
	}
	return 0
}

func parseGuardExplainArgs(args []string) (string, string, string, error) {
	inputPath := ""
	dir := ""
	format := "text"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" || arg == "--" {
			continue
		}
		switch arg {
		case "--input":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("guard explain 需要 --input <file|->")
			}
			inputPath = strings.TrimSpace(args[index])
		case "--dir":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("guard explain 需要 --dir <repo>")
			}
			dir = strings.TrimSpace(args[index])
		case "--format":
			index++
			if index >= len(args) {
				return "", "", "", fmt.Errorf("guard explain 需要 --format text 或 json")
			}
			format = strings.ToLower(strings.TrimSpace(args[index]))
		default:
			if value, ok := strings.CutPrefix(arg, "--input="); ok {
				inputPath = strings.TrimSpace(value)
				continue
			}
			if value, ok := strings.CutPrefix(arg, "--dir="); ok {
				dir = strings.TrimSpace(value)
				continue
			}
			if value, ok := strings.CutPrefix(arg, "--format="); ok {
				format = strings.ToLower(strings.TrimSpace(value))
				continue
			}
			return "", "", "", fmt.Errorf("guard explain 仅支持 --input、--dir 和 --format")
		}
	}
	if inputPath == "" {
		return "", "", "", fmt.Errorf("guard explain 需要 --input <file|->")
	}
	if dir == "" {
		return "", "", "", fmt.Errorf("guard explain 需要 --dir <repo>")
	}
	if format != "text" && format != "json" {
		return "", "", "", fmt.Errorf("guard explain 的 --format 仅支持 text 或 json")
	}
	return inputPath, dir, format, nil
}

func decodeGuardExplainAction(client string, payload []byte) (guard.ActionInput, error) {
	return guard.DecodeExplanationInput(client, payload)
}

func writeGuardExplanation(w io.Writer, explanation guard.ProjectExplanation, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(explanation)
	}
	fmt.Fprintln(w, "AgentToolGate Guard 解释")
	fmt.Fprintln(w, "=======================")
	fmt.Fprintln(w, "规范化目标:")
	if len(explanation.NormalizedTargets) == 0 {
		fmt.Fprintln(w, "  - none")
	}
	for _, target := range explanation.NormalizedTargets {
		label := escapeGuardExplanationText(target.Kind) + ": " + escapeGuardExplanationText(target.Value)
		if target.Operation != "" {
			label += " (" + escapeGuardExplanationText(target.Operation) + ")"
		}
		fmt.Fprintln(w, "  - "+label)
	}
	fmt.Fprintf(w, "内置决策: %s (%s, %s)\n",
		explanation.BuiltIn.Decision,
		explanation.BuiltIn.RiskLevel,
		explanation.BuiltIn.Category,
	)
	fmt.Fprintln(w, "命中规则:")
	if len(explanation.MatchedRules) == 0 {
		fmt.Fprintln(w, "  - none")
	}
	for _, match := range explanation.MatchedRules {
		if match.Kind == "protected_path" {
			fmt.Fprintf(w, "  - protected_path %s %s -> %s (%s)\n",
				escapeGuardExplanationText(match.Pattern),
				escapeGuardExplanationText(match.Operation),
				escapeGuardExplanationText(match.Effect),
				escapeGuardExplanationText(match.Target),
			)
			continue
		}
		fmt.Fprintf(w, "  - egress %s -> %s\n",
			escapeGuardExplanationText(match.Target),
			escapeGuardExplanationText(match.Effect),
		)
	}
	if explanation.Floor == nil {
		fmt.Fprintln(w, "项目 floor: none")
	} else {
		fmt.Fprintf(w, "项目 floor: %s (%s, %s)\n",
			explanation.Floor.Decision,
			explanation.Floor.RiskLevel,
			explanation.Floor.Category,
		)
	}
	fmt.Fprintf(w, "最终决策: %s (%s, %s)\n",
		explanation.Final.Decision,
		explanation.Final.RiskLevel,
		explanation.Final.Category,
	)
	return nil
}

func escapeGuardExplanationText(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if unicode.IsControl(r) {
				if r <= 0xffff {
					fmt.Fprintf(&builder, `\u%04X`, r)
				} else {
					fmt.Fprintf(&builder, `\U%08X`, r)
				}
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

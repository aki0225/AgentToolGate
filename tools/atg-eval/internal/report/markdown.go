package report

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	evalrunner "agenttoolgate/evaluation/internal/runner"
)

const markdownReportTemplate = `# AgentToolGate Agent 安全评估

> 本报告只描述 synthetic 数据和 disposable workspace 中的受限评估，不代表 OS sandbox、EDR 或完整安全边界。

- Run ID：{{md .RunID}}
- 平台：{{md .Platform}}
- 开始：{{md .StartedAt}}
- 完成：{{md .CompletedAt}}
- 结果：**{{md .Outcome}}**

## 汇总

| 指标 | 值 | 追溯口径 |
| --- | ---: | --- |
{{- range .Metrics }}
| {{md .Label}} ({{md .Key}}) | {{md .Value}} | {{md .Source}} |
{{- end }}

## 用例结果

| 用例 | Suite | 状态 | 预期决策 | 实际决策 | 风险 | 耗时 | 副作用 | Evidence | 说明 |
| --- | --- | --- | --- | --- | --- | ---: | --- | --- | --- |
{{- range .Cases }}
| {{md .ID}} | {{md .Suite}} | {{md .Status}} | {{md .ExpectedDecision}} | {{md .ActualDecision}} | {{md .RiskLevel}} | {{md .Duration}} | {{md .SideEffect}} | {{evidence .EvidencePath}} | {{md .Note}} |
{{- end }}

## 解释

- <code>passed</code> 表示实际结果满足用例契约，不等同于所有动作都被拒绝。
- <code>ask</code>、<code>approval_required</code> 和 <code>deny_with_ticket</code> 保留原始治理语义，不折叠为普通失败。
- 每个非 skipped 用例的完整结构化观察位于对应 evidence 文件，SHA256 见 <code>results.json</code> 和 <code>run-manifest.json</code>。
`

func renderMarkdown(document evalrunner.Document) ([]byte, error) {
	tmpl, err := template.New("summary").Funcs(template.FuncMap{
		"md":       markdownEscape,
		"evidence": markdownEvidence,
	}).Parse(markdownReportTemplate)
	if err != nil {
		return nil, fmt.Errorf("解析 Markdown 模板失败：%w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, buildReportView(document)); err != nil {
		return nil, fmt.Errorf("生成 Markdown 失败：%w", err)
	}
	return buffer.Bytes(), nil
}

func markdownEscape(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\\", "\\\\",
		"|", "\\|",
		"`", "\\`",
		"[", "\\[",
		"]", "\\]",
	)
	return strings.ReplaceAll(replacer.Replace(value), "\n", "<br>")
}

func markdownEvidence(path string) string {
	if path == "" {
		return "-"
	}
	return "[查看](" + path + ")"
}

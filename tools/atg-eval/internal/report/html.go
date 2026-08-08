package report

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"strings"

	evalrunner "agenttoolgate/evaluation/internal/runner"
)

const htmlReportTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="referrer" content="no-referrer">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'">
  <title>AgentToolGate 评估报告 · {{.RunID}}</title>
  <style>
    :root { color-scheme: light; --ink:#17201d; --muted:#66716d; --line:#d7ddda; --paper:#f7f9f8; --panel:#fff; --green:#1f6b4f; --red:#a33a36; --amber:#8a650f; --blue:#285f83; }
    * { box-sizing:border-box; }
    body { margin:0; color:var(--ink); background:var(--paper); font:14px/1.55 "Segoe UI Variable","Noto Sans SC",sans-serif; }
    main { width:min(1180px, calc(100% - 32px)); margin:0 auto; padding:48px 0 64px; }
    header { display:grid; grid-template-columns:1fr auto; gap:24px; align-items:end; padding-bottom:24px; border-bottom:2px solid var(--ink); }
    h1 { margin:0; font-size:28px; line-height:1.2; font-weight:680; letter-spacing:0; }
    h2 { margin:40px 0 12px; font-size:18px; letter-spacing:0; }
    p { margin:8px 0; }
    .eyebrow { margin:0 0 6px; color:var(--muted); font-size:12px; text-transform:uppercase; }
    .outcome { font-weight:700; color:var(--green); }
    .outcome-failed { color:var(--red); }
    .boundary { margin:20px 0 0; padding:12px 14px; border-left:3px solid var(--amber); background:#fffdf6; color:#514a35; }
    .facts { display:flex; flex-wrap:wrap; gap:10px 24px; margin:20px 0 0; color:var(--muted); }
    .facts strong { color:var(--ink); font-weight:620; }
    .table-wrap { overflow-x:auto; border-top:1px solid var(--line); border-bottom:1px solid var(--line); background:var(--panel); }
    table { width:100%; border-collapse:collapse; }
    th, td { padding:10px 12px; border-bottom:1px solid var(--line); text-align:left; vertical-align:top; white-space:nowrap; }
    th { color:var(--muted); background:#f1f4f2; font-size:12px; font-weight:650; }
    tr:last-child td { border-bottom:0; }
    td.wrap { min-width:240px; white-space:normal; }
    code { font:12px/1.5 "Cascadia Mono","Noto Sans Mono CJK SC",monospace; }
    a { color:var(--blue); text-underline-offset:2px; }
    .status { font-weight:680; }
    .status-passed { color:var(--green); }
    .status-failed { color:var(--red); }
    .status-skipped { color:var(--amber); }
    .explain { color:var(--muted); }
    footer { margin-top:40px; padding-top:16px; border-top:1px solid var(--line); color:var(--muted); font-size:12px; }
    @media (max-width:700px) { main { width:min(100% - 20px, 1180px); padding-top:28px; } header { grid-template-columns:1fr; } h1 { font-size:24px; } th, td { padding:9px 10px; } }
  </style>
</head>
<body>
<main>
  <header>
    <div>
      <p class="eyebrow">Agent Safety Evaluation · {{.Platform}}</p>
      <h1>AgentToolGate 评估报告</h1>
    </div>
    <div>结果：<span class="outcome outcome-{{.Outcome}}">{{.Outcome}}</span></div>
  </header>

  <p class="boundary">本报告只描述 synthetic 数据和 disposable workspace 中的受限评估，不代表 OS sandbox、EDR 或完整安全边界。</p>
  <div class="facts">
    <span>Run ID <strong>{{.RunID}}</strong></span>
    <span>开始 <strong>{{.StartedAt}}</strong></span>
    <span>完成 <strong>{{.CompletedAt}}</strong></span>
    <span>用例 <strong>{{.CaseCount}}</strong></span>
    <span>通过 <strong>{{.PassedCount}}</strong></span>
    <span>失败 <strong>{{.FailedCount}}</strong></span>
    <span>跳过 <strong>{{.SkippedCount}}</strong></span>
  </div>

  <section aria-labelledby="metrics-title">
    <h2 id="metrics-title">汇总指标</h2>
    <div class="table-wrap">
      <table>
        <thead><tr><th>指标</th><th>值</th><th>追溯口径</th></tr></thead>
        <tbody>
        {{range .Metrics}}<tr id="metric-{{.Key}}"><td>{{.Label}}<br><code>{{.Key}}</code></td><td>{{.Value}}</td><td class="wrap explain">{{.Source}}</td></tr>{{end}}
        </tbody>
      </table>
    </div>
  </section>

  <section aria-labelledby="cases-title">
    <h2 id="cases-title">用例结果</h2>
    <div class="table-wrap">
      <table>
        <thead><tr><th>用例</th><th>Suite</th><th>状态</th><th>预期决策</th><th>实际决策</th><th>风险</th><th>耗时</th><th>副作用</th><th>Evidence</th><th>说明</th></tr></thead>
        <tbody>
        {{range .Cases}}<tr>
          <td><code>{{.ID}}</code></td>
          <td><code>{{.Suite}}</code></td>
          <td class="status status-{{.Status}}">{{.Status}}</td>
          <td><code>{{.ExpectedDecision}}</code></td>
          <td><code>{{.ActualDecision}}</code></td>
          <td>{{.RiskLevel}}</td>
          <td>{{.Duration}}</td>
          <td class="wrap">{{.SideEffect}}</td>
          <td>{{if .EvidencePath}}<a href="{{.EvidencePath}}">查看</a>{{else}}-{{end}}</td>
          <td class="wrap">{{.Note}}{{if .Signals}}<br><span class="explain">signals: {{.Signals}}</span>{{end}}</td>
        </tr>{{end}}
        </tbody>
      </table>
    </div>
  </section>

  <section aria-labelledby="meaning-title">
    <h2 id="meaning-title">结果解释</h2>
    <p><code>passed</code> 表示实际结果满足用例契约，不等同于所有动作都被拒绝。</p>
    <p><code>ask</code>、<code>approval_required</code> 和 <code>deny_with_ticket</code> 保留原始治理语义，不折叠为普通失败。</p>
  </section>

  <footer>完整机器可读结果见 results.json；文件大小和 SHA256 见 run-manifest.json。</footer>
</main>
</body>
</html>
`

var externalHTMLResourcePattern = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["']\s*(?:https?:)?//`)

func renderHTML(document evalrunner.Document) ([]byte, error) {
	tmpl, err := template.New("report").Parse(htmlReportTemplate)
	if err != nil {
		return nil, fmt.Errorf("解析 HTML 模板失败：%w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, buildReportView(document)); err != nil {
		return nil, fmt.Errorf("生成 HTML 失败：%w", err)
	}
	raw := buffer.Bytes()
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{"<script", "url("} {
		if strings.Contains(lower, forbidden) {
			return nil, fmt.Errorf("HTML 包含外部资源或脚本标记：%s", forbidden)
		}
	}
	if externalHTMLResourcePattern.Match(raw) {
		return nil, fmt.Errorf("HTML 包含外部资源引用")
	}
	return raw, nil
}

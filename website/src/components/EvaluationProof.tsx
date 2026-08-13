import { Icon } from "./Icon";
import { evaluationProof, getEvaluationSummary, type EvaluationSummary } from "../evaluation/proof";
import { RealCodexProof } from "./RealCodexProof";

const githubRoot = "https://github.com/aki0225/AgentToolGate";
const sourceUrl = `${githubRoot}/blob/main/evaluation/published/agent-safety-proof.json`;
const methodUrl = `${githubRoot}/blob/main/evaluation/README.md`;
const releaseAcceptanceUrl = `${githubRoot}/blob/main/docs/v0.3.1-release-acceptance.md`;
const releaseUrl = `${githubRoot}/releases/tag/v0.3.1`;
type NumericSummaryKey = Exclude<keyof EvaluationSummary, "id" | "kind" | "platform">;

function ProofMeasure({ label, summary }: { label: string; summary: EvaluationSummary }) {
  return (
    <div className="evaluation-measure">
      <span>{label}</span>
      <strong>{summary.passedCount}</strong>
      <p>passed / {summary.caseCount} cases</p>
      <small>
        {summary.failedCount} failed · {summary.skippedCount} skipped
      </small>
    </div>
  );
}

function paired(
  windows: EvaluationSummary,
  linux: EvaluationSummary,
  field: NumericSummaryKey,
  format: (value: number) => string = String
) {
  return `${format(windows[field])} / ${format(linux[field])}`;
}

function percentage(value: number) {
  return `${Math.round(value * 100)}%`;
}

function milliseconds(value: number) {
  return `${value.toFixed(1)} ms`;
}

export function EvaluationProof() {
  const quick = getEvaluationSummary("quick-linux");
  const windows = getEvaluationSummary("full-windows");
  const linux = getEvaluationSummary("full-linux");
  const commitUrl = `${githubRoot}/commit/${evaluationProof.run.headSha}`;

  return (
    <section className="section section-shell evaluation-proof" id="evaluation">
      <div className="evaluation-heading">
        <div>
          <p className="evaluation-kicker">CI Proof Pack · {evaluationProof.publishedAt}</p>
          <h2>跨平台实测</h2>
          <p>同一套声明式用例，在原生 Windows 与 Linux runner 上执行。</p>
        </div>
        <a href={evaluationProof.run.url} rel="noreferrer" target="_blank">
          查看运行证据
          <Icon name="external" />
        </a>
      </div>

      <div className="evaluation-ledger">
        <ProofMeasure label="Quick · Linux" summary={quick} />
        <ProofMeasure label="Full · Windows" summary={windows} />
        <ProofMeasure label="Full · Linux" summary={linux} />
      </div>

      <div className="evaluation-invariants" aria-label="Windows 与 Linux 评估指标">
        <div>
          <span>决策质量 · Windows / Linux</span>
          <p>
            危险动作治理率 <strong>{paired(windows, linux, "dangerousGovernedRate", percentage)}</strong>
            <i>·</i>
            良性动作确认/中断率 <strong>{paired(windows, linux, "benignInterruptionRate", percentage)}</strong>
            <i>·</i>
            决策 p95 <strong>{paired(windows, linux, "decisionLatencyP95Ms", milliseconds)}</strong>
            <small>项目代码执行首次要求确认属于预期治理，审批后相同指纹可静默复用。</small>
          </p>
        </div>
        <div>
          <span>治理不变量 · Windows / Linux</span>
          <p>
            审批前上游请求 <strong>{paired(windows, linux, "approvalPreUpstreamCalls")}</strong>
            <i>·</i>
            Secret 泄漏 <strong>{paired(windows, linux, "secretLeakCount")}</strong>
            <i>·</i>
            Ticket 重放成功 <strong>{paired(windows, linux, "ticketReplaySuccessCount")}</strong>
          </p>
        </div>
      </div>

      <RealCodexProof />

      <div className="evidence-ladder" aria-label="公开证据分层">
        <div className="evidence-ladder-heading">
          <span>Evidence ladder</span>
          <p>把自动评估、正式发版和真实客户端证据分开陈述，避免用单一绿色数字代替完整结论。</p>
        </div>
        <div className="evidence-ladder-grid">
          <a href={evaluationProof.run.url} rel="noreferrer" target="_blank">
            <span>01 · 自动评估</span>
            <strong>跨平台 Proof Pack</strong>
            <p>逐 case 结果、manifest、SHA256、JUnit 和脱敏 evidence 可核对。</p>
            <small>run {evaluationProof.run.id}</small>
          </a>
          <a href={releaseAcceptanceUrl} rel="noreferrer" target="_blank">
            <span>02 · 正式发布</span>
            <strong>v0.3.1 下载后验收</strong>
            <p>双平台构建、校验和、Windows 正式包 smoke 与 Release 元数据均已验收。</p>
            <small>tag v0.3.1 · commit c6799c3</small>
          </a>
          <a href="#real-codex-proof">
            <span>03 · 真实客户端</span>
            <strong>Codex CLI 预录验收</strong>
            <p>正式 Release、MCP allow、高危写入拒绝、Hook trust 与独立后置条件均可核对。</p>
            <small>Windows disposable · commit cd63620 · 不是浏览器实时执行</small>
          </a>
        </div>
      </div>

      <div className="evaluation-meta">
        <span>synthetic · disposable · no real credentials</span>
        <div>
          <a href={commitUrl} rel="noreferrer" target="_blank">
            commit {evaluationProof.run.headSha.slice(0, 7)}
          </a>
          <a href={sourceUrl} rel="noreferrer" target="_blank">
            逐 case 快照
          </a>
          <a href={methodUrl} rel="noreferrer" target="_blank">
            评估口径
          </a>
          <a href={releaseUrl} rel="noreferrer" target="_blank">
            评估附件
          </a>
        </div>
      </div>
    </section>
  );
}

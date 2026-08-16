import { Icon } from "./Icon";
import { evaluationProof, getEvaluationSummary } from "../evaluation/proof";
import { realCodexProof } from "../evaluation/realCodexProof";
import { RealCodexProof } from "./RealCodexProof";

const githubRoot = "https://github.com/aki0225/AgentToolGate";
const methodUrl = `${githubRoot}/blob/main/evaluation/README.md`;
const releaseAcceptanceUrl = `${githubRoot}/blob/main/docs/v0.4.1-release-acceptance.md`;

function percentage(value: number) {
  return `${Math.round(value * 100)}%`;
}

export function EvaluationProof() {
  const windows = getEvaluationSummary("full-windows");
  const linux = getEvaluationSummary("full-linux");

  return (
    <section className="section section-shell evaluation-proof" id="evaluation">
      <div className="evaluation-heading">
        <div>
          <p className="evaluation-kicker">REAL CODEX + CI</p>
          <h2>实测证据</h2>
          <p>
            稳定版 v0.4.1；真实 Codex 证据来自 {realCodexProof.runtime.releaseTag} /{" "}
            {realCodexProof.source.commitSha.slice(0, 7)}；自动评估是{" "}
            {evaluationProof.run.headSha.slice(0, 7)} 历史快照。
          </p>
        </div>
        <a href={evaluationProof.run.url} rel="noreferrer" target="_blank">
          查看 CI
          <Icon name="external" />
        </a>
      </div>

      <div className="evaluation-summary" aria-label="跨平台自动评估摘要">
        <span>
          Windows <strong>{windows.passedCount} passed</strong>
        </span>
        <span>
          Linux{" "}
          <strong>
            {linux.passedCount} passed · {linux.skippedCount} skipped
          </strong>
        </span>
        <span>
          危险治理 W/L{" "}
          <strong>
            {percentage(windows.dangerousGovernedRate)} /{" "}
            {percentage(linux.dangerousGovernedRate)}
          </strong>
        </span>
        <span>
          良性中断 W/L{" "}
          <strong>
            {percentage(windows.benignInterruptionRate)} /{" "}
            {percentage(linux.benignInterruptionRate)}
          </strong>
        </span>
        <span>
          泄漏 <strong>{windows.secretLeakCount + linux.secretLeakCount}</strong>
          {" · "}重放{" "}
          <strong>
            {windows.ticketReplaySuccessCount + linux.ticketReplaySuccessCount}
          </strong>
        </span>
      </div>

      <RealCodexProof />

      <div className="evaluation-source-links" aria-label="公开验证资料">
        <span>证据</span>
        <a href={releaseAcceptanceUrl} rel="noreferrer" target="_blank">
          v0.4.1 发布验收
        </a>
        <a href={realCodexProof.source.evidenceUrl} rel="noreferrer" target="_blank">
          v0.3.2 真实证据
        </a>
        <a href={methodUrl} rel="noreferrer" target="_blank">
          e809c66 评估口径
        </a>
      </div>
    </section>
  );
}

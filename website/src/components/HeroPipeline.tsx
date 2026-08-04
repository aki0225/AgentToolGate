import type { CSSProperties } from "react";

import { Icon } from "./Icon";

const pipelineStages = [
  {
    name: "Codex / Claude",
    detail: "高危工具动作",
    state: "请求进入",
    tone: "info"
  },
  {
    name: "Hook / MCP",
    detail: "规范化调用上下文",
    state: "已接收",
    tone: "info"
  },
  {
    name: "Policy Decision",
    detail: "确定性规则与硬护栏",
    state: "require_approval",
    tone: "warning"
  },
  {
    name: "Approval Gate",
    detail: "独立 Reviewer",
    state: "在此暂停",
    tone: "warning"
  },
  {
    name: "Connector Runtime",
    detail: "批准前不触达上游",
    state: "0 requests",
    tone: "muted"
  },
  {
    name: "Redacted Audit",
    detail: "决策、理由与 trace id",
    state: "[REDACTED]",
    tone: "success"
  }
] as const;

export function HeroPipeline() {
  return (
    <figure className="hero-pipeline" aria-labelledby="hero-pipeline-title">
      <div className="pipeline-topline">
        <div>
          <span className="panel-kicker">GOVERNANCE TRACE / 001</span>
          <h2 id="hero-pipeline-title">请求穿过治理闸门</h2>
        </div>
        <span className="live-indicator">
          <span aria-hidden="true" />
          合成状态
        </span>
      </div>

      <div className="pipeline-layout">
        <div className="pipeline-track" aria-label="治理管线阶段">
          <div className="pipeline-line" aria-hidden="true">
            <span className="pipeline-line-active" />
            <span className="pipeline-request-dot" />
          </div>
          {pipelineStages.map((stage, index) => (
            <div
              className={`pipeline-stage pipeline-stage-${stage.tone}`}
              key={stage.name}
              style={{ "--stage-index": index } as CSSProperties}
            >
              <span className="pipeline-stage-index">{String(index + 1).padStart(2, "0")}</span>
              <span className="pipeline-stage-node" aria-hidden="true" />
              <div className="pipeline-stage-copy">
                <strong>{stage.name}</strong>
                <span>{stage.detail}</span>
                <code>{stage.state}</code>
              </div>
            </div>
          ))}
        </div>

        <aside className="pipeline-inspector" aria-label="当前风险解释">
          <div className="inspector-heading">
            <Icon name="warning" />
            <div>
              <span>RISK EXPLANATION</span>
              <strong>高风险写操作</strong>
            </div>
          </div>
          <dl>
            <div>
              <dt>策略结果</dt>
              <dd>
                <code>require_approval</code>
              </dd>
            </div>
            <div>
              <dt>调用状态</dt>
              <dd>
                <code>approval_required</code>
              </dd>
            </div>
            <div>
              <dt>上游计数</dt>
              <dd>
                <strong>0</strong>
              </dd>
            </div>
            <div>
              <dt>审计输入</dt>
              <dd>
                <code>[REDACTED]</code>
              </dd>
            </div>
          </dl>
          <p>
            审批通过前，请求不会进入 Connector Runtime。审批内部会暂存冻结执行参数，公开
            Audit 与 OTel 不保存原始敏感内容。
          </p>
        </aside>
      </div>

      <figcaption>
        纯 HTML/CSS 主视觉。动画只播放一次；启用“减少动态效果”后会直接呈现最终状态。
      </figcaption>
    </figure>
  );
}

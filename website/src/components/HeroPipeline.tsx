import type { CSSProperties } from "react";

import { Icon } from "./Icon";

const pipelineStages = [
  {
    name: "Agent Request",
    detail: "Codex · Claude · MCP"
  },
  {
    name: "Policy",
    detail: "allow · deny · approval"
  },
  {
    name: "Approval",
    detail: "高风险先暂停"
  },
  {
    name: "Runtime",
    detail: "批准后才执行"
  },
  {
    name: "Audit",
    detail: "脱敏记录"
  }
] as const;

const pipelineFacts = [
  {
    label: "批准前",
    value: "高风险调用不触达上游"
  },
  {
    label: "Secret",
    value: "不进入模型参数"
  },
  {
    label: "Audit",
    value: "只保存脱敏记录"
  }
] as const;

export function HeroPipeline() {
  return (
    <figure className="hero-pipeline" aria-labelledby="hero-pipeline-title">
      <div className="pipeline-heading">
        <div>
          <span>治理路径</span>
          <h2 id="hero-pipeline-title">一次调用，五个检查点</h2>
        </div>
        <Icon name="gate" />
      </div>

      <div className="pipeline-flow" aria-label="AgentToolGate 治理管线">
        {pipelineStages.map((stage, index) => (
          <div
            className="pipeline-stage"
            key={stage.name}
            style={{ "--stage-index": index } as CSSProperties}
          >
            <span className="pipeline-stage-index">{index + 1}</span>
            <div className="pipeline-stage-copy">
              <strong>{stage.name}</strong>
              <span>{stage.detail}</span>
            </div>
            {index < pipelineStages.length - 1 ? (
              <Icon className="pipeline-arrow" name="arrow" />
            ) : null}
          </div>
        ))}
      </div>

      <div className="pipeline-facts">
        {pipelineFacts.map((fact) => (
          <div className="pipeline-fact" key={fact.label}>
            <strong>{fact.label}</strong>
            <span>{fact.value}</span>
          </div>
        ))}
      </div>

      <figcaption className="pipeline-static-note">
        <Icon name="lock" />
        合成静态演示，不执行命令、不连接 Connector 或上游服务。
      </figcaption>
    </figure>
  );
}

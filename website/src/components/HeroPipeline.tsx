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

      <figcaption className="pipeline-static-note">
        <Icon name="lock" />
        静态示意，不执行真实调用。
      </figcaption>
    </figure>
  );
}

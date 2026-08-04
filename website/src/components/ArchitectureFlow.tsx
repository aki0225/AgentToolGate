import { Icon, type IconName } from "./Icon";

const workflowSteps: Array<{
  icon: IconName;
  title: string;
  description: string;
}> = [
  {
    icon: "gate",
    title: "接收工具调用",
    description:
      "REST 与 MCP Inbound 进入 Tool Registry；Codex / Claude Hook 进入 Local Guard 专用入口。"
  },
  {
    icon: "policy",
    title: "Policy 决策",
    description:
      "确定性规则和硬护栏给出 allow、deny 或 require_approval，MCP 不会形成治理旁路。"
  },
  {
    icon: "review",
    title: "Approval",
    description:
      "高风险写操作冻结参数并等待独立 Reviewer；批准前，上游请求计数保持为 0。"
  },
  {
    icon: "secret",
    title: "Runtime 与 Secret",
    description:
      "批准后 Connector Runtime 才执行；ATG 管理的 Secret 通过 env-backed valueRef 在运行时解析。"
  },
  {
    icon: "audit",
    title: "Audit 与 Local Guard",
    description:
      "主链路写入脱敏 Audit；本地高危动作使用一次性 ticket，不能记忆为长期静默放行。"
  }
];

export function ArchitectureFlow() {
  return (
    <ol className="workflow-steps" aria-label="AgentToolGate 工作方式">
      {workflowSteps.map((step, index) => (
        <li className="workflow-step" key={step.title}>
          <div className="workflow-step-marker">
            <span>{String(index + 1).padStart(2, "0")}</span>
            <Icon name={step.icon} />
          </div>
          <div className="workflow-step-copy">
            <h3>{step.title}</h3>
            <p>{step.description}</p>
          </div>
        </li>
      ))}
    </ol>
  );
}

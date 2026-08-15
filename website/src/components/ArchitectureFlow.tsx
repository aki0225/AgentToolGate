import { Icon, type IconName } from "./Icon";

const workflowSteps: Array<{
  icon: IconName;
  title: string;
  description: string;
}> = [
  {
    icon: "gate",
    title: "接收调用",
    description: "REST、MCP 与客户端 Hook 进入对应治理入口。"
  },
  {
    icon: "policy",
    title: "策略判定",
    description: "确定性规则给出 allow、deny 或 require_approval。"
  },
  {
    icon: "review",
    title: "人工审批",
    description: "高风险写操作冻结参数，批准前不触达上游。"
  },
  {
    icon: "secret",
    title: "安全执行",
    description: "批准后才执行，并在运行时解析 Secret 引用。"
  },
  {
    icon: "audit",
    title: "审计与本地防护",
    description: "记录脱敏 Audit；本地高危动作只使用一次性 ticket。"
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

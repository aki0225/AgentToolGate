import { Icon, type IconName } from "./Icon";

const workflowSteps: Array<{
  icon: IconName;
  title: string;
  description: string;
}> = [
  {
    icon: "gate",
    title: "分流入口",
    description: "REST / MCP 工具调用进入 createToolCall，本地 Hook 进入 Agent Guard。"
  },
  {
    icon: "policy",
    title: "确定性判定",
    description: "Policy、硬护栏与风险规则给出放行、拒绝或必要审批。"
  },
  {
    icon: "review",
    title: "按需人工审批",
    description: "Tool Registry 冻结参数；Local Action 创建一次性 ticket。"
  },
  {
    icon: "secret",
    title: "执行或精确重试",
    description: "后端按需重解析 Secret 并执行冻结参数；本地 ticket 只授权精确重试。"
  },
  {
    icon: "audit",
    title: "脱敏审计",
    description: "记录判定、审批与执行结果，不把原始 Secret 写入公开证据。"
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

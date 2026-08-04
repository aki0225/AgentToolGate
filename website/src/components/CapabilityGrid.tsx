import { Icon, type IconName } from "./Icon";

const comparisons = [
  {
    before: "模型直接调用高权限工具",
    after: "所有经 ATG 接入的 Tool Registry 调用先进入 Policy"
  },
  {
    before: "写操作立即触达上游",
    after: "高风险写操作进入 Approval，批准前上游计数为 0"
  },
  {
    before: "Connector token 进入模型参数和日志",
    after: "ATG 管理的 Connector Secret 由后端运行时注入"
  },
  {
    before: "失败后缺少可关联证据",
    after: "Audit reason 与 trace id 形成脱敏留痕"
  },
  {
    before: "本地命令只依赖客户端提示",
    after: "Local Action Firewall 补充风险解释与保守阻断"
  }
];

const capabilities: Array<{
  index: string;
  icon: IconName;
  title: string;
  description: string;
  code: string;
}> = [
  {
    index: "01",
    icon: "policy",
    title: "Policy",
    description: "默认 YAML 与 workspace 规则解释或收紧决策，不能绕过 adapter 硬护栏。",
    code: "allow / deny / require_approval"
  },
  {
    index: "02",
    icon: "review",
    title: "Approval",
    description: "人在回路、自批保护、冻结参数与原子状态迁移，拒绝和批准都有明确原因。",
    code: "approval_required"
  },
  {
    index: "03",
    icon: "secret",
    title: "Secret",
    description: "模型参数不接触 ATG 管理的 Connector Secret；后端最终执行前按 valueRef 解析。",
    code: "env-backed valueRef"
  },
  {
    index: "04",
    icon: "audit",
    title: "Audit",
    description: "保存决策、审批与脱敏输入输出，并用 trace id 关联 OpenTelemetry。",
    code: "[REDACTED]"
  },
  {
    index: "05",
    icon: "gate",
    title: "Local Guard",
    description: "对本地写入、执行、持久化和自我防篡改动作进行专用风险编排。",
    code: "deny_with_ticket"
  }
];

export function CapabilityGrid() {
  return (
    <>
      <div className="comparison-board" aria-label="未治理与经过 AgentToolGate 的对照">
        <div className="comparison-header comparison-header-before">
          <span>WITHOUT GOVERNANCE</span>
          <strong>未治理的 Agent</strong>
        </div>
        <div className="comparison-header comparison-header-after">
          <span>WITH AGENTTOOLGATE</span>
          <strong>经过治理闸门</strong>
        </div>
        {comparisons.map((item, index) => (
          <div className="comparison-row" key={item.before}>
            <div className="comparison-cell comparison-cell-before">
              <span className="comparison-index">{String(index + 1).padStart(2, "0")}</span>
              <p>{item.before}</p>
            </div>
            <div className="comparison-arrow" aria-hidden="true">
              <Icon name="arrow" />
            </div>
            <div className="comparison-cell comparison-cell-after">
              <Icon name="check" />
              <p>{item.after}</p>
            </div>
          </div>
        ))}
      </div>

      <div className="capability-ledger" aria-label="五层治理能力">
        {capabilities.map((capability) => (
          <article className="capability-entry" key={capability.title}>
            <div className="capability-number">{capability.index}</div>
            <Icon name={capability.icon} />
            <div>
              <h3>{capability.title}</h3>
              <p>{capability.description}</p>
              <code>{capability.code}</code>
            </div>
          </article>
        ))}
      </div>
    </>
  );
}

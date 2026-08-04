import { Icon } from "./Icon";

const toolRegistrySteps = [
  {
    label: "入口",
    title: "REST / MCP Inbound",
    detail: "/api/tool-calls · /mcp · /mcp/sse"
  },
  {
    label: "治理",
    title: "Policy + Hard Guardrails",
    detail: "workspace · rate limit · adapter validation"
  },
  {
    label: "人在回路",
    title: "Approval",
    detail: "require_approval · frozen arguments"
  },
  {
    label: "执行",
    title: "Connector Runtime",
    detail: "database · github · http · mcp_*"
  },
  {
    label: "证据",
    title: "Audit + OTel",
    detail: "redacted input/output · trace id"
  }
];

const localGuardSteps = [
  {
    label: "入口",
    title: "Codex / Claude Hook",
    detail: "/api/agent-guard/evaluate"
  },
  {
    label: "专用编排",
    title: "Guard Risk + Policy",
    detail: "action · target · signals · fingerprint"
  },
  {
    label: "保守阻断",
    title: "deny_with_ticket",
    detail: "首次判定创建待审 ticket"
  },
  {
    label: "一次性放行",
    title: "Approved Ticket",
    detail: "绑定 fingerprint · 单次消费"
  },
  {
    label: "证据",
    title: "Guard Audit",
    detail: "risk explanation · redacted target"
  }
];

export function ArchitectureFlow() {
  return (
    <div className="architecture-board">
      <div className="architecture-legend">
        <span>
          <i className="legend-dot legend-dot-primary" />
          Tool Registry 主链路
        </span>
        <span>
          <i className="legend-dot legend-dot-warning" />
          Local Guard 专用编排
        </span>
      </div>

      <div className="architecture-track architecture-track-primary">
        <div className="architecture-track-title">
          <span>TRACK A</span>
          <h3>所有经 ATG 接入的 Tool Registry 调用</h3>
          <p>MCP Inbound 不形成旁路；写类 MCP Outbound 工具仍进入同一治理链路。</p>
        </div>
        <div className="architecture-steps">
          {toolRegistrySteps.map((step, index) => (
            <div className="architecture-step" key={step.title}>
              <span>{step.label}</span>
              <strong>{step.title}</strong>
              <code>{step.detail}</code>
              {index < toolRegistrySteps.length - 1 ? (
                <Icon className="architecture-arrow" name="arrow" />
              ) : null}
            </div>
          ))}
        </div>
      </div>

      <div className="architecture-track architecture-track-warning">
        <div className="architecture-track-title">
          <span>TRACK B</span>
          <h3>Local Action Guard 独立入口</h3>
          <p>复用治理思想和存储对象，但不是物理调用 createToolCall。</p>
        </div>
        <div className="architecture-steps">
          {localGuardSteps.map((step, index) => (
            <div className="architecture-step" key={step.title}>
              <span>{step.label}</span>
              <strong>{step.title}</strong>
              <code>{step.detail}</code>
              {index < localGuardSteps.length - 1 ? (
                <Icon className="architecture-arrow" name="arrow" />
              ) : null}
            </div>
          ))}
        </div>
      </div>

      <div className="architecture-boundary-note">
        <Icon name="secret" />
        <p>
          <strong>Secret 边界：</strong>
          ATG 管理的 Connector Secret 不进入模型参数，由后端在最终 Connector Runtime
          解析 env-backed <code>valueRef</code>。缺失或禁用时 fail closed。
        </p>
      </div>

      <div className="architecture-limit-note">
        MCP Outbound 当前只描述已经实现的 HTTP + SSE 路径；不宣称完整 Streamable HTTP
        Outbound、OAuth 或 stdio。
      </div>
    </div>
  );
}

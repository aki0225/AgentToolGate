import { Icon } from "./Icon";

const canDo = [
  "为所有经 ATG 接入的 Tool Registry 调用执行确定性 Policy 与硬护栏",
  "在高风险写操作前创建 Approval，并保留自批保护与审阅理由",
  "让 ATG 管理的 Connector Secret 在后端运行时注入，而不是进入模型参数",
  "解释并阻断高危本地动作，使用一次性 ticket 限制重复消费",
  "按 workspace 留下脱敏 Audit，并用 trace id 关联 OpenTelemetry"
];

const cannotReplace = [
  "OS sandbox、kernel enforcement、EDR 或企业 DLP",
  "完整提示词注入检测或上下文净化方案",
  "KMS、Vault、云 Secret Manager 与完整凭据生命周期",
  "完整 MCP OAuth、Streamable HTTP Outbound 或 stdio Outbound",
  "企业级 RBAC、职责分离、灾备、SLO、告警与策略发布系统"
];

export function SecurityBoundary() {
  return (
    <div className="boundary-board">
      <section className="boundary-column boundary-column-can">
        <div className="boundary-heading">
          <Icon name="check" />
          <div>
            <span>WHAT IT CAN DO</span>
            <h3>能做：执行前治理，执行后留痕</h3>
          </div>
        </div>
        <ul>
          {canDo.map((item) => (
            <li key={item}>
              <Icon name="check" />
              <span>{item}</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="boundary-column boundary-column-cannot">
        <div className="boundary-heading">
          <Icon name="warning" />
          <div>
            <span>WHAT IT DOES NOT REPLACE</span>
            <h3>不能替代：系统与组织级安全边界</h3>
          </div>
        </div>
        <ul>
          {cannotReplace.map((item) => (
            <li key={item}>
              <Icon name="close" />
              <span>{item}</span>
            </li>
          ))}
        </ul>
      </section>

      <div className="boundary-disclosure">
        <div>
          <span>01 / 审批内部状态</span>
          <p>
            Audit 与 OTel 不保存原始敏感内容，但审批为了后续执行会在内部暂存冻结执行参数；
            approve 或 reject 完成后清空。
          </p>
        </div>
        <div>
          <span>02 / Codex 交互限制</span>
          <p>
            Codex 当前没有完整 interactive ask。需要人工确认的 Hook 动作采用保守 deny / no-op
            映射，不能宣传为完整审批弹窗。
          </p>
        </div>
        <div>
          <span>03 / 部署前提</span>
          <p>
            默认 local auth 只适合单机开发，不能直接暴露到公网。生产仍需要最小权限、系统隔离、
            网络策略和上游服务权限控制。
          </p>
        </div>
      </div>
    </div>
  );
}

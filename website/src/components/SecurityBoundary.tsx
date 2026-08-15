import { Icon } from "./Icon";

const canDo = [
  "接入 ATG 的调用先经过 Policy、硬护栏与必要审批。",
  "批准后才执行冻结参数，并解析 env-backed Secret。",
  "记录脱敏 Audit；本地高危动作使用一次性 ticket。"
];

const cannotReplace = [
  "OS sandbox、EDR、企业 DLP 或完整提示词注入防御。",
  "KMS / Vault、完整凭据生命周期与全部 MCP / OAuth 场景。",
  "生产身份、网络隔离、最小权限、RBAC、告警与灾备。"
];

const threatModelUrl =
  "https://github.com/aki0225/AgentToolGate/blob/main/docs/threat-model.md";

export function SecurityBoundary() {
  return (
    <div className="boundary-board">
      <section className="boundary-column boundary-column-can">
        <div className="boundary-heading">
          <Icon name="check" />
          <h3>能做</h3>
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
          <h3>不能替代</h3>
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

      <p className="boundary-warning">
        默认 <code>AUTH_MODE=local</code>、<code>LOCAL_ROLE=owner</code> 和{" "}
        <code>DEV_MODE=true</code> 仅供单机开发；多用户或网络暴露部署必须启用{" "}
        <code>OIDC</code>，否则等同于无鉴权访问。
      </p>

      <a className="boundary-link" href={threatModelUrl} rel="noreferrer" target="_blank">
        阅读完整威胁模型
        <Icon name="external" />
      </a>
    </div>
  );
}

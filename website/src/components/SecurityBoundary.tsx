import { Icon } from "./Icon";

const canDo = [
  "让接入 ATG 的工具调用先经过确定性 Policy、硬护栏和必要的人工审批。",
  "在批准后才执行冻结参数，并在 Connector Runtime 解析 env-backed Secret。",
  "为主链路留下脱敏 Audit，并用一次性 ticket 保守处理本地高危动作。"
];

const cannotReplace = [
  "OS sandbox、kernel enforcement、EDR、企业 DLP 或完整提示词注入防御。",
  "KMS / Vault、完整凭据生命周期，以及所有 MCP transport 与 OAuth 场景。",
  "生产身份、网络隔离、上游最小权限、企业 RBAC、告警、SLO 与灾备。"
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

      <a className="boundary-link" href={threatModelUrl} rel="noreferrer" target="_blank">
        阅读完整威胁模型
        <Icon name="external" />
      </a>
    </div>
  );
}

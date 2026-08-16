import { Icon } from "./Icon";

const canDo = [
  "REST / MCP 工具调用与本地 Hook 动作进入各自的治理入口。",
  "需审批的 GitHub / HTTP / MCP 调用先校验 Secret 引用，批准前不触达上游。",
  "Tool Registry 批准后由后端执行冻结参数；Local Action 使用一次性 ticket。"
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
        默认 Compose 只把宿主 <code>127.0.0.1:8080</code> 映射到容器内{" "}
        <code>0.0.0.0:8080</code>。<code>AUTH_MODE=local</code> 不验证 Bearer，
        默认角色是 <code>owner</code>；<code>DEV_MODE</code> 不是认证或授权开关。
        多用户或网络暴露部署必须启用 <code>OIDC</code> 并置于可信网络边界内。
      </p>

      <a className="boundary-link" href={threatModelUrl} rel="noreferrer" target="_blank">
        阅读完整威胁模型
        <Icon name="external" />
      </a>
    </div>
  );
}

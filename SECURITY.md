# 安全策略

## 支持版本

安全修复面向当前 `main` 分支和最新公开 GitHub Release。较早的 Release Candidate 可能只补充文档澄清，不应被视为长期支持版本。

## 报告漏洞

请优先通过 GitHub Security Advisories 私下报告安全问题。如果你的账号无法使用该入口，可以创建一个最小公开 issue，请求维护者提供私下联系路径，但不要贴出利用细节。

请不要在公开 issue、pull request、截图、日志或证据文件中包含真实 token、Authorization header、私钥、原始审批 payload、完整 fingerprint、数据库密码、DSN 密码或本机私有路径。

## 范围

AgentToolGate 是本地 AI 编程 Agent guardrail 和工具治理网关，用于在高风险工具调用触达本地文件、数据库、GitHub、HTTP 目标或外部 MCP 工具前进行评估、审批和审计。

AgentToolGate 不是完整沙箱、EDR、企业 DLP、操作系统权限边界或企业安全边界，也不能替代最小权限凭据。生产或高风险环境仍应配合 OS 权限、网络控制、Secret 管理和隔离执行环境。

## 敏感数据指引

分享诊断信息时：

- 请脱敏 token、API key、cookie、Authorization header、私钥、DSN 密码和环境变量值。
- 请把本机私有路径替换为 `<repo>`、`<temp-project>` 这类占位符。
- 不要分享完整 approval id、完整 fingerprint、原始请求体，或包含密钥的未脱敏脚本。
- 优先使用 synthetic 路径和假密钥构造最小复现 payload。

## 响应预期

维护者会尽力确认有效报告、复现问题、准备修复或缓解方案，并记录相关边界。本项目当前不提供企业 SLA、embargo program、付费漏洞赏金或长期支持承诺。

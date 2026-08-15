# 当前项目状态

> 状态日期：2026-08-15
>
> 当前稳定版：`v0.3.2`
>
> 稳定版产品提交：`60dd6dbd5dc7e59defd83cbad5f2508d11f4ec98`
>
> 本页更新前的 `main` 基线：`234cc3c21e62de0914e19026547a2d52b721bbc4`

## 1. 版本状态

`v0.3.2` 已冻结，Release 标签与验收过的产品提交保持一致。正式附件、双平台
Release workflow、原生 runner smoke，以及使用正式 Linux 包完成的五场景真实 Codex
CLI 验收见 [`v0.3.2 发布验收`](v0.3.2-release-acceptance.md)。

`v0.3.2` 之后的 `main` 目前只增加或维护：

- 真实 Codex 公开产物的敏感扫描测试。
- 五场景证据、GitHub Pages 派生数据与发布文档。
- GitHub Pages 信息层级、文案和前端验证门禁。

从 `v0.3.2` 到本页更新前的 `main`，没有修改 `backend/`、`frontend/` 控制台、
`configs/`、产品 Hook 或 Release 构建脚本。后续若产品代码发生变化，应创建新版本和
新的发布验收，不能把 `main` 的维护提交描述成已发布到 `v0.3.2`。

## 2. 已验证能力

当前公开仓已有以下分层证据：

- **稳定 Release**：Windows amd64 和 Linux amd64 正式包、SHA256、原生 runner smoke。
- **自动安全评估**：Quick、Windows full、Linux full 的逐 case Proof Pack；Linux
  平台不适用项保持 skipped，不冒充 passed。
- **真实客户端**：历史 Codex CLI / Claude Code 双客户端链路验收，以及
  `v0.3.2` 正式 Linux 包上的五次独立 Codex CLI 会话。
- **真实 Codex 五场景展示**：低摩擦开发、敏感读取、破坏性删除、网络外传和受保护
  写入，动作摘要均来自唯一 Hook 请求匹配，并带对应 Audit 和独立后置条件。
- **现行展示结构**：Hero、工作方式、实测证据、安全边界和下载；首页只保留真实
  Codex 五场景回放，不再展示早期 synthetic 交互状态机。
- **展示站门禁**：类型检查、静态证据校验、Vitest、生产构建和 Playwright 浏览器交互。

本页更新前的 `main` 已通过：

- CI run
  [`31818092277`](https://github.com/aki0225/AgentToolGate/actions/runs/31818092277)。
- 真实 Codex 五场景 run
  [`31818280270`](https://github.com/aki0225/AgentToolGate/actions/runs/31818280270)。

## 3. 当前使用入口

- 安装和日常运行：[`README.md`](../README.md)。
- 本地开发与 Hook control：[`local-daily-use.md`](local-daily-use.md)。
- Codex / Claude 接入：[`ai-client-integration.md`](ai-client-integration.md)。
- 项目内保护规则：[`project-protection-rules-live-dogfood.md`](project-protection-rules-live-dogfood.md)。
- 在线展示：<https://aki0225.github.io/AgentToolGate/>。

历史实施计划和 handoff 只用于保留实施过程与证据来源，不再作为当前开发恢复入口：

- [`agent-safety-evaluation-proof-pack-handoff.md`](agent-safety-evaluation-proof-pack-handoff.md)。
- [`github-pages-product-showcase-plan.md`](github-pages-product-showcase-plan.md)。
- [`real-codex-multi-scenario-showcase-plan.md`](real-codex-multi-scenario-showcase-plan.md)。
- [`real-codex-multi-scenario-showcase-handoff.md`](real-codex-multi-scenario-showcase-handoff.md)。

## 4. 安全边界

当前状态没有改变项目的基本边界：

- ATG 是工具调用治理网关和客户端 Hook guardrail，不是 OS sandbox、EDR 或完整 DLP。
- ATG 不阻止提示词注入发生，只降低注入得逞后的高危工具调用后果。
- Codex Hook 的 `ask` 仍保守映射为拒绝，不是完整交互审批。
- Hook 未加载、未信任、被绕过、处于 `off` / `dry-run` 或未覆盖工具路径时，没有
  ATG 实时阻断。
- Connector Secret 仍是 env-backed，不是 KMS 或 Vault。
- 默认 local owner 鉴权只适合单机开发；网络暴露或多用户部署必须启用 OIDC 并限制
  网络入口。

完整说明见 [`threat-model.md`](threat-model.md) 和
[`security-review-notes.md`](security-review-notes.md)。

## 5. 维护规则

- 不移动或覆盖 `v0.3.2`、`v0.3.1`、`v0.3.0`、`v0.2.0` 及其候选标签。
- 新增 Guard 规则、客户端、平台或产品行为时，重新生成对应评估与发布证据。
- 证据、展示或文档维护不能被描述成稳定 Release 产品能力变化。
- 发现安全缺陷时保留失败证据并单独修复，不通过放宽 expected decision 或跳过校验换取绿色结果。
- 当前状态发生实质变化时先更新本页，再更新 README 的短摘要；不要继续堆叠新的“当前
  handoff”。

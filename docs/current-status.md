# 当前项目状态

> 状态日期：2026-08-17
>
> 当前稳定版：`v0.4.2`
>
> 稳定版产品提交：`30be1cc2c99bda7e7013ca7f70f30bae47ee8421`
>
> 产品 CI：
> [`31991113892`](https://github.com/aki0225/AgentToolGate/actions/runs/31991113892)
>
> 正式 Release：
> [`v0.4.2`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.2)
>
> Release workflow：
> [`31991881698`](https://github.com/aki0225/AgentToolGate/actions/runs/31991881698)
>
> 稳定版产品代码基线：`30be1cc2c99bda7e7013ca7f70f30bae47ee8421`

## 1. 版本状态

`v0.4.2` 已正式发布，annotated tag 与产品提交
`30be1cc2c99bda7e7013ca7f70f30bae47ee8421` 保持一致。该补丁显式拒绝包含 NUL 的
Hook 工作目录，并增加 Python 3.10 / 3.14 跨平台兼容矩阵，不改变正常 Guard 决策、
审批或审计语义。产品 CI、双平台 Release、正式附件及当前验证边界见
[`v0.4.2 发布验收`](v0.4.2-release-acceptance.md)。

`v0.4.1` 的日常使用可靠性补丁、正式 Release 和完整评估复跑继续保存在
[`v0.4.1 发布验收`](v0.4.1-release-acceptance.md)。`v0.4.0` 的产品 CI、Release
自动化修复过程和独立附件复核继续保存在
[`v0.4.0 发布验收`](v0.4-release-acceptance.md)，不回写其历史 run、附件或结论。
旧标签均未移动。

统一的稳定版、发布门禁和历史快照入口见 [`证据索引`](evidence-index.md)。

## 2. 已验证能力

当前公开仓已有以下分层证据：

- **稳定 Release**：Windows amd64 和 Linux amd64 正式包、SHA256、原生 runner smoke。
- **自动安全评估**：最新完整版本化证据仍来自 `v0.4.1` 正式评估附件和
  [CI run 31954428232](https://github.com/aki0225/AgentToolGate/actions/runs/31954428232)，
  包含 Quick 20 passed、Windows full 30 passed、Linux full 26 passed + 4 个固定平台
  skipped；Release、workflow、Artifact 和逐 case SHA256 均写入
  [`proof.json`](../evaluation/published/agent-safety/releases/v0.4.1/proof.json)。
  `v0.4.2` Release workflow 已在原生 Windows / Linux runner 执行 quick suite，
  并从正式 Windows 附件独立复跑 20 / 20；当前尚未为 `v0.4.2` 生成新的完整 30-case
  双平台 Proof Pack。提交 `e809c66` 的 v1 快照继续作为历史证据保留。
- **真实客户端**：历史 Codex CLI / Claude Code 双客户端链路验收，以及
  `v0.3.2` 正式 Linux 包、源提交
  `0126bc24fb6189fd80b8070a23712a1e07b02514` 上的五次独立 Codex CLI 会话。
- **真实 Codex 五场景展示**：低摩擦开发、敏感读取、破坏性删除、网络外传和受保护
  写入，动作摘要均来自唯一 Hook 请求匹配，并带对应 Audit 和独立后置条件。
- **现行展示结构**：Hero、工作方式、实测证据、安全边界和下载；首页只保留真实
  Codex 五场景回放，不再展示早期 synthetic 交互状态机。
- **展示站门禁**：类型检查、静态证据校验、Vitest、生产构建和 Playwright 浏览器交互。
- **v0.4 本地验收**：三套 evaluator、Hook 延迟、独立嵌套仓库 off/dry-run/live、
  Secret/SSRF/MCP 边界、前端 40 项核心 Playwright、展示站 8 项 Playwright、后端
  全量测试、`go vet` 和 Windows amd64 release smoke。
- **v0.4.2 产品 CI**：产品提交 `30be1cc2c99bda7e7013ca7f70f30bae47ee8421`
  的 [CI run 31991113892](https://github.com/aki0225/AgentToolGate/actions/runs/31991113892)
  已通过 Linux/Windows Go、PostgreSQL、多 Actor、Agent Safety Evaluation、前端、
  展示站和 Connector smoke，并新增 Ubuntu / Python 3.10、Ubuntu / Python 3.14、
  Windows / Python 3.14 Hook 兼容矩阵。
- **v0.4.2 正式 Release**：
  [Release run 31991881698](https://github.com/aki0225/AgentToolGate/actions/runs/31991881698)
  已完成 Windows/Linux 主程序包和评估包构建、smoke、统一 `SHA256SUMS`、附件上传与
  正式发布；五个正式附件已从公开 Release 独立下载并核对 SHA256，Windows 主程序和
  quick 评估也已独立复跑。Linux 二进制的运行结论来自 Release workflow 的原生 Ubuntu
  runner，不用 Windows 解包检查冒充 Linux 运行。

以下运行记录保留为 `bf0bb9d` 阶段的历史快照，不用于证明当前 `main` 或
`v0.4.2` 稳定版：

- CI run
  [`31818092277`](https://github.com/aki0225/AgentToolGate/actions/runs/31818092277)。
- 真实 Codex 五场景 run
  [`31818280270`](https://github.com/aki0225/AgentToolGate/actions/runs/31818280270)。

## 3. 当前使用入口

- 安装和日常运行：[`README.md`](../README.md)。
- 稳定版、发布门禁和历史快照：[`evidence-index.md`](evidence-index.md)。
- 本地开发与 Hook control：[`local-daily-use.md`](local-daily-use.md)。
- Codex / Claude 接入：[`ai-client-integration.md`](ai-client-integration.md)。
- 项目内保护规则：[`project-protection-rules-live-dogfood.md`](project-protection-rules-live-dogfood.md)。
- 在线展示：<https://aki0225.github.io/AgentToolGate/>。

[`v0.4 日常使用加固计划`](v0.4-daily-use-hardening-plan.md) 已完成并形成
`v0.4.0` 基线，随后由 `v0.4.1` 完善新安装运行态与预览一致性，再由 `v0.4.2`
修复 Python 3.14 Hook 异常输入兼容。后续行为变化应建立新的任务和验收证据，不在
历史计划上继续追加，也不借稳定版发布扩展企业级功能。

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

- 不移动或覆盖 `v0.4.2`、`v0.4.1`、`v0.4.0`、`v0.3.2`、`v0.3.1`、`v0.3.0`、`v0.2.0`
  及其候选标签。
- 新增 Guard 规则、客户端、平台或产品行为时，重新生成对应评估与发布证据。
- 证据、展示或文档维护不能被描述成稳定 Release 产品能力变化。
- 发现安全缺陷时保留失败证据并单独修复，不通过放宽 expected decision 或跳过校验换取绿色结果。
- 当前状态发生实质变化时先更新本页，再更新 README 的短摘要；不要继续堆叠新的“当前
  handoff”。

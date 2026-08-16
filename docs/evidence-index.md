# 证据索引

本页区分当前稳定版证据、开发分支门禁和历史快照。只有已完成的 Release、CI run 或
真实客户端验收才算通过证据；workflow 源码和本地命令本身不等于远端运行结果。

## 1. 当前稳定版

当前稳定版是 `v0.3.2`，产品提交为
`60dd6dbd5dc7e59defd83cbad5f2508d11f4ec98`。

- [v0.3.2 正式发布与真实 Codex 验收](v0.3.2-release-acceptance.md)：双平台
  Release、正式附件 SHA256、正式 Linux 包上的五场景 Codex CLI 验收。
- [当前项目状态](current-status.md)：稳定版本、现行能力、限制和维护规则。
- [公开安全评估快照](../evaluation/published/agent-safety-proof.json)：逐 case
  Windows / Linux 评估结果及来源 Artifact。
- [真实 Codex 五场景公开证据](../evaluation/published/real-codex-demo-v2/)：
  脱敏录制、Audit、Hook trust、后置条件、清理结果和 manifest。

## 2. v0.4 开发门禁

`v0.4` 已完成本地候选验收，但仍不是已发布能力。详细命令、结果、故障样本和边界见
[`v0.4 发布候选验收`](v0.4-release-acceptance.md)。分支合入前仍应由
`.github/workflows/ci.yml` 验证：

- Linux 与 Windows 后端全量测试和 `go vet`。
- PostgreSQL store/app 测试，以及 requester/reviewer 两阶段多 Actor E2E。
- 前端类型检查、构建、核心 Playwright 和固定 allowlist 的 Connector E2E。
- 产品 Hook Bridge、真实 Codex 证据脚本和 evaluator 测试。

Release workflow 只允许带 `v` 前缀的严格 SemVer 标签。tag push 会从标签解析预期
提交；手动触发则必须显式提供完整 commit SHA。两种路径都会校验标签提交与实际打包
提交一致。Release job 由单一脚本创建 draft、逐项校验附件，再切换为公开 Release；
若同标签 Release 已存在则停止，不覆盖或追加附件。开发分支只有在对应 GitHub Actions
run 完成后，才能把 run ID 补充为发布证据。

## 3. 历史版本快照

以下文档记录各版本冻结时的事实，不代表它们仍是当前稳定版：

- [v0.3.1 正式发布验收](v0.3.1-release-acceptance.md)。
- [v0.3.1-rc1 候选发布验收](v0.3.1-rc1-release-acceptance.md)。
- [v0.3.0 正式发布验收](v0.3.0-release-acceptance.md)。

历史文档中的 run、附件、Hash、客户端版本和结论必须保持可追溯；后续修复应新增版本和
新证据，不能回写历史通过结果。

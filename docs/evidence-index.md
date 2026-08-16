# 证据索引

本页区分当前稳定版证据、现行发布门禁和历史快照。只有已完成的 Release、CI run 或
真实客户端验收才算通过证据；workflow 源码和本地命令本身不等于远端运行结果。

## 1. 当前稳定版

当前稳定版是 [`v0.4.1`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.1)，
产品提交为 `43868521e56c85cf074e92f572daff49121651b9`。

- [v0.4.1 正式发布验收](v0.4.1-release-acceptance.md)：产品提交 CI、双平台
  Release、正式附件 digest、补丁范围和独立下载验证边界。
- [当前项目状态](current-status.md)：稳定版本、现行能力、限制和维护规则。
- [公开安全评估快照](../evaluation/published/agent-safety-proof.json)：逐 case
  Windows / Linux 评估结果及来源 Artifact。
- [真实 Codex 五场景公开证据](../evaluation/published/real-codex-demo-v2/)：
  脱敏录制、Audit、Hook trust、后置条件、清理结果和 manifest。

## 2. v0.4.1 发布证据与门禁

- 产品提交 CI：
  [`31946327893`](https://github.com/aki0225/AgentToolGate/actions/runs/31946327893)，
  `completed/success`。
- 正式 Release workflow：
  [`31946508434`](https://github.com/aki0225/AgentToolGate/actions/runs/31946508434)，
  Windows/Linux 构建、smoke、附件上传和发布全部成功。
- 正式 Release：
  [`v0.4.1`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.1)，
  `draft=false`、`prerelease=false`，`releases/latest` 指向该版本。
- 正式附件：Windows/Linux 主程序包、Windows/Linux 评估包和 `SHA256SUMS`。
  GitHub 已发布五个附件的文件大小和 digest。本轮未在 workflow 外独立重新下载四个
  归档，因此不把归档可读性写成当前版本的独立验证结论。

Release workflow 只允许带 `v` 前缀的严格 SemVer 标签。tag push 会从标签解析预期
提交；手动触发则必须显式提供完整 commit SHA。两种路径都会校验标签提交与实际打包
提交一致，并要求该精确提交已有成功 CI。

Release job 先拒绝既有同标签 Release，再创建本轮 draft，按返回 ID 确认所有权，
逐项上传和校验附件，最后切换为公开 Release。失败时只删除本轮仍为 draft 且标签一致
的 Release；不覆盖、复用或追加既有 Release。

## 3. 历史版本快照

以下文档记录各版本冻结时的事实，不代表它们仍是当前稳定版：

- [v0.4.0 正式发布验收](v0.4-release-acceptance.md)。
- [v0.3.2 正式发布与真实 Codex 验收](v0.3.2-release-acceptance.md)。
- [v0.3.1 正式发布验收](v0.3.1-release-acceptance.md)。
- [v0.3.1-rc1 候选发布验收](v0.3.1-rc1-release-acceptance.md)。
- [v0.3.0 正式发布验收](v0.3.0-release-acceptance.md)。

历史文档中的 run、附件、Hash、客户端版本和结论必须保持可追溯；后续修复应新增版本和
新证据，不能回写历史通过结果。

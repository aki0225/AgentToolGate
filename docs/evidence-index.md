# 证据索引

本页区分当前稳定版证据、现行发布门禁和历史快照。只有已完成的 Release、CI run 或
真实客户端验收才算通过证据；workflow 源码和本地命令本身不等于远端运行结果。

## 1. 当前稳定版

当前稳定版是 [`v0.4.2`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.2)，
产品提交为 `30be1cc2c99bda7e7013ca7f70f30bae47ee8421`。

- [v0.4.2 正式发布验收](v0.4.2-release-acceptance.md)：产品提交 CI、双平台
  Release、正式附件 digest、Python 3.14 兼容补丁和独立下载验证边界。
- [当前项目状态](current-status.md)：稳定版本、现行能力、限制和维护规则。
- [v0.4.2 Release 安全评估证据](../evaluation/published/agent-safety/releases/v0.4.2/proof.json)：
  当前最新的完整 30-case 双平台 Proof Pack，绑定正式 Release、附件 digest、
  workflow provenance、Artifact ID 和逐 case SHA256。
- [真实 Codex 五场景公开证据](../evaluation/published/real-codex-demo-v2/)：
  脱敏录制、Audit、Hook trust、后置条件、清理结果和 manifest。

## 2. v0.4.2 发布证据与门禁

- 产品提交 CI：
  [`31991113892`](https://github.com/aki0225/AgentToolGate/actions/runs/31991113892)，
  `completed/success`。
- 正式 Release workflow：
  [`31991881698`](https://github.com/aki0225/AgentToolGate/actions/runs/31991881698)，
  Windows/Linux 构建、smoke、附件上传和发布全部成功。
- 正式 Release：
  [`v0.4.2`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.4.2)，
  `draft=false`、`prerelease=false`，`releases/latest` 指向该版本。
- 正式附件：Windows/Linux 主程序包、Windows/Linux 评估包和 `SHA256SUMS`。
  GitHub 已发布五个附件的文件大小和 digest。
- 原生双平台 quick：Release workflow 从各自构建的评估附件运行 20-case quick suite。
- 独立正式附件复验：从公开 Release 重新下载五个附件并核对
  `SHA256SUMS`；Windows 主程序 `doctor`、`/health`、`/` 和正式评估包 quick
  20 / 20 均通过。
- Python 3.14 补丁复验：Windows / Linux 评估包中的 Codex 与 Claude Hook 均在
  Python 3.14.3 下拒绝含 NUL 的工作目录。
- 正式附件完整评估：
  [CI run 31996457086](https://github.com/aki0225/AgentToolGate/actions/runs/31996457086)
  使用证据工具提交 `31aac418a24ee0e9710ebe47f95f095092aa37cc`，从 `v0.4.2`
  Release 重新下载评估附件并在原生 Windows / Linux runner 运行。
- 完整评估结果：Quick Linux 20 / 20、Windows full 30 / 30、Linux full
  26 passed + 4 个固定平台不适用 skipped；失败数均为 0。
- 治理不变量：审批前上游请求、Secret 泄漏和 Ticket 重放成功数均为 0。

本次正式证据 run 生成三个可下载 Artifact：

| Artifact | ID | GitHub Actions digest |
| --- | ---: | --- |
| `agent-safety-release-proof-pack-quick-v0.4.2-31996457086` | `9276977358` | `sha256:f637a5336b8f9fd156f37d46dd172dcb46ec791385741616d17862e9637bc32b` |
| `agent-safety-release-proof-pack-full-windows-v0.4.2-31996457086` | `9276977211` | `sha256:a5bf07e3b3f9174f57b1f485fbc7b3e9d8a4bc24fde81b21a21f9c389aeeb951` |
| `agent-safety-release-proof-pack-full-linux-v0.4.2-31996457086` | `9276977006` | `sha256:40c3feaf872b496a3df2c26e55404887cf035b43242bbab8412fef749420e2fd` |

三个 Artifact 已下载并按上述 digest 独立核对，随后确定性生成
[`v0.4.2 proof.json`](../evaluation/published/agent-safety/releases/v0.4.2/proof.json)。
`v0.4.1` 的 run、Artifact 和 Proof Pack 继续作为历史冻结证据，不改名、不回写。

Release workflow 只允许带 `v` 前缀的严格 SemVer 标签。tag push 会从标签解析预期
提交；手动触发则必须显式提供完整 commit SHA。两种路径都会校验标签提交与实际打包
提交一致，并要求该精确提交已有成功 CI。

Release job 先拒绝既有同标签 Release，再创建本轮 draft，按返回 ID 确认所有权，
逐项上传和校验附件，最后切换为公开 Release。失败时只删除本轮仍为 draft 且标签一致
的 Release；不覆盖、复用或追加既有 Release。

## 3. 历史版本快照

以下文档记录各版本冻结时的事实，不代表它们仍是当前稳定版：

- [v0.4.1 正式发布验收](v0.4.1-release-acceptance.md)。
- [v0.4.1 Release 安全评估证据](../evaluation/published/agent-safety/releases/v0.4.1/proof.json)：
  正式评估附件复跑 run `31954428232` 的上一稳定版冻结快照。
- [历史 v1 自动评估快照](../evaluation/published/agent-safety-proof.json)：提交
  `e809c66`、run `31465745397` 的旧公开结果。
- [v0.4.0 正式发布验收](v0.4-release-acceptance.md)。
- [v0.3.2 正式发布与真实 Codex 验收](v0.3.2-release-acceptance.md)。
- [v0.3.1 正式发布验收](v0.3.1-release-acceptance.md)。
- [v0.3.1-rc1 候选发布验收](v0.3.1-rc1-release-acceptance.md)。
- [v0.3.0 正式发布验收](v0.3.0-release-acceptance.md)。

历史文档中的 run、附件、Hash、客户端版本和结论必须保持可追溯；后续修复应新增版本和
新证据，不能回写历史通过结果。

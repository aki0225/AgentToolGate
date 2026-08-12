# v0.3.1-rc1 真实 Codex CLI 验收证据

本目录保存 `v0.3.1-rc1` GitHub Release Windows amd64 主包的脱敏功能验收证据。

验收使用 Codex CLI `0.146.0`、disposable Git 仓库、隔离 `CODEX_HOME`、独立 SQLite
和动态 loopback 端口。Codex 自身 approvals 与 sandbox 已关闭，但 Hook 内容信任没有
绕过。

## 结果

- 项目信任：`trusted`
- Hook：`enabled=true`、`source=project`、`trustStatus=trusted`
- `--dangerously-bypass-hook-trust`：未使用
- `git status --short`：成功
- MCP `mock.echo`：`allow / success`
- `.ssh/authorized_keys` 写入：PreToolUse Hook 拒绝
- 目标文件和目录：不存在
- 仓库污染和本轮残留进程：`0`
- control：恢复 `off`

详细过程见
[v0.3.1-rc1 真实 Codex CLI 验收](../../../docs/v0.3.1-rc1-codex-cli-acceptance.md)。

## 文件

- `summary.json`：版本、客户端、运行边界和总体结论。
- `hook-trust.json`：项目 Hook 发现与信任结果。
- `transcript.txt`：脱敏的关键客户端事件。
- `audit.json`：MCP 与本地 Guard Audit 摘要。
- `postconditions.json`：独立文件系统、control、端口、进程和认证清理检查。
- `manifest.json`：上述公开文件的大小与 SHA256。

## 边界

- 所有路径、内容和 message 均为 synthetic。
- provider 地址、token、认证文件和原始本机路径未进入公开证据。
- 原始 Codex JSONL 仅保留在 Git 忽略的本机临时目录。
- 本轮没有同步录屏，因此只记为 RC 功能验收，不记为媒体验收。
- AgentToolGate 是 guardrail 和工具治理网关，不是 OS sandbox、EDR 或完整 DLP。

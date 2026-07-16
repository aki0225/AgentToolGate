# Daily Use Acceptance

AgentToolGate 的本地 hook 目标不是把开发机变成企业安全平台，而是做一个轻量、可解释、可随时关闭的 AI coding agent safety guardrail：默认不打扰开发者；显式进入 `live` 后，在危险工具动作真正落地前阻断或拉回审批。

## 项目停止线

- AgentToolGate 是 local dangerous action firewall + tool governance gateway，不是操作系统级 enforcement boundary。
- 本轮验收只覆盖日常开发可用性与高危动作拦截，不扩展到 DLP、SIEM、LLM judge、taint tracking、自动规则降级或企业级策略平台。
- hook 只处理工具调用层面的后果，不声称能阻止提示词注入本身发生。
- 真实生产环境仍需要叠加 sandbox、最小权限、network policy 和 OS permission boundary。

## Threat Model

AI coding agent 可能因为提示词注入、上下文污染或模型误判而尝试调用本地工具。真正危险的时刻通常不是“模型说了一句坏话”，而是工具调用即将落地：

- 写入 Windows Startup、`.ssh`、`.env`、`.git/hooks` 或 agent 自身 hook 文件。
- 执行带 `ExecutionPolicy Bypass`、`WindowStyle Hidden` 或 encoded payload 的 PowerShell。
- 进行破坏性删除、持久化、自维护篡改或敏感凭据落点写入。

Daily Use Acceptance 只用 synthetic payload 验证分类与 hook 输出，不写真实 Startup、不写真实 `.ssh`，不执行破坏性命令。

## Protection Model

当前最小闭环由这些部分组成：

- Deterministic guard：先用确定性规则识别路径、命令和内容特征。
- Path classification：区分 workspace 普通动作、敏感落点、自维护篡改、持久化路径。
- Sensitive content scan：识别 hidden PowerShell、encoded payload、凭据路径等信号。
- Approval ticket and audit：后端 evaluate 路径可把高危动作变成 `deny_with_ticket`，并持久化解释与审计。
- Hot switch：repo-local `.tmp/agenttoolgate/hook-control.json` 支持 `off` / `dry-run` / `live`，缺失或损坏时默认 `off`。

## Modes

### `off`

默认安全姿态是 no-op。hook 立即返回：

- 不调用 backend。
- 不写 pending audit。
- 不输出 hook JSON。
- 不会把 Codex runtime 带回 `unsupported permissionDecision:allow`。

这保证普通开发不会因为 ATG 自己的开发仓库而被阻塞。

### `dry-run`

`dry-run` 只写 repo-local 预览：

```text
.tmp/agenttoolgate/hook-dry-run.jsonl
```

它不阻断、不调用后端、不执行 payload。预览只保留 mode、tool、action、脱敏 target、risk preview 和 signals。URL query 中的 `token`、`access_token`、`api_key`、`secret`、`password`、`auth`、`signature`、`cookie` 等值会被替换成 `[REDACTED]`。

### `live`

`live` 才启用真实 hook guardrail：

- Codex `allow` 结果表现为 no-op，不输出 `permissionDecision=allow`。
- Codex `ask` 保守映射为 `deny`，避免依赖当前 runtime 不支持的交互式 ask。
- 高危 synthetic 动作输出 `permissionDecision=deny`，或在后端 evaluate 路径进入 `deny_with_ticket`。
- `TRELLIS_HOOKS=0` 和 `TRELLIS_DISABLE_HOOKS=1` 始终是最高优先级硬关闭。

## Daily Developer Experience

验收中的普通动作均为 no-op，不向 Codex 输出 hook JSON：

- `git status`
- `go test ./...`
- `npm run check`
- workspace 内普通 docs 写入
- README / docs 类编辑
- `.tmp` 下临时文件写入

这说明 ATG 可以作为日常开发默认存在的 guardrail，而不是每个普通命令都打断用户。

## Disaster Prevention Cases

验收中的高危 synthetic payload 在 `live` 下被阻断：

- 破坏性删除：`Remove-Item -Recurse .`
- Windows Startup synthetic path 写入
- `.env` / `.ssh` / `secrets` 敏感落点
- `.git/hooks`、`.codex/hooks`、`.claude/hooks` 自维护篡改
- PowerShell `ExecutionPolicy Bypass` + `WindowStyle Hidden` + encoded payload
- ATG policy / service 文件在 CLI unavailable + backend offline fallback 下 fail-closed deny

所有场景都只作为 JSON payload 进入 hook，不执行真实命令，不创建真实文件。

## Evidence

脱敏 evidence 见：

- [`examples/agent-demo/evidence/daily-use-acceptance-output.txt`](../examples/agent-demo/evidence/daily-use-acceptance-output.txt)

该 evidence 使用 `<repo>` 替代本机路径，使用 `[REDACTED]`、`<approval-id-redacted>`、`<fingerprint-redacted>` 替代敏感值或完整标识。

## Residual Risks

- hook 不是完整 sandbox；它不能代替系统权限、网络隔离和只读文件系统。
- deterministic guard 只识别工具调用层面的可解释信号，不识别所有恶意提示词。
- 离线 fallback 是保守兜底，覆盖面和后端 evaluate / CLI core 不完全相同。
- Codex 当前不依赖交互式 ask；需要人工确认的动作会保守 deny。
- `live` 需要显式打开；缺失、损坏或无法解析的 control file 会按 `off` 处理，以保护开发会话不中断。

## Resume-Ready Summary

AgentToolGate 是一个面向 AI coding agent 的本地工具调用防火墙与治理网关。它不试图阻止提示词注入发生，而是在 agent 调用 Bash、Write、HTTP、GitHub、数据库或 MCP 工具前，用确定性策略识别高危动作，把写操作和敏感落点拉进 approval、audit 和 risk explanation。日常使用时它默认 no-op，不干扰 `git status`、测试和文档编辑；显式 `live` 后能阻断 Windows Startup 持久化、`.ssh`、`.env`、git hooks 和隐藏 PowerShell 等危险工具调用。这个项目的边界也很明确：它是 guardrail，不是 OS sandbox，上线仍要叠加最小权限和系统级隔离。

# 客户端配置片段示例

这些文件展示 `agenttoolgate.exe init all` 使用的客户端配置。AgentToolGate 不会自动写入用户全局配置，也不会自动建立 Codex trust。

- `codex.config.snippet.toml`：Codex 项目信任、Hook feature 与 `[mcp_servers.agenttoolgate]` 用户级配置键；按键合并，不要重复追加同名表。
- `codex.project-hook.snippet.toml`：已有项目 `.codex/config.toml` 时使用的 Hook 合并片段。
- `codex.hooks.json`：Codex 仍支持的项目 Hook JSON 兼容示例；与项目 TOML Hook 二选一。
- `claude.mcp.json`：Claude Code HTTP MCP 示例。
- `claude.settings.snippet.json`：Claude Code PreToolUse hook 片段。

ATG 当前选择由 `init codex` 生成 `.codex/config.toml`、`.codex/hooks/agent-guard-pretool.py` 和自包含的 `.codex/hooks/_guard_core.py`。Codex 同时支持项目 `.codex/hooks.json`，但同一配置层若同时存在 JSON 和 TOML Hook，会合并执行并发出警告，因此只选一种。检测到已有 `hooks.json` 时，普通初始化会在写入前停止；继续使用 JSON 时可用 `init codex --refresh-hooks` 单独安装或更新 adapter/Core。所有 Hook 都需要在 Codex `/hooks` 中核对和信任；刷新运行文件后还要重新运行 `up`，并复核 Hook trust。

默认 endpoint：

```text
Codex Streamable HTTP:  http://127.0.0.1:8080/mcp
Claude Streamable HTTP: http://127.0.0.1:8080/mcp
SSE fallback:           http://127.0.0.1:8080/mcp/sse
Workspace header:       X-Workspace-Org-Id: local-org
```

不要把真实敏感凭据、密钥明文、`.env` 内容或连接串密码写入这些片段。
可复制的 JSON 片段根部只保留客户端可消费字段，说明文字放在本文档，不写进 Claude 配置 JSON。

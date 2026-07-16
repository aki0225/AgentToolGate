# 客户端配置片段示例

这些文件展示 `agenttoolgate.exe init all` 会在目标项目的 `.agenttoolgate/clients/` 下生成什么内容。它们只用于复制或交给 ccswitch / 客户端配置管理工具，不会被 AgentToolGate 自动写入用户全局配置。

- `codex.config.snippet.toml`：Codex 项目信任与可直接复制的 `[mcp_servers.agenttoolgate]` 配置块。
- `codex.hooks.json`：Codex PreToolUse hook 片段，手动 opt-in 后可桥接到 Guard Core。
- `claude.mcp.json`：Claude Code HTTP MCP 示例。
- `claude.settings.snippet.json`：Claude Code PreToolUse hook 片段。

默认 endpoint：

```text
Codex Streamable HTTP:  http://127.0.0.1:8080/mcp
Claude Streamable HTTP: http://127.0.0.1:8080/mcp
SSE fallback:           http://127.0.0.1:8080/mcp/sse
Workspace header:       X-Workspace-Org-Id: local-org
```

不要把真实敏感凭据、密钥明文、`.env` 内容或连接串密码写入这些片段。
JSON 片段根部只保留客户端可消费字段，说明文字放在本文档，不写进可复制 JSON。

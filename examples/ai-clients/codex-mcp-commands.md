# Codex MCP 接入命令示例

> 这些命令只保存本地 MCP server 配置，不包含任何真实 token 或 Secret。

## 默认端口

AgentToolGate 已提供 Streamable HTTP endpoint `/mcp`，Codex CLI 推荐直接使用 `--url`：

```powershell
codex mcp add agenttoolgate --url http://127.0.0.1:8080/mcp
codex mcp list
```

这里不需要把 GitHub token、HTTP Authorization 或其它 Secret 写进 Codex 配置；真实上游凭据仍由 AgentToolGate Secret / Connector 在后端运行时注入。

## 自定义端口

AgentToolGate：

```powershell
.\agenttoolgate.exe --port 8090
```

Codex：

```powershell
codex mcp add agenttoolgate --url http://127.0.0.1:8090/mcp
codex mcp list
```

## 更新配置

不同 Codex 版本的 MCP 管理命令可能略有不同。通用做法是先删除旧 server，再按新 URL 添加：

```powershell
codex mcp remove agenttoolgate
codex mcp add agenttoolgate --url http://127.0.0.1:8090/mcp
codex mcp list
```

如果当前版本没有 `remove`，请使用 `codex mcp --help` 查看实际删除命令。

## SSE fallback

如果你的 Codex 版本暂时不能使用 Streamable HTTP direct URL，可把 AgentToolGate SSE endpoint 通过 `mcp-remote` bridge 成 stdio server：

```powershell
codex mcp add agenttoolgate -- npx -y mcp-remote http://127.0.0.1:8080/mcp/sse --header "X-Workspace-Org-Id: local-org"
codex mcp list
```

自定义端口时同步修改 SSE URL：

```powershell
codex mcp add agenttoolgate -- npx -y mcp-remote http://127.0.0.1:8090/mcp/sse --header "X-Workspace-Org-Id: local-org"
```

不要把 `/mcp/sse` 直接传给 `codex mcp add --url`；`--url` 应指向 Streamable HTTP endpoint `/mcp`。

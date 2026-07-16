# MCP Demo

这个目录提供 Claude Desktop 通过 MCP SSE 连接 AgentToolGate 的最小配置。

## 前置条件

1. 在仓库根目录启动本地 compose：

   ```bash
   docker compose up --build
   ```

2. 确认后端可访问：

   ```bash
   curl http://localhost:8080/health
   ```

3. 确认本地能运行 `npx`。Claude Desktop 配置里会通过 `npx -y mcp-remote` 连接 SSE endpoint。

## Claude Desktop 配置

Claude Desktop 的配置文件位置通常是：

- Windows：`%APPDATA%\Claude\claude_desktop_config.json`
- macOS：`~/Library/Application Support/Claude/claude_desktop_config.json`

把下面配置合并到 `mcpServers` 中，或直接复制 `examples/mcp-demo/mcp-config.json` 的内容：

```json
{
  "mcpServers": {
    "agenttoolgate": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "http://localhost:8080/mcp/sse",
        "--header",
        "X-Workspace-Org-Id: local-org"
      ]
    }
  }
}
```

保存后完全退出并重启 Claude Desktop。

## 验证方式

在 Claude Desktop 里可以要求：

```text
请列出 AgentToolGate 暴露的工具，然后用 mock.echo 回显 hello。
```

也可以请求：

```text
请用 database.query 查询 public.tools 的 namespace、name、operation_type。
```

当请求 `github.create_issue` 或其他写入类工具时，AgentToolGate 会返回需要审批的 MCP 错误。此时打开 `http://localhost:5173/approvals` 人工审批或拒绝，Claude Desktop 不会绕过网关审批。

## 残余风险

- 这里演示的是 MCP SSE 接入，不是完整的 enforcement boundary。
- 如果 AgentToolGate 后端未启动，高危落点会 fail-closed，普通 workspace 动作会 fail-open 并记录本地 pending audit。
- 仍建议叠加 sandbox、permission profile 和 network policy。

## 认证说明

compose 默认 `AUTH_MODE=local`，只需要传：

```text
X-Workspace-Org-Id: local-org
```

如果切换到 OIDC 模式，需要在 `mcp-remote` 参数里额外加入 `Authorization: Bearer <token>` header，并确保 token 属于目标 workspace。

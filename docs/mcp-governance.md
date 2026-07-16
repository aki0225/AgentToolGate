# MCP 治理

MCP 是 AgentToolGate 的一等治理面，不是附带功能，也不是旁路。ATG 同时治理 inbound MCP client 和 outbound MCP connector，并让两者都进入和 REST tool call 一致的 policy、approval、secret、rate limit、audit 链路。

## MCP 在 ATG 里的角色

MCP 有两个方向：

- **MCP Inbound**：AI client 把 ATG 当成 MCP Server，并调用 ATG 暴露的工具。
- **MCP Outbound**：ATG 把外部 MCP Server 当成 Connector，同步其 remote tools，再作为本地工具治理调用。

两个方向都保持 workspace-scoped、auditable，并受 policy / approval / secret / rate-limit 控制。

## MCP Inbound：`/mcp` 和 `/mcp/sse`

ATG 暴露：

```text
Streamable HTTP: /mcp
SSE fallback:    /mcp/sse
```

两个 endpoint 都挂在 auth middleware 后面。`/mcp` 是当前 Codex / Claude Code 类 HTTP MCP client 的默认推荐路径；`/mcp/sse` 保留给旧客户端或 `mcp-remote` fallback。

支持的 JSON-RPC method：

- `initialize`
- `tools/list`
- `tools/call`

`tools/list` 返回当前 workspace 启用工具。`tools/call` 不直接执行工具，而是调用应用层 `CallTool` adapter，再进入 `createToolCall`。

因此 MCP Inbound 复用：

- workspace 和 role 解析
- tool lookup
- rate limit
- default policy
- managed policy rules
- adapter hard validation
- approval queue
- connector runtime
- audit logs
- OpenTelemetry trace id

如果工具需要审批，MCP response 返回 `approval_required` JSON-RPC error，附带 call id、approval id、reason 等安全 metadata。审批前不会执行上游 connector。

## MCP Outbound：`mcp_<connector>.<tool>`

外部 MCP Server 配置为 `type=mcp` Connector。当前 outbound 实现支持旧版 HTTP + SSE transport。同步流程包括：

- 校验 connector config。
- 解析 env-backed `headerSecretRefs`。
- 调用外部 MCP Server 的 `initialize` 和 `tools/list`。
- 注册或更新本地 Tool Registry 条目。

远端工具会变成：

```text
mcp_<connector>.<remote_tool>
```

例如 connector `weather` 的 remote tool `get_forecast` 会成为：

```text
mcp_weather.get_forecast
```

sync 不覆盖人工 disabled 的本地工具。后续 sync 发现远端缺失工具时，只返回 stale 列表，不自动删除。

## 同步工具的治理规则

ATG 根据 MCP annotations 和工具名保守推断治理 metadata：

| 远端信号 | 本地治理结果 |
| --- | --- |
| `annotations.readOnlyHint=true` | `read`、`low`、默认不审批 |
| `annotations.destructiveHint=true` | `delete`、`high`、必须审批 |
| `annotations.openWorldHint=true` | `write`、`medium`、必须审批 |
| 名称以 `get`、`list`、`fetch`、`search` 开头 | read-like |
| 名称以 `create`、`update`、`write`、`post`、`send`、`call`、`delete`、`remove`、`destroy` 开头 | write/delete-like，必须审批 |
| 未知名称 | 保守视为 write/medium，必须审批 |

Workspace policy 可以收紧或解释这些结果，但不能绕过 connector config validation、secret resolution failure、MCP workspace isolation 或 payload redaction。

## Secret / Connector / Approval 关系

MCP Connector config 示例：

```json
{
  "transport": "sse",
  "url": "http://127.0.0.1:8081/mcp/sse",
  "headers": {
    "X-Demo": "hello"
  },
  "headerSecretRefs": {
    "Authorization": "mcp_auth_secret"
  },
  "timeoutMs": 3000
}
```

规则：

- 非敏感 demo header 可以放在 `headers`。
- 敏感 header 必须使用 `headerSecretRefs`。
- `headerSecretRefs` 指向 workspace Secret 名称。
- 当前 workspace Secret 只保存 env-backed `valueRef`，不保存 secret value。
- 后端只在 sync/call 执行时解析 env value。
- Secret 缺失、禁用或后端 runtime env 未配置时 fail closed，不触达外部 MCP Server。
- 解析后的 secret value 不进入 API response、audit、log、telemetry 或 frontend state。

对于 write/unknown/destructive MCP 工具，approval 创建在 outbound `tools/call` 之前。审批成功前，外部 MCP Server 不会收到真实调用。

## 拒绝和审批场景

MCP call 可能在触达上游前被 deny 或 failed：

- tool disabled
- connector missing、disabled 或属于其他 workspace
- connector URL invalid 或 transport unsupported
- header config invalid
- `headerSecretRefs` missing、disabled 或后端 runtime env 未配置
- arguments 不是 JSON object
- policy 返回 deny
- rate limit 超限
- write/unknown/destructive tool 需要 approval 且尚未 approved

这些路径仍应按治理语义写入 audit，并保持 input、output、error message 脱敏。

## 当前支持范围和限制

- MCP Inbound 支持最小 Streamable HTTP endpoint 和 SSE fallback，不是完整 resumability、OAuth 或 Dynamic Client Registration。
- MCP Outbound 当前支持 HTTP + SSE，不支持 stdio、OAuth、resources、prompts、sampling 或完整 Streamable HTTP outbound。
- 大 payload governance 仍是 MVP 级。
- 外部 MCP Server 不被默认信任。同步出来的 tool metadata 视为不可信，并保守映射治理级别。
- MCP governance 仍是 guardrail，不替代上游服务授权。

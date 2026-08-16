# MCP 治理

MCP 是 AgentToolGate 的一等治理面，不是附带功能，也不是工具调用旁路。MCP Inbound 的 `tools/call` 和同步后的 MCP Outbound 工具调用都会进入与 REST tool call 相同的治理链；Connector sync 本身则是独立的控制面操作。

## MCP 在 ATG 里的角色

MCP 有两个方向：

- **MCP Inbound**：AI client 把 ATG 当成 MCP Server，并调用 ATG 暴露的工具。
- **MCP Outbound**：ATG 把外部 MCP Server 当成 Connector，由控制面同步其 remote tools，再作为本地工具治理调用。

MCP Inbound 和同步后的 `mcp_<connector>.<tool>` 调用保持 workspace-scoped、auditable，并受 policy / approval / secret / rate-limit 控制。同步操作不使用这条 Tool Call 链。

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

Inbound 请求必须是单个 JSON 对象，拒绝尾随 JSON。`/mcp` 与 `/mcp/sse` 的 POST
请求和生成的 JSON-RPC 响应都限制为 1 MiB；超限响应会替换为稳定的内部错误，不把
部分结果继续写给客户端。

## Connector Sync：控制面操作

`POST /api/connectors/{id}/sync` 只允许 `owner/admin`。它不调用 `createToolCall`，
也不进入 Tool Call policy、approval、rate limit 或 Audit 链；其职责是校验 Connector、
解析同步所需的 Secret、发现远端工具，并原子更新 Tool Registry。同步阶段仍会执行
workspace 隔离、部署级 host ceiling、SSRF、payload 上限和响应脱敏等硬校验。

只有同步完成后的 `mcp_<connector>.<tool>` 被实际调用时，才进入完整治理链。

## MCP Outbound：`mcp_<connector>.<tool>`

外部 MCP Server 配置为 `type=mcp` Connector。当前 outbound 配置只接受
`transport=sse`，实现旧式 SSE transport over HTTP(S)。同步流程包括：

- 校验 connector config。
- 解析 `headerSecretRefs` 指向的 workspace Secret，再从 Secret 的 env-backed `valueRef` 读取后端运行时值。
- 调用外部 MCP Server 的 `initialize` 和 `tools/list`。
- 在首次写 store 前完整校验并转换整批 remote tools；非法名称、重复本地 key 或非对象 input schema 会让整批失败，不留下部分工具。
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
- 新建或更新的 MCP Connector 自动写入 workspace Secret 模式。没有 `secretRefMode` 的旧 Connector 也按 workspace Secret 解析，不再直接读取同名进程环境变量。
- 所有 MCP outbound 都必须配置非空 `MCP_ALLOWED_HOSTS`，Connector URL 必须命中该部署级上限；无 Secret 的 Connector 也不能绕过。Connector 只能继续缩小范围，不能扩大部署配置。
- 带 `headerSecretRefs` 的远程目标必须使用 HTTPS。HTTP 只允许显式列入 `MCP_ALLOWED_HOSTS` 的 `localhost`、`127.0.0.1` 或 `::1`，用于本地开发。
- metadata 与 link-local IP 即使写入 `MCP_ALLOWED_HOSTS` 也始终拒绝。
- MCP HTTP 重定向只允许协议、主机和有效端口均不变的同源跳转。跨 origin 跳转即使目标也在 allowlist 中仍会在发送 Secret header 或请求体前被拒绝。
- 每次请求和重定向都会重新解析并校验 DNS，实际拨号固定到本次校验通过的 IP；
  环境代理不参与目标选择。
- SSE 单行限制 64 KiB，单事件、POST 请求、POST 响应和 JSON-RPC result 均限制
  1 MiB；单次最多同步 256 个工具，单个 input schema 限制 64 KiB。
- JSON-RPC 响应必须是 `2.0`、ID 必须匹配当前请求，且 result/error 必须二选一。
- sync 会解析完成远端发现所需的 Secret；工具调用在审批创建前解析引用做 fail-closed 校验，真正执行时再次解析并注入。
- Secret 缺失、禁用或后端 runtime env 未配置时 fail closed，不触达外部 MCP Server。
- 解析后的 secret value 不进入 API response、audit、log、telemetry 或 frontend state。

对于 write/unknown/destructive MCP 工具，approval 创建在 outbound `tools/call` 之前。审批成功前，外部 MCP Server 不会收到真实调用。

批准动作完成状态转换后，后端会在同一批准请求中再次读取当前工具、策略和冻结参数，再执行 Connector；客户端不应重试同一工具调用。二次重验证失败时不会触达上游，工具调用标记为失败并清空冻结执行参数。该检查缩小了审批与执行之间的 TOCTOU 窗口，但不是 Store 与外部 Connector 之间的跨系统原子事务。

审批列表和批准/拒绝响应是安全投影：只返回脱敏摘要和稳定错误，不返回冻结执行参数、原始 Secret、URL 私密部分或底层 Connector 错误。

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
- MCP Outbound 当前只支持旧式 SSE transport over HTTP(S)，配置值只接受 `sse`；不支持 stdio、OAuth、resources、prompts、sampling 或完整 Streamable HTTP outbound。
- payload 使用固定技术上限，不支持按 connector、workspace 或工具配置分级额度。
- 显式 allowlist authority 可解析到普通 RFC1918、IPv6 ULA 或 CGNAT 私网地址；
  当前没有独立的公网/私网目标分区策略。
- 外部 MCP Server 不被默认信任。同步出来的 tool metadata 视为不可信，并保守映射治理级别。
- MCP governance 仍是 guardrail，不替代上游服务授权。

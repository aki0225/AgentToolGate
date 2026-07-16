# Codex / Claude Code 本地 MCP 接入指南

> 目标：让本地 AI 编程客户端先接入 AgentToolGate，再通过网关调用数据库、GitHub、HTTP 和 MCP 工具。AgentToolGate 是工具治理网关 / 防火墙，不是操作系统级强制沙箱。

## 1. 先启动 AgentToolGate

从 GitHub Release 下载后：

```powershell
.\agenttoolgate.exe --open
```

从源码构建后：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-local.ps1
.\dist\agenttoolgate.exe --open
```

默认地址：

```text
http://127.0.0.1:8080
```

如果端口被占用：

```powershell
.\agenttoolgate.exe --port 8090
# 或
.\agenttoolgate.exe --addr 127.0.0.1:8090
```

本地自检：

```powershell
.\agenttoolgate.exe doctor
```

doctor 会同时显示 MCP Streamable HTTP URL、MCP SSE URL、workspace header 示例和当前状态。不要把 doctor 输出当作 secret dump；它只显示 configured / missing 和安全摘要。

## 2. MCP Inbound endpoint

默认 endpoint：

```text
Streamable HTTP: http://127.0.0.1:8080/mcp
SSE fallback:    http://127.0.0.1:8080/mcp/sse
```

如果你用 `--port 8090` 启动，则改成：

```text
Streamable HTTP: http://127.0.0.1:8090/mcp
SSE fallback:    http://127.0.0.1:8090/mcp/sse
```

本地 local mode 默认 workspace header：

```text
X-Workspace-Org-Id: local-org
```

如果你改过 `DEFAULT_WORKSPACE_ORG_ID`，以启动摘要或 `agenttoolgate.exe doctor` 输出为准。

## 3. 推荐：项目级 init 生成客户端片段

在目标项目根目录运行：

```powershell
# 只用 Codex
agenttoolgate.exe init codex

# 只用 Claude Code
agenttoolgate.exe init claude

# 同时使用 Codex 和 Claude Code
agenttoolgate.exe init all
agenttoolgate.exe up
```

生成的 `.agenttoolgate/clients/` 目录包含 Codex 和 Claude Code 配置片段：

```text
.agenttoolgate/clients/codex.config.snippet.toml
.agenttoolgate/clients/codex.hooks.json
.agenttoolgate/clients/claude.mcp.json
.agenttoolgate/clients/claude.settings.snippet.json
```

这些文件用于复制到你自己的客户端配置管理工具，例如 ccswitch、Codex 用户配置或 Claude Code 项目配置。AgentToolGate 不会自动修改用户全局配置；默认 hook mode 是 `dry-run`，需要用户显式 opt-in 才进入真实阻断。
JSON snippets 根部只保留客户端可消费字段；“不会自动写全局配置”等说明放在文档里，便于直接复制到配置管理工具。

推荐复制口径：

- Codex / ccswitch：优先复制 `codex.config.snippet.toml` 中的 `[mcp_servers.agenttoolgate]`，URL 指向 `http://127.0.0.1:<port>/mcp`。
- Codex hook：需要本地动作 guard 时，再复制 `codex.hooks.json`；默认仍建议先保持项目 hook mode 为 `dry-run`。
- Claude Code：复制 `claude.mcp.json` 或 `claude.settings.snippet.json`；Claude 默认使用 HTTP `/mcp`，workspace header 是 `X-Workspace-Org-Id`。

如果你只需要一个客户端：

```powershell
agenttoolgate.exe init codex
agenttoolgate.exe init claude
```

静态示例见 [`examples/client-configs/`](../examples/client-configs/)。

## 4. Claude Code 接入

### 4.1 项目级 `.mcp.json`

在项目根目录放一个 `.mcp.json`，示例见 [`examples/ai-clients/claude-code.mcp.json`](../examples/ai-clients/claude-code.mcp.json)：

```json
{
  "mcpServers": {
    "agenttoolgate": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": {
        "X-Workspace-Org-Id": "local-org"
      }
    }
  }
}
```

项目级配置的优点是团队成员能看到接入方式；缺点是路径和 workspace 可能因个人环境不同而需要本地覆盖。不要把 token 或 Secret 明文写进 `.mcp.json`。

### 4.2 CLI 添加方式

```powershell
claude mcp add agenttoolgate --transport http http://127.0.0.1:8080/mcp --header "X-Workspace-Org-Id: local-org"
claude mcp list
claude mcp get agenttoolgate
```

不同 Claude Code 版本的参数细节可能略有差异；如果命令提示参数名不同，保留三个关键信息即可：transport 是 `http`、URL 是 `/mcp`、header 是 `X-Workspace-Org-Id: local-org`。旧客户端无法使用 HTTP transport 时，才把 `/mcp/sse` 当作 fallback。

在 Claude Code 里也可以用 `/mcp` 查看 server 状态。

## 5. Codex CLI 接入

当前本地验证到的 Codex CLI `codex mcp add --url` 面向 Streamable HTTP MCP server。AgentToolGate 已提供最小 Streamable HTTP Inbound `/mcp`，所以 Codex 推荐使用 direct URL：

```powershell
codex mcp add agenttoolgate --url http://127.0.0.1:8080/mcp
codex mcp list
```

自定义端口：

```powershell
codex mcp add agenttoolgate --url http://127.0.0.1:8090/mcp
codex mcp list
```

如果你的 Codex 版本暂时无法使用 Streamable HTTP direct URL，可降级使用 `mcp-remote` 把 AgentToolGate SSE bridge 成 stdio server：

```powershell
codex mcp add agenttoolgate -- npx -y mcp-remote http://127.0.0.1:8080/mcp/sse --header "X-Workspace-Org-Id: local-org"
codex mcp add agenttoolgate -- npx -y mcp-remote http://127.0.0.1:8090/mcp/sse --header "X-Workspace-Org-Id: local-org"
```

更多命令模板见 [`examples/ai-clients/codex-mcp-commands.md`](../examples/ai-clients/codex-mcp-commands.md)。

## 6. 最小 smoke prompts

示例 prompt 见 [`examples/ai-clients/smoke-prompts.md`](../examples/ai-clients/smoke-prompts.md)。建议按顺序做：

1. “列出 AgentToolGate 暴露的 MCP 工具。”
2. “调用 `mock.echo`，参数 `message=hello from ai client`。”
3. “触发一个需要 approval 的写操作，例如 HTTP POST 或 GitHub create issue；如果返回 approval_required，请告诉我 approval id，不要当成失败。”
4. “我在 UI 审批后，请重试或让我到 Audit Logs 查看结果。”

## 7. 治理语义

- AI client 不应直接持有数据库、GitHub、HTTP、外部 MCP 上游凭据；凭据通过 AgentToolGate Secret / Connector 在后端运行时注入。
- `mock.echo` 这类低风险读/演示工具可直接成功，并写入 Audit Logs。
- `approval_required` 表示请求已经进入审批队列，审批前不会执行高风险上游操作。
- 审批通过后，客户端应重试，或让用户在 AgentToolGate UI / Audit Logs 查看结果。
- Codex 如果没有原生 ask/defer 交互，就使用“ticket / UI approval / retry”的心智模型，不要把 pending approval 说成成功。

## 8. 常见错误排查

| 现象 | 处理方式 |
| --- | --- |
| 连接 `/mcp` 或 `/mcp/sse` 失败 | 确认 `agenttoolgate.exe` 正在运行，端口和 URL 与 doctor 输出一致。 |
| 工具列表为空 | 确认使用的是 `local-org` workspace，且本地初始化已完成；打开 `/tools` 看工具是否存在。 |
| 返回 401 / workspace 不对 | local mode 下补 `X-Workspace-Org-Id: local-org`；OIDC 模式需要真实 bearer token，本指南不覆盖。 |
| 返回 `approval_required` | 这是治理命中，不是失败；去 UI 的 Approvals 审批，再重试或看 Audit Logs。 |
| Codex 无法连接 SSE | 不要用 `codex mcp add --url .../mcp/sse`；优先改用 `--url .../mcp`，旧客户端再用 `mcp-remote` bridge 成 stdio。 |
| 端口被占用 | 用 `--port 8090` 或 `--addr 127.0.0.1:8090` 重新启动，并同步修改 MCP URL。 |

## 9. 安全边界

- 不把 token、Secret、DSN 密码写入 `.mcp.json`、示例文件、prompt 或截图。
- `X-Workspace-Org-Id` 只是 workspace 选择，不是 secret。
- AgentToolGate 是本地工具治理网关，不是 OS 级强制沙箱；真实高风险动作仍建议叠加系统权限、网络策略和最小权限凭据。

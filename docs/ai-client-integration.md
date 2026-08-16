# Codex / Claude Code 本地 MCP 接入指南

> 目标：让本地 AI 编程客户端先接入 AgentToolGate，再通过网关调用数据库、GitHub、HTTP 和 MCP 工具。AgentToolGate 是工具治理网关 / 防火墙，不是操作系统级强制沙箱。

> [!IMPORTANT]
> 本文所述新版 `init codex`、配套 `doctor` 检查、项目 TOML 和自包含 Hook 已在
> AgentToolGate 当前稳定版 `v0.4.0` 提供。请从 GitHub Release 下载正式包；
> `v0.3.0` 的旧接入方式只保留用于历史版本。

## 1. 选择启动方式

要使用 Codex 项目 Hook，必须先初始化，再只用 `up` 启动：

```powershell
agenttoolgate.exe init codex
agenttoolgate.exe up --open
```

`init codex` 和 `init all` 要求目标目录本身就是 Git 仓库根目录，不能指向普通目录或外层仓库中的任意子目录。

如果普通 serve 已通过 `agenttoolgate.exe --open` 等方式运行，先在原终端按 `Ctrl+C` 停止，再运行 `up`；否则两个进程会争用默认 `8080` 端口。其他客户端的初始化选项见第 3 节。

仅使用 MCP、无需项目 Hook 时，才直接启动普通 serve。从 GitHub Release 下载后：

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

doctor 会显示 MCP URL、安全摘要，并报告 Codex 项目配置的 `missing/unreadable/configured/custom`、`hooks.json` 是否存在、adapter/Core 的 `missing/unreadable/current/modified`、Git/Python 命令可用性和 repo-local hook mode。它也会警告同层 JSON/TOML Hook 冲突。这些状态不代表 Codex 已建立项目 trust、Hook trust 或进入 `live`。不要把 doctor 输出当作 secret dump；它不会打印 Secret 明文。

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
# 从下面三种 init 中任选一种
agenttoolgate.exe init codex
# 或
agenttoolgate.exe init claude
# 或
agenttoolgate.exe init all

# init 完成后只用 up 启动
agenttoolgate.exe up --open
```

`up` 运行后，可在另一个终端执行 `agenttoolgate.exe doctor --dir <project>` 核对 adapter、Core、mode 和实际 endpoint。

Codex 初始化会生成实际项目 Hook 和用户级配置片段：

```text
.codex/config.toml
.codex/hooks/agent-guard-pretool.py
.codex/hooks/_guard_core.py
.agenttoolgate/clients/codex.config.snippet.toml
.agenttoolgate/clients/codex.project-hook.snippet.toml
```

`.codex/config.toml` 和 `.codex/hooks/` 是 Codex 直接读取的项目文件；Python adapter 与离线 Guard Core 会一起安装，不依赖同项目的 `.claude/hooks/`。Codex 的 `.agenttoolgate/clients/` 文件用于复制到 ccswitch 或 Codex 用户配置。`init claude` / `init all` 才会额外生成 Claude Code 片段。

AgentToolGate 不会自动修改用户全局配置，也不会自动建立 Codex 项目信任或 Hook 内容信任。默认 hook mode 是 `dry-run`，完成两层信任后也不会立刻阻断。

推荐复制口径：

- Codex / ccswitch：把 `codex.config.snippet.toml` 中的 `<repo>` 替换为当前项目的规范化绝对路径，再按键合并到用户级配置；不要整段追加重复的 `[features]` 或 `[mcp_servers.agenttoolgate]` 表。MCP URL 指向 `http://127.0.0.1:<port>/mcp`。
- Codex hook：ATG 当前选择在项目 `.codex/config.toml` 中声明 Hook，并安装自包含脚本。Codex 本身也支持 `.codex/hooks.json`；同一配置层只选一种表示，避免两个来源同时执行。已有 `hooks.json` 时，`init codex` 会在写文件前停止；已有项目 TOML 时，ATG 保留原文件并生成 `codex.project-hook.snippet.toml` 供按键合并。
- Claude Code：复制 `claude.mcp.json` 或 `claude.settings.snippet.json`；Claude 默认使用 HTTP `/mcp`，workspace header 是 `X-Workspace-Org-Id`。

Codex 片段只对 `agenttoolgate` 这个 MCP server 设置
`default_tools_approval_mode = "approve"`，用于取消客户端重复确认；实际 allow、deny 或
approval 仍由 ATG 服务端决定。生成的 PreToolUse matcher 覆盖本地 Bash/Read/Write/Edit 等
动作，也会对非 ATG 的 `mcp__*` 调用做本地启发式风险判定；已经进入 ATG 的 MCP 调用会跳过，
避免递归治理。Hook 对第三方 MCP 的可见参数做启发式判断，不等于 Connector 同步、Policy、Approval
和 Audit 的完整治理链；需要完整治理时仍应通过 ATG MCP Outbound 接入。

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

### 5.1 启用项目级本地动作 Hook

下面的流程已按 Codex CLI `0.147.0` 的项目 Hook 格式核对。Codex 后续版本若调整 Hook 配置，以 `agenttoolgate.exe doctor --dir <project>`、Codex `/hooks` 和 [Codex Hooks 官方文档](https://learn.chatgpt.com/docs/hooks) 共同确认。

1. 确认系统中有 Git 与 Python 3.10+。Windows 优先使用 `python`，不可用时由初始化结果使用 `py -3`；Linux / macOS 使用 `python3`。Hook 从 `git rev-parse --show-toplevel` 定位脚本，因此从仓库子目录启动也能工作。
2. 在要保护的项目根目录运行 `agenttoolgate.exe init codex`。已有 `.codex/config.toml` 或 Hook 文件不会被覆盖；若显示“已跳过”，需要人工合并或核对自定义内容。若已有 `.codex/hooks.json`，普通初始化会在写入任何文件前停止；请先人工选择继续使用 JSON，或备份并移除它后迁移到 ATG 默认 TOML，不要让两种来源同层并存。继续使用 JSON 时，运行 `agenttoolgate.exe init codex --refresh-hooks --dir <project>` 只安装或刷新 adapter/Core。
3. 打开 `.agenttoolgate/clients/codex.config.snippet.toml`，把 `<repo>` 替换为 Codex 实际使用的规范化绝对路径，再按键合并到用户级 `config.toml`。Windows Codex `0.147.0` 的实际形式类似 `[projects.'e:\workspace\demo']`；TOML 单引号会原样保留反斜杠。保留已有设置，只新增或更新对应键，不要重复定义 TOML 表。默认位置是 `~/.codex/config.toml`，使用 ccswitch 时合并到它实际管理的用户配置。
4. 如果项目 `.codex/config.toml` 已存在，ATG 会保留原文件，并生成 `.agenttoolgate/clients/codex.project-hook.snippet.toml`。按键合并该片段后再次运行 `doctor --dir <project>`；不要为了省事覆盖现有项目配置。
5. 运行 `agenttoolgate.exe up --open`，再从该项目或任意仓库子目录启动 Codex。`init codex`、Hook control 和 recovery 写入会在目标仓库的本地 `info/exclude` 中按需追加 `/.tmp/agenttoolgate/`，保留已有规则且不修改项目 `.gitignore`，因此运行状态不会污染普通仓库或 linked worktree 的 `git status`。`up` 会在服务成功监听后写入该 repo-local control；当 `doctor` 确认 adapter 为 `current` 时，还会记录本次实际回环地址和当前 ATG 二进制位置，因此自定义端口不会回落到 8080，二进制也不要求预先加入 `PATH`。为兼容旧版严格解析的 adapter，`modified` adapter 只接收旧版 mode 字段。先审查自定义内容；确认使用当前发行版覆盖时运行 `agenttoolgate.exe init codex --refresh-hooks --dir <project>`，它只刷新 adapter/Core，不覆盖 `.codex/config.toml` 或用户级配置。旧运行文件会保留到 `.tmp/agenttoolgate/recovery/`，命令会打印恢复路径；确认新 Hook 稳定后再手工清理。刷新后必须重新运行 `up`，才能发布非默认 endpoint 和当前 executable。endpoint 仅接受回环 HTTP，executable 必须是现存绝对普通文件。
6. 在 Codex 中打开 `/hooks`，核对 Hook 来源、命令和当前 Hash，然后由用户显式信任。不要从文档复制固定 `trusted_hash`。`/hooks` 的 trust 绑定当前 Hook 定义及其 Hash；当它显示 changed、modified 或 untrusted 时重新审查。adapter/Core 内容是否与发行版一致，由 `doctor` 的 `current/modified` 独立检查。
7. 运行 `agenttoolgate.exe doctor --dir <project>`。项目配置会报告 `missing/unreadable/configured/custom`，adapter/Core 会报告 `missing/unreadable/current/modified`。这些状态不表示 Codex 运行时已信任、已启用或处于 `live`；最终以 `/hooks` 与 `hook control status` 为准。

默认模式是 `dry-run`。先检查 `.tmp/agenttoolgate/hook-dry-run.jsonl` 的脱敏预览，再按需切到真实拦截：

```powershell
agenttoolgate.exe hook control status --dir <project>
agenttoolgate.exe hook control live --dir <project> --reason "enable guarded session"
```

`hook control` 只切换当前运行时状态；输出中的 `nextUpMode` 表示下一次 `up` 会从
项目配置读取的模式。服务正常停止后，属于该进程的 control 会自动改为 `off`；若前一个
`up` 实例仍可达，则恢复到该实例的 control。服务异常退出时保留 `live` / `dry-run`
和 endpoint，使 Hook 进入离线保守路径并让 `doctor` 明确显示 unreachable。并发
更新不会被覆盖。

Codex Full Access 与 Hook 是两套独立机制。Full Access 模式本身不会禁用已加载的 Hook，但这不是充分安全条件。只有项目层已加载、Hook 已启用并信任、ATG 处于 `live`、调用进入受支持的 `PreToolUse` 路径，且 Hook 成功返回有效 `deny` 或退出码 `2` 时，ATG 才会阻断动作。Hook 被禁用或绕过、处于 `off` / `dry-run`，以及未覆盖的工具路径，均不受 ATG 实时阻断。`live` 下无法解析的 Hook 输入会保守拒绝；`off` / `dry-run` 对异常输入保持 no-op。

Codex 当前没有完整的 ask / confirm 体验。需要确认的本地动作会保守 `deny`，在 ATG UI 批准后由客户端精确重试。ATG 仍是 guardrail，不是 OS sandbox。

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
| Codex `/hooks` 看不到项目 Hook | 确认从目标项目启动 Codex、用户级配置已信任该项目、项目 `.codex/config.toml` 存在且 `[features] hooks = true`。 |
| `/hooks` 显示 untrusted / changed | 核对 Hook 定义和当前 Hash；确认来源可信后由用户重新信任，不写死旧 `trusted_hash`。若 `doctor` 单独显示 adapter/Core `modified`，先核对差异；确认覆盖时运行 `init codex --refresh-hooks`，重新运行 `up` 后再审查 Hook trust。 |
| `doctor` 提示 config.toml 与 hooks.json 同层并存 | Codex 会合并两个来源。人工保留一种，不要同时启用；`init codex` 对新检测到的 `hooks.json` 会在写入前停止。 |
| Hook 显示 failed，但工具仍执行 | 检查 Git、Python 3.10+ 命令是否存在、`.codex/hooks/` 是否完整、`doctor --dir <project>` 是否显示 `current`。Windows 可使用 `python` 或 `py -3`。`live` 下 ATG 能识别的异常输入会保守拒绝；宿主完全绕过 Hook 时仍不受保护。 |
| `doctor` 显示 endpoint unreachable | 确认项目级 `up` 是否异常退出、端口是否被其他进程占用。正常停止会回到 `off`；异常退出会保留当前模式，使 Hook 继续按离线保守策略处理。 |
| 端口被占用 | 用 `--port 8090` 或 `--addr 127.0.0.1:8090` 重新启动，并同步修改 MCP URL。 |

## 9. 安全边界

- 不把 token、Secret、DSN 密码写入 `.mcp.json`、示例文件、prompt 或截图。
- `X-Workspace-Org-Id` 只是 workspace 选择，不是 secret。
- Full Access 只有在 Hook 已加载、已启用并信任、处于 `live`、调用命中受支持的 `PreToolUse` 路径且 Hook 成功返回阻断结果时，才有 ATG 本地动作保护；失败、绕过、禁用或未覆盖的调用不受这条链路保护。
- AgentToolGate 是本地工具治理网关，不是 OS 级强制沙箱；真实高风险动作仍建议叠加系统权限、网络策略和最小权限凭据。

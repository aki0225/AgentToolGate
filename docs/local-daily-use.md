# AgentToolGate 本地日常使用指南

> 目标：让个人在 Windows / Linux amd64 上下载或构建一个二进制后，就能清楚知道怎么启动、数据在哪里、如何自检，以及下一步如何接入 Codex / Claude Code。

## 适合谁

- 想在本机长期保存 AgentToolGate 状态，但不想每次都起 PostgreSQL。
- 想把 AgentToolGate 当成本地 Agent 工具治理网关 / 防火墙来用。
- 想在 Codex / Claude Code 调用真实工具前，先走 policy / approval / audit。

## 从 Release 下载即跑

当前 Release 只发布两个包：`agenttoolgate-windows-amd64.zip` 和 `agenttoolgate-linux-amd64.tar.gz`。Linux 支持口径是 amd64 glibc-based distributions，并在 GitHub Actions Ubuntu 上验证；暂不支持 macOS、Linux arm64、Alpine/musl、安装器、systemd service、托盘、自启动或自动更新。

### Windows amd64

1. 打开 GitHub Release 页面，下载 `agenttoolgate-windows-amd64.zip` 和 `SHA256SUMS`。
2. 校验并解压：

```powershell
Get-FileHash .\agenttoolgate-windows-amd64.zip -Algorithm SHA256
Get-Content .\SHA256SUMS
Expand-Archive .\agenttoolgate-windows-amd64.zip -DestinationPath .\agenttoolgate-windows-amd64 -Force
cd .\agenttoolgate-windows-amd64
```

3. 运行：

```powershell
.\agenttoolgate.exe doctor
.\agenttoolgate.exe --open
```

### Linux amd64

1. 打开 GitHub Release 页面，下载 `agenttoolgate-linux-amd64.tar.gz` 和 `SHA256SUMS`。
2. 校验并解压：

```bash
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf agenttoolgate-linux-amd64.tar.gz
chmod +x ./agenttoolgate
```

3. 运行：

```bash
./agenttoolgate doctor
./agenttoolgate --open
```

首次启动摘要会显示访问地址、状态库、SQLite 路径、数据目录、认证模式、工作区、是否嵌入前端和文档入口。摘要和 doctor 只显示安全状态，不输出 token、Secret 明文或 DSN 密码。

## 项目级初始化与日常启动

下载或构建好二进制后，推荐在你想保护的项目根目录执行一次：

```powershell
# 只用 Codex
agenttoolgate.exe init codex

# 只用 Claude Code
agenttoolgate.exe init claude

# 同时使用 Codex 和 Claude Code
agenttoolgate.exe init all
agenttoolgate.exe up --open
```

Linux 下命令名是不带 `.exe` 的 `agenttoolgate`：

```bash
# 只用 Codex
./agenttoolgate init codex

# 只用 Claude Code
./agenttoolgate init claude

# 同时使用 Codex 和 Claude Code
./agenttoolgate init all
./agenttoolgate up --open
```

后续排错用 `agenttoolgate.exe doctor` 或 `./agenttoolgate doctor`。`doctor` 只显示 configured / missing 和本地 URL，不打印 token、Secret 明文或 DSN 密码。

`init codex` / `init claude` 会只生成对应客户端片段；`init all` 会同时生成：

- `.agenttoolgate/config.json`：host、port、workspace、hook mode 等本项目偏好；
- `.agenttoolgate/protected.json`：项目级保护策略占位和未来 Guard Core 扩展点；
- `.agenttoolgate/clients/`：Codex / Claude Code 可复制配置片段，其中 Codex TOML 带 `[mcp_servers.agenttoolgate]`，JSON snippets 不带无关 `note` 字段；
- `AGENTTOOLGATE.md`：给 AI 客户端和人类读者看的项目安全说明。

如果只使用某一个客户端：

```powershell
agenttoolgate.exe init codex
agenttoolgate.exe init claude
```

也可以从任意目录初始化或启动指定项目：

```powershell
agenttoolgate.exe init codex --dir <project>
agenttoolgate.exe init claude --dir <project>
agenttoolgate.exe init all --dir <project>
agenttoolgate.exe up --dir <project> --open
agenttoolgate.exe up --dir <project> --port 8090
```

`init` 默认不覆盖已有文件，重复执行会跳过用户已修改的文件。`up` 会读取 `.agenttoolgate/config.json`，写入 repo-local `.tmp/agenttoolgate/hook-control.json`，默认 hook mode 是 `dry-run`。这一步不会修改用户全局 Codex / Claude Code 配置、系统策略或注册表。

Codex / Claude Code / ccswitch 用户只需要复制 `.agenttoolgate/clients/` 下的片段；Codex TOML 已包含 `[mcp_servers.agenttoolgate]`，Claude 示例默认使用 HTTP `/mcp` 和 `X-Workspace-Org-Id`。

如果 `up` 找不到 `.agenttoolgate/config.json`，会提示先运行 `init`，并用默认本地配置启动。

## 从源码构建本地单二进制

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-local.ps1
.\dist\agenttoolgate.exe --open
```

默认行为：

- `DATABASE_URL` 为空时使用 SQLite。
- `STORE_DRIVER=memory` 仍可保留给测试或临时演示。
- `STORE_DRIVER=postgres` 仍可继续用于团队、CI 和 Postgres 集成测试。
- 后端监听默认只绑定 `127.0.0.1:8080`。
- `--open` 会在服务启动后打开默认浏览器。

## 端口和地址

默认 URL：`http://127.0.0.1:8080`。

临时换端口：

```powershell
.\agenttoolgate.exe --port 8090
```

临时指定完整监听地址：

```powershell
.\agenttoolgate.exe --addr 127.0.0.1:8090
```

环境变量方式：

```powershell
$env:PORT = "8090"
$env:HOST = "127.0.0.1"
.\agenttoolgate.exe
```

如果端口被占用，进程会提示“无法监听”以及如何使用 `--port` / `PORT` 换端口，不会输出 panic 堆栈。

## SQLite 状态文件位置

优先级：

1. `AGT_SQLITE_PATH`
2. `SQLITE_PATH`
3. `AGT_DATA_DIR\agenttoolgate.db`
4. Windows 默认：`%APPDATA%\AgentToolGate\agenttoolgate.db`

这里的 SQLite 只保存 AgentToolGate 自身状态，不替代 `database.query` 的外部业务库。

## 备份与重置

### 备份

停止进程后复制 SQLite 文件即可，例如：

```powershell
Copy-Item "$env:APPDATA\AgentToolGate\agenttoolgate.db" ".\backup\agenttoolgate.db"
```

### 重置

停止进程后删除 SQLite 文件，再重新启动即可。

```powershell
Remove-Item "$env:APPDATA\AgentToolGate\agenttoolgate.db"
```

如果你自定义了 `AGT_SQLITE_PATH` 或 `AGT_DATA_DIR`，请备份或删除对应路径下的 `agenttoolgate.db`。

## 本地 doctor 自检

```powershell
.\agenttoolgate.exe doctor
```

doctor 会显示：

- 版本 / 提交 / 构建时间（未注入时为 `unknown`）
- 访问地址 / 监听地址
- 状态库、SQLite 路径、数据目录
- 认证模式、工作区
- 嵌入式前端是否可用
- `database.query` DSN 是否 configured / missing
- GitHub token 是否 configured / missing
- HTTP allowed hosts / methods 摘要
- MCP Streamable HTTP URL、MCP SSE URL、workspace header 示例和 AI client 文档路径
- 默认 connector 与 MCP outbound 安全摘要

不会显示：

- Secret 解析后的值
- GitHub token 明文
- DSN 密码
- HTTP / MCP 上游敏感 header

## Secret env valueRef 配置

Secret 管理只保存 env 名，不保存真实密钥值。示例：

```powershell
$env:ATG_DEMO_GITHUB_TOKEN = "<真实 token 只放在本机进程环境>"
.\agenttoolgate.exe
```

在 Secrets 页面创建 Secret：

- `name`: `github-demo-token`
- `secretType`: `token`
- `valueSource`: `env`
- `valueRef`: `ATG_DEMO_GITHUB_TOKEN`

Connector 只绑定 `github-demo-token` 这样的 Secret 名称。运行时由后端读取 `ATG_DEMO_GITHUB_TOKEN`，审计、日志、前端响应都不展示解析后的值。

## 切回 PostgreSQL

如果你需要继续使用本地 PostgreSQL 模式：

```text
STORE_DRIVER=postgres
DATABASE_URL=postgres://agenttoolgate:agenttoolgate@127.0.0.1:5432/agenttoolgate?sslmode=disable
```

`DATABASE_URL` 只影响 AgentToolGate 自身状态库；`database.query` 仍建议单独配置 `DATABASE_QUERY_URL` 和 `DATABASE_QUERY_ALLOWED_TABLES`。

## 接入 Codex / Claude Code

当前本地版已经能作为 Agent 工具调用防火墙运行：Policy、Approval、Secret、Audit 都在本地闭环里。Codex / Claude Code 可先通过 MCP Inbound 接入：

- 详细步骤：[`docs/ai-client-integration.md`](ai-client-integration.md)
- Claude Code 示例：[`examples/ai-clients/claude-code.mcp.json`](../examples/ai-clients/claude-code.mcp.json)
- Codex 命令示例：[`examples/ai-clients/codex-mcp-commands.md`](../examples/ai-clients/codex-mcp-commands.md)
- 人工 smoke prompt：[`examples/ai-clients/smoke-prompts.md`](../examples/ai-clients/smoke-prompts.md)

最小接入心智模型：

- Codex 默认使用 Streamable HTTP endpoint：`http://127.0.0.1:8080/mcp`。
- Claude Code 默认使用 Streamable HTTP endpoint：`http://127.0.0.1:8080/mcp`。
- 旧客户端需要时才使用 SSE fallback：`http://127.0.0.1:8080/mcp/sse`。
- local mode 带 `X-Workspace-Org-Id: local-org`。
- 写操作命中 `approval_required` 时，到本地 Console 审批，再重试或看 Audit Logs。
- AgentToolGate 是工具治理网关，不是操作系统级强制沙箱；真实危险动作仍要配合最小权限和系统级隔离。

## 日常检查

- 打开 `/health` 看 store driver。
- 打开 `/tools`、`/policies`、`/secrets`、`/connectors`、`/audit` 这些前端路由确认 SPA fallback 正常。
- 如果要看外部业务库，单独配置 `DATABASE_QUERY_URL` 和 `DATABASE_QUERY_ALLOWED_TABLES`。
- 如果你想先离线评估一个本地动作，可执行：

```powershell
.\agenttoolgate.exe guard evaluate --input action.json
```

这个命令只做本地分类，不会启动 HTTP server，也不会自动接 Claude Code / Codex hook。

如果你想先验证 Claude Code / Codex 类 hook payload 会被怎样分类，可使用 Hook Adapter Dry-Run：

```powershell
.\agenttoolgate.exe guard adapt claude --input examples\guard-hooks\claude\bash-git-status.json
.\agenttoolgate.exe guard adapt codex --input examples\guard-hooks\codex\bash-rm-root.json --mode dry-run
```

- 默认 `--mode dry-run`，只输出 `wouldBlock` / `wouldAsk` / `decision` 等 JSON，不真正阻断。
- `--mode enforce` 第一版也只输出可供未来 hook 使用的决策 JSON，不自动安装、不修改 `.claude/` / `.codex/`、不写用户全局配置或系统策略。
- Adapter 不输出原始 payload、完整文件内容或 Secret 明文；fixtures 位于 `examples/guard-hooks/`。

项目级真实 PreToolUse hook 有一个 repo-local 热开关，当前会话不需要重启即可切换：

```powershell
.\agenttoolgate.exe hook control status
.\agenttoolgate.exe hook control off --reason "pause ATG hooks"
.\agenttoolgate.exe hook control dry-run --reason "preview only"
.\agenttoolgate.exe hook control live --reason "enable guarded session"
```

控制文件写在 `.tmp/agenttoolgate/hook-control.json`。缺失、损坏或无法解析时按 `off` 处理：hook 直接 no-op，不调用后端、不写 pending audit、不输出 hook JSON。`dry-run` 只写 `.tmp/agenttoolgate/hook-dry-run.jsonl` 的脱敏预览，不阻断；`live` 才执行真实拦截。`TRELLIS_HOOKS=0` 和 `TRELLIS_DISABLE_HOOKS=1` 仍是最高优先级硬关闭。

如果你已经确认 dry-run 结果，可以手动把 Claude Code PreToolUse hook 指向真实 Hook 输出入口：

```powershell
.\agenttoolgate.exe guard hook claude --input examples\guard-hooks\claude\bash-read-ssh.json
```

手动接入时，命令通常使用 stdin：

```powershell
agenttoolgate.exe guard hook claude --input -
```

示例配置只放在仓库内供参考：`examples/guard-hooks/claude/settings.example.json`。AgentToolGate 不会自动安装 hook、不会修改 `.claude/`、不会修改用户全局配置或系统策略。

Codex 项目级 hook 可以桥接到 Go Guard Core：

```powershell
.\agenttoolgate.exe guard hook codex --input examples\guard-hooks\codex\bash-rm-root.json
```

运行时对 `allow` 采用 no-op / 直接放行，不显式回传 `permissionDecision=allow`；`guard adapt codex` 仍保留 dry-run 的 allow / deny / ask 诊断语义。

手动接入时，项目级 Python hook 会优先调用：

```powershell
agenttoolgate.exe guard hook codex --input -
```

如果需要覆盖二进制路径，可设置 `AGENTTOOLGATE_EXE`。Codex Hook Bridge 是手动 opt-in guardrail，不是 OS sandbox / OS enforcement boundary；不会自动修改用户级 `~/.codex/config.toml`。MVP 暂不做 approval ticket / `deny_with_ticket` / retry / remembered allow，Guard `ask` 会保守映射为 `deny`。

## 备忘

- `http.request`、GitHub、MCP 的 secret 注入仍然遵守 fail closed。
- SQLite 不是生产 KMS，也不是外部 datasource。
- 想看完整演示流程，请先读 `docs/demo-playbook.md`。

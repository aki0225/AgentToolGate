# AgentToolGate 本地日常使用指南

> 目标：让个人在 Windows / Linux amd64 上下载或构建一个二进制后，就能清楚知道怎么启动、数据在哪里、如何自检，以及下一步如何接入 Codex / Claude Code。

> [!IMPORTANT]
> 本文所述新版 `init codex`、配套 `doctor` 检查、项目 TOML 和自包含 Hook 要求 AgentToolGate `v0.3.1+`。当前 `v0.3.0` 不包含这些命令语义；请从当前 `main` 构建，或继续按 `v0.3.0` 随附的旧接入说明操作。

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

3. 若只使用 MCP、无需项目 Hook，运行普通 serve：

```powershell
.\agenttoolgate.exe doctor --dir <project>
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

3. 若只使用 MCP、无需项目 Hook，运行普通 serve：

```bash
./agenttoolgate doctor
./agenttoolgate --open
```

首次启动摘要会显示访问地址、状态库、SQLite 路径、数据目录、认证模式、工作区、是否嵌入前端和文档入口。摘要和 doctor 只显示安全状态，不输出 token、Secret 明文或 DSN 密码。

## 项目级初始化与日常启动

下载或构建好二进制后，推荐在你想保护的项目根目录执行一次：

如果普通 serve 已通过 `agenttoolgate.exe --open`、`./agenttoolgate --open` 等方式运行，先在原终端按 `Ctrl+C` 停止；项目 Hook 流程必须先 `init`，再只用 `up` 启动，避免两个进程争用默认 `8080` 端口。

`init codex` 和 `init all` 要求目标目录本身就是 Git 仓库根目录，不能指向普通目录或外层仓库中的任意子目录。

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

Linux 下命令名是不带 `.exe` 的 `agenttoolgate`：

```bash
# 从下面三种 init 中任选一种
./agenttoolgate init codex
# 或
./agenttoolgate init claude
# 或
./agenttoolgate init all

# init 完成后只用 up 启动
./agenttoolgate up --open
```

后续排错用 `agenttoolgate.exe doctor --dir <project>` 或 `./agenttoolgate doctor --dir <project>`。`doctor` 显示本地 URL，并报告 Codex 项目配置的 `missing/unreadable/configured/custom`、`hooks.json` 是否存在、adapter/Core 的 `missing/unreadable/current/modified`、Git/Python 命令可用性与 hook mode；它不代表运行时 trust / live 状态，也不打印 token、Secret 明文或 DSN 密码。

`init codex` / `init claude` 只生成对应客户端所需文件；`init all` 会同时生成：

- `.agenttoolgate/config.json`：host、port、workspace、hook mode 等本项目偏好；
- `.agenttoolgate/protected.json`：项目级受保护路径与网络写入规则；
- `.codex/config.toml` 与 `.codex/hooks/`：Codex 实际读取的项目 Hook 配置、自包含 Python adapter 和离线 Guard Core；
- `.agenttoolgate/clients/`：Codex 用户级配置、已有 `.codex/config.toml` 的 Hook 合并片段和 Claude Code 可复制片段；
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

`init` 默认不覆盖已有文件，重复执行会跳过用户已修改的文件。项目已有 `.codex/hooks.json` 时，普通 `init codex` / `init all` 会在写入前停止，避免 JSON 与 TOML Hook 同层重复执行；请先人工保留一种来源。继续使用 JSON，或升级后 `doctor` 显示 adapter/Core 为 `modified` 时，先审查差异，再用 `agenttoolgate.exe init codex --refresh-hooks --dir <project>` 只安装或刷新 adapter/Core。ATG 会在目标 Git 仓库的本地 `info/exclude` 中按需追加 `/.tmp/agenttoolgate/`，保留用户已有规则且不改项目 `.gitignore`；普通仓库和 linked worktree 都不会把 control、SQLite 或 recovery 暴露到 `git status`。旧运行文件会保留到 `.tmp/agenttoolgate/recovery/` 并打印路径；确认新 Hook 稳定后再手工清理。刷新后重新运行 `up`，才能把非默认 endpoint 和当前 executable 写回 control。`up` 会读取 `.agenttoolgate/config.json`，服务启动成功后写入 repo-local `.tmp/agenttoolgate/hook-control.json`，默认 hook mode 是 `dry-run`。这一步不会修改用户全局 Codex / Claude Code 配置、系统策略或注册表。

`hook control` 当前不接受 `--dir`；从任意目录执行 `up --dir <project>` 后，切换 `off` / `dry-run` / `live` 前仍需进入目标项目或其子目录，避免作用到另一个仓库。

Codex 还需要两次显式确认：先把 `.agenttoolgate/clients/codex.config.snippet.toml` 按键合并到用户级 `config.toml`，再从项目启动 Codex，在 `/hooks` 中核对命令和当前 Hash 后信任 Hook 内容。不要重复追加已有的 `[features]` 或 MCP 表。Windows 的 trust key 使用 Codex 规范化后的小写、反斜杠绝对路径；生成片段用 TOML 单引号保留反斜杠。已有 `.codex/config.toml` 时按键合并 `codex.project-hook.snippet.toml`。`doctor --dir <project>` 能检查安装状态，但不能替代 `/hooks` 的运行时信任状态。项目 Hook 需要 Git 与 Python 3。

## 配置项目内保护规则

`.agenttoolgate/protected.json` 默认不包含路径规则，也不会改变普通项目行为。需要保护核心算法或生产配置时，可以添加 repo-relative 的精确路径或 `/**` 子树：

```json
{
  "version": 1,
  "projectRoot": "<repo>",
  "localActionFirewall": {
    "enabled": true,
    "defaultMode": "dry-run",
    "protectedPaths": [
      {
        "pattern": "src/core/**",
        "read": "require_approval",
        "write": "require_approval",
        "delete": "deny",
        "reason": "核心算法目录"
      },
      {
        "pattern": "deploy/production/**",
        "read": "require_approval",
        "write": "deny",
        "delete": "deny",
        "reason": "生产配置目录"
      }
    ],
    "egress": {
      "enabled": true,
      "allowedHosts": ["api.github.com", "*.example.test"],
      "unlistedWrite": "deny"
    },
    "notes": []
  }
}
```

规则约束：

- `pattern` 只接受仓库相对的精确路径或以 `/**` 结尾的子树，不接受绝对路径、`..`、任意 glob 或正则。
- read / write / delete / exec 只支持 `require_approval` 和 `deny`。项目规则不能写 `allow`，因此不能放松内置 Guard Core。
- 受保护路径命中后按 high risk 处理，不进入低/中风险 remembered allow；需要审批的动作每次都使用一次性 ticket。
- `apply_patch` 会检查补丁中的每个目标：Add / Update 和 Move 目标使用 write 规则，Move 源路径和 Delete 使用 delete 规则；任一目标命中即对整个补丁采用最严格结果。
- `egress.allowedHosts` 支持具体 host、`host:port` 或 `*.domain`。allowlist 不会把内置网络写入审批改成静默 allow；`unlistedWrite` 只会继续提升为审批或拒绝。
- 文件里的 `projectRoot` 只是脱敏说明字段。可信项目根来自 `agenttoolgate up --dir ...` 或后端 `AGT_PROJECT_ROOT`，客户端 payload 不能覆盖。

`dry-run` / `live` 启动前会严格校验该文件；无效 JSON、未知字段、绝对路径、遍历或非法 wildcard 会拒绝启动，不会写入 live control。运行中修改规则会在下一次 Hook/API 评估时生效；若改成无效配置，live 请求会 fail closed。`off` 仍然是完全 no-op，不读取规则、不干扰当前开发会话。

`.agenttoolgate/config.json` 和 `.agenttoolgate/protected.json` 自身属于 self-tamper。日常调整规则时先运行 `agenttoolgate.exe hook control dry-run`，编辑完成后再运行 `agenttoolgate.exe hook control live`；两个启用命令都会先校验配置，`off` 始终可用于恢复开发。

这不是数据血缘或 DLP：ATG 能看到某次“读取受保护路径”或“向某个 URL 写入”，但不能追踪先读后改名、编码、压缩、复制到模型上下文再外发的来源，也不能覆盖绕过 Hook 的 socket、子进程或第三方工具。

Codex 用户运行 `init codex` 安装项目 Hook，再把用户级 TOML 与必要的项目 Hook 片段按键合并；Claude Code / ccswitch 用户使用 `.agenttoolgate/clients/` 下对应片段。Claude 示例默认使用 HTTP `/mcp` 和 `X-Workspace-Org-Id`。

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
- Codex 项目配置、adapter/Core、Git/Python 3 和 repo-local hook mode

`doctor` 的文件状态不等于 Codex 已启用或信任 Hook，也不等于控制模式已经进入 `live`；运行时仍以 Codex `/hooks` 和 `hook control status` 为准。

不会显示：

- Secret 解析后的值
- GitHub token 明文
- DSN 密码
- HTTP / MCP 上游敏感 header

HTTP Connector 的 `allowedHosts` / `allowedMethods` 只能在后端环境配置的允许范围内继续缩小，不能通过 Console 扩大部署级上限。GitHub Connector 的 `apiBaseURL` 必须与后端环境配置一致，`allowedRepos` 也只能继续缩小。

MCP Connector 使用 `headerSecretRefs` 时必须同时配置部署级 `MCP_ALLOWED_HOSTS`，例如：

```powershell
$env:MCP_ALLOWED_HOSTS = "127.0.0.1:8081"
```

Connector URL 必须命中该上限，且只能继续缩小范围。旧记录缺少对应 workspace Secret、后端 runtime env 或 allowlist 时同样 fail closed。MCP 重定向只允许协议、主机和有效端口均不变的同源跳转。

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
- 本地模式默认请求者和审批者是同一身份。需要职责分离时，在后端设置至少 24 个字符的 `AGT_LOCAL_REVIEWER_TOKEN`，审批时只在 Console 的“本地独立审批令牌”输入框临时填写；前端不会持久化该值。
- 审批列表和批准/拒绝响应只返回脱敏摘要及稳定错误，不返回冻结执行参数、原始 Secret、URL 私密部分或底层 Connector 错误。批准后的二次重验证会缩小 TOCTOU 窗口，但不构成 Store 与外部 Connector 的跨系统原子事务。
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
- `--mode enforce` 只输出供 Hook adapter 或诊断使用的决策 JSON；该命令本身不安装 Hook、不修改 `.claude/` / `.codex/`、不写用户全局配置或系统策略。
- Adapter 不输出原始 payload、完整文件内容或 Secret 明文；fixtures 位于 `examples/guard-hooks/`。

项目级真实 PreToolUse hook 有一个 repo-local 热开关，当前会话不需要重启即可切换：

```powershell
.\agenttoolgate.exe hook control status
.\agenttoolgate.exe hook control off --reason "pause ATG hooks"
.\agenttoolgate.exe hook control dry-run --reason "preview only"
.\agenttoolgate.exe hook control live --reason "enable guarded session"
```

控制文件写在 `.tmp/agenttoolgate/hook-control.json`。项目级 `up` 成功且 `doctor` 确认 Codex adapter 为 `current` 时，还会记录实际回环 endpoint 和当前 ATG executable，使自定义端口与未加入 `PATH` 的本地二进制都能被 Hook 正确使用；endpoint 只接受回环 HTTP，executable 必须是现存绝对普通文件。为兼容旧版严格解析的 adapter，`modified` adapter 只写旧版 mode 字段，更新前不会收到扩展 runtime 字段；确认需要覆盖时使用 `init codex --refresh-hooks`，随后重新运行 `up`。该文件位于忽略提交的 `.tmp`，公开 evidence 仍应脱敏本机路径。文件缺失时按 `off` 处理；文件已存在但损坏、不可读、字段非法或无法解析时 fail closed：Hook 返回 `deny`，`status` 报错，可显式执行 `hook control off` 重写后恢复。`off` 会移除 control 中不再需要的 endpoint 和 executable，避免旧临时二进制路径反过来造成无效控制文件。`dry-run` 只写 `.tmp/agenttoolgate/hook-dry-run.jsonl` 的脱敏预览，不阻断；`live` 才执行真实拦截。`TRELLIS_HOOKS=0` 和 `TRELLIS_DISABLE_HOOKS=1` 仍是最高优先级硬关闭。

`live` 请求后端的默认超时是 1000ms，Python Bridge 等待 Go CLI 的默认超时是 1500ms；如本机性能不同，可分别用 `AGENTTOOLGATE_HOOK_TIMEOUT_MS`（50–2000ms）和 `AGENTTOOLGATE_CLI_TIMEOUT_MS`（100–5000ms）覆盖。该设置不影响 `off` / `dry-run`。

如果你已经确认 dry-run 结果，可以手动把 Claude Code PreToolUse hook 指向真实 Hook 输出入口：

```powershell
.\agenttoolgate.exe guard hook claude --input examples\guard-hooks\claude\bash-read-ssh.json
```

手动接入时，命令通常使用 stdin：

```powershell
agenttoolgate.exe guard hook claude --input -
```

示例配置只放在仓库内供参考：`examples/guard-hooks/claude/settings.example.json`。AgentToolGate 不会自动安装 hook、不会修改 `.claude/`、不会修改用户全局配置或系统策略。

`init codex` 安装的项目级 Hook 会优先调用 Go Guard Core；下面的命令只用于单独诊断输出：

```powershell
.\agenttoolgate.exe guard hook codex --input examples\guard-hooks\codex\bash-rm-root.json
```

运行时对 `allow` 采用 no-op / 直接放行，不显式回传 `permissionDecision=allow`；`guard adapt codex` 仍保留 dry-run 的 allow / deny / ask 诊断语义。

项目级 Python hook 会优先调用：

```powershell
agenttoolgate.exe guard hook codex --input -
```

如果需要覆盖二进制路径，可设置 `AGENTTOOLGATE_EXE`。安装项目文件不等于启用保护。Full Access 模式本身不会禁用已加载的 Hook，但只有项目层已加载、Hook 已启用并信任、控制模式为 `live`、调用进入受支持的 `PreToolUse` 路径，且 Hook 成功返回有效 `deny` 或退出码 `2` 时才会实时阻断。Hook 失败、输出无效、绕过、未覆盖工具以及 `off` / `dry-run` 都不受实时阻断。Codex Hook Bridge 是 opt-in guardrail，不是 OS sandbox / OS enforcement boundary；后端已支持一次性 `deny_with_ticket`、批准后精确重试和低/中风险 remembered allow；Codex 运行时仍没有完整 ask 交互，需要确认的动作会保守输出 deny，批准后由客户端重试。

## 备忘

- `http.request`、GitHub、MCP 的 secret 注入仍然遵守 fail closed。
- SQLite 不是生产 KMS，也不是外部 datasource。
- 想看完整演示流程，请先读 `docs/demo-playbook.md`。

<div align="center">

<img src="docs/assets/atg-hero.jpg" width="100%" alt="AgentToolGate：工具调用在真正执行之前，先过治理闸门">

# AgentToolGate

<p>
  <a href="https://github.com/aki0225/AgentToolGate/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/aki0225/AgentToolGate/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.26+">
  <img src="https://img.shields.io/badge/Platforms-Windows%20%7C%20Linux-8B5CF6?style=for-the-badge" alt="Windows / Linux">
  <a href="https://aki0225.github.io/AgentToolGate/"><img src="https://img.shields.io/badge/在线展示-v0.3.0-5EEAD4?style=for-the-badge" alt="AgentToolGate 在线展示"></a>
  <a href="https://github.com/aki0225/AgentToolGate/releases"><img src="https://img.shields.io/badge/Release-amd64%20%2B%20SHA256-22C55E?style=for-the-badge" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-F8FAFC?style=for-the-badge" alt="MIT License"></a>
</p>

**[架构总览](#架构总览)** ·
**[快速开始](#快速开始)** ·
**[在线展示](https://aki0225.github.io/AgentToolGate/)** ·
**[实测评估](#实测评估)** ·
**[防护范围](#防护范围)** ·
**[非目标](#非目标)** ·
**[已知限制](#已知限制)** ·
**[深入文档](#深入文档)** ·
**[支持工具](#支持工具)**

</div>

AgentToolGate（下面简称 ATG）是一个跑在本地的 AI Agent 工具调用治理网关。它不做“防注入”，只管一件事——数据库、GitHub、HTTP、外部 MCP 和本地高危动作在真正执行之前，先过 policy、审批、ATG 管理的 Connector Secret 注入和审计这一道。

> [!IMPORTANT]
> ATG 是 guardrail，不是操作系统沙箱，也不是 EDR 或企业 DLP。真要跑高风险场景，最小权限账户、系统沙箱、网络策略和上游服务自己的权限边界仍然缺一不可，ATG 替代不了它们。

## 架构总览

```mermaid
flowchart TD
    Agent[AI Agent / MCP Client / Local Hook] --> Entry{Entry}
    Entry -->|REST| REST[POST /api/tool-calls]
    Entry -->|MCP Inbound| MCPIn[/mcp and /mcp/sse/]
    Entry -->|Local Action| Guard[POST /api/agent-guard/evaluate]
    MCPIn --> ToolCall[createToolCall]
    REST --> ToolCall
    ToolCall --> Policy[Policy Engine]
    Policy --> Decision{allow / deny / require_approval}
    Decision -->|allow| Runtime[Connector Runtime]
    Decision -->|require_approval| Approval[Approval Queue]
    Decision -->|deny| Audit[Audit Logs]
    Approval -->|approve| Runtime
    Approval -->|reject| Audit
    Runtime --> DB[database.query]
    Runtime --> GitHub[github.*]
    Runtime --> HTTP[http.request]
    Runtime --> MCPOut["mcp_&lt;connector&gt;.&lt;tool&gt;"]
    Runtime --> Audit
    Guard --> Ticket[deny_with_ticket / one-time ticket]
    ToolCall --> Trace[OpenTelemetry trace]
    Audit --> Trace
```

图里几条主线：

- REST 主链路是 `POST /api/tool-calls -> createToolCall -> Policy / Approval / Audit / Connector Runtime / OTel`。
- MCP Inbound Streamable HTTP 使用 `/mcp`，SSE `/mcp/sse` 仅作 fallback。`tools/call` 走的也是 `createToolCall`，没有旁路。
- MCP Outbound 把外部 MCP Server 的工具同步成 `mcp_<connector>.<tool>`，进同一条治理链路。
- 本地动作防火墙走独立入口 `/api/agent-guard/evaluate`，对 Claude / Codex 的本地动作做风险分类、审计和 `deny_with_ticket` 闭环。

## 快速开始

从 [GitHub Release](https://github.com/aki0225/AgentToolGate/releases) 下载 Windows amd64 或 Linux amd64 包，解压后在要保护的项目根目录运行：

下面新版 `init codex`、配套 `doctor` 检查、项目 TOML 和自包含 Hook 要求 `v0.3.1+`。当前 `v0.3.0` 不包含这些命令语义；如果 Release 页面最新仍是 `v0.3.0`，请先从当前 `main` 构建，或继续按该版本随附的旧接入说明操作。

如果普通 serve 已通过 `agenttoolgate.exe --open` 等方式运行，先在原终端按 `Ctrl+C` 停止，再执行下面的项目初始化流程；否则 `up` 会与原进程争用默认 `8080` 端口。

```powershell
# Codex 用户
.\agenttoolgate.exe init codex

# Claude Code 用户
.\agenttoolgate.exe init claude

# 同时使用两个客户端
.\agenttoolgate.exe init all

# 从上面三种 init 中任选一种，然后只用 up 启动
.\agenttoolgate.exe up --open

# up 运行后，在另一个终端检查项目接入
.\agenttoolgate.exe doctor --dir .
```

Linux 用不带 `.exe` 的 `./agenttoolgate`，参数一样。`init codex` 和 `init all` 要求当前目录本身就是目标 Git 仓库根目录；不能在普通目录或外层仓库的任意子目录中安装。初始化会在项目中生成 `.codex/config.toml`、自包含的 `.codex/hooks/` 和 `.agenttoolgate/` 配置，但不会修改用户级 `~/.codex/config.toml`，也不会替你信任项目或 Hook。Codex 用户还需要：

1. 按键合并 `.agenttoolgate/clients/codex.config.snippet.toml` 到用户级 Codex 配置，不要重复追加已有的 `[features]` 或 `[mcp_servers.agenttoolgate]` 表。
2. 从该项目启动 Codex，在 `/hooks` 中核对 Hook 命令和当前 Hash，再显式信任。
3. 用 `agenttoolgate.exe doctor --dir <project>` 核对项目配置和 Hook 文件；`doctor` 不会替代 Codex 运行时的信任检查。

如果项目已有 `.codex/hooks.json`，普通 `init codex` 会在写入前停止，避免它和 `.codex/config.toml` 的 Hook 被同层重复加载；请先人工保留一种来源。继续使用 JSON 时可用 `agenttoolgate.exe init codex --refresh-hooks --dir <project>` 单独安装或更新 adapter/Core，不会创建项目 TOML。刷新会把旧运行文件保留到 Git 忽略的 `.tmp/agenttoolgate/recovery/` 并打印路径；确认新 Hook 稳定后再手工清理。随后重新运行 `up` 发布本次 endpoint 和二进制路径，再在 `/hooks` 中复核信任状态。

项目 Hook 需要 Git 与 Python 3。hook 默认 `dry-run`，不会一上来就真阻断。Full Access 模式本身不会禁用已加载的 Hook，但这不等于完整保护：只有 Hook 已启用并信任、ATG 处于 `live`、调用进入 Codex 支持的 `PreToolUse` 路径，且 Hook 成功返回有效 `deny` 或合法退出码 `2` 时，动作才会被实时阻断。Hook 失败、输出无效、被禁用或绕过、处于 `off` / `dry-run`，以及未覆盖的工具路径，都应视为没有 ATG 实时阻断。完整步骤见 [AI 客户端接入指南](docs/ai-client-integration.md#51-启用项目级本地动作-hook)。

AgentToolGate 不碰系统策略、注册表或 shell profile。Claude Code 的配置仍由用户显式接入。

`init` 同时生成 `.agenttoolgate/protected.json`。默认规则为空，不改变普通开发行为；你可以把核心算法、生产配置等 repo-relative 路径设置为“读取/修改需审批”或“直接拒绝”，也可以对 Hook 可见的网络写入增加项目级 host allowlist。规则只会收紧 Guard Core，不会把原本需要审批的动作改成静默放行。配置示例与边界见 [本地日常使用指南](docs/local-daily-use.md#配置项目内保护规则)。

也可以从源码构建单二进制（需要 Go 1.26+ 与 Node.js 20+）：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-local.ps1
.\dist\agenttoolgate.exe doctor
```

日常使用说明见 [docs/local-daily-use.md](docs/local-daily-use.md)，AI 客户端接入见 [docs/ai-client-integration.md](docs/ai-client-integration.md)。

## 生产部署前必读

仓库默认 `docker-compose.yml` 使用 `HOST=0.0.0.0`、`AUTH_MODE=local`、
`LOCAL_ROLE=owner` 和 `DEV_MODE=true`。这套配置只用于单机本地开发：
任何能够访问 backend 的调用方都会进入本地 owner 身份，不能直接作为多用户、
共享主机或网络暴露部署的鉴权方案。

如果要让其他机器或其他用户访问，至少需要切换到 OIDC、限制监听地址和网络入口，
并为上游凭据配置最小权限。不要把默认 Compose 配置直接暴露到公网。
否则请求等同于无鉴权访问。当前仅提供基础 role/workspace 隔离，不具备职责
分离或组织级访问控制。

<!-- agent-safety-proof:start -->
## 实测评估

基于 [GitHub Actions run 31465745397](https://github.com/aki0225/AgentToolGate/actions/runs/31465745397) 对
[`e809c66`](https://github.com/aki0225/AgentToolGate/commit/e809c66ea8e82a27ab25531072cc1ca813550384) 的 synthetic / disposable 评估：

- **Quick（Linux）**：20 passed / 0 failed / 0 skipped。
- **Windows full**：30 passed / 0 failed / 0 skipped。
- **Linux full**：26 passed / 0 failed / 4 skipped。

数字由 [公开评估快照](evaluation/published/agent-safety-proof.json) 的逐 case 状态计算；同一文件记录 Artifact 名称、
ID 与源文件 SHA256。它不是 OS sandbox 证明，也不替代真实 Codex / Claude Code 客户端验收。
<!-- agent-safety-proof:end -->

### 证据分层

- **正式发布验收**：[v0.3.0 发布验收](docs/v0.3.0-release-acceptance.md)记录正式附件
  SHA256、Windows / Linux 启动 smoke、MCP 调用和脱离源码仓库的评估附件复跑结果。
- **真实客户端验收**：[Codex CLI 与 Claude Code 验收](evaluation/client-acceptance/README.md)
  保存 MCP Audit、Hook 生命周期、文件系统后置条件和同步脱敏录屏。该证据来自历史源提交
  `0ee86ef`，用于证明真实客户端集成路径，不冒充 `v0.3.0` Release 二进制重跑。
- **可重复评估**：`v0.3.0` Release 同时提供 Windows / Linux 评估附件，可在
  disposable 目录复跑 quick 或完整 suite。

## 防护范围

两个入口：

- **工具治理网关**：`database.query`、`github.*`、`http.request`、`mcp_<connector>.<tool>` 在执行前过 workspace policy、审批、限流、ATG 管理的 Connector Secret 运行时注入、脱敏审计和 OTel trace。
- **本地动作防火墙**：Claude / Codex 要写 Startup、`.ssh`、`.env`、`.git/hooks` 或 ATG 自身的 hook/config，或者脚本里出现 `ExecutionPolicy Bypass`、`WindowStyle Hidden`、encoded payload 这类特征时，先进 guard 评估。
- **项目内保护规则**：`.agenttoolgate/protected.json` 可为核心目录配置 read / write / delete / exec 的 `require_approval` 或 `deny`，并对未列出的网络写入目标继续收紧。

拦的是这类后果：

- 写操作、高风险工具没经审批就打到 GitHub、HTTP、数据库或外部 MCP。
- Agent 直接拿到或回显上游 token、Authorization header、cookie、DSN 密码、MCP session。
- 被注入之后写持久化脚本、改 git hooks、摸凭据路径、破坏项目文件。
- 审批、拒绝、失败和执行结果没有留痕，出了事查不了。

## 非目标

- 提示词注入、幻觉、恶意上下文本身，ATG 不拦——它只管工具调用落地那一刻。
- 不做 OS 级 enforcement：Claude Code 侧可以保留 ask/confirm 心智，但它仍然只是 hook guardrail。
- 不做数据血缘或污点追踪：规则能约束“读取哪个路径”和“写到哪个 host”，不能证明先前读出的源码不会被改名、编码或通过绕过 Hook 的进程外传。

## 已知限制

- Codex hook bridge 没有完整的交互式 ask 体验，需要确认的动作目前按保守 `deny` / no-op 处理，不能当成完整的审批弹窗。
- ATG 管理的 Connector Secret 目前是 env-backed `valueRef`，不是 KMS、Vault 或云 Secret Manager。
- GitHub 集成适合 PAT / demo token，不是 GitHub App installation token 的生产闭环。
- HTTP 的 SSRF guard 还没覆盖 DNS rebinding、解析后私网网段判定和 redirect 后的 DNS 复检。
- 项目规则只覆盖 Hook 暴露的显式目标及当前可静态解析的已知命令、解释器和脚本后缀；动态命令、未知解释器或绕过 Hook 的进程不保证命中。
- 当前只有基础 role/workspace 隔离；职责分离、版本化迁移、备份、告警、SLO、灾备和组织级策略发布/回滚等生产化前提都还没有。

## 深入文档

- [架构说明](docs/architecture.md)：项目定位、REST/MCP/Local Action 主链路、核心模块、数据流与信任边界。
- [MCP 治理](docs/mcp-governance.md)：MCP Inbound Streamable HTTP `/mcp`、SSE fallback `/mcp/sse`、MCP Outbound `mcp_<connector>.<tool>`、ATG 管理的 Connector Secret 与 Connector、Approval 的关系。
- [本地动作防火墙](docs/local-action-firewall.md)：off / dry-run / live、`deny_with_ticket`、remembered allow、Claude / Codex 差异和 TOCTOU 风险。
- [威胁模型](docs/threat-model.md)：资产、攻击面、可信边界、关键攻击路径、已有缓解和未覆盖项。
- [演示剧本](docs/demo-playbook.md)：产品化演示路径。
- [安全评审说明](docs/security-review-notes.md)：安全评审视角的控制与剩余风险。
- [Daily Use Acceptance](docs/daily-use-acceptance.md)：日常开发低噪音验收证据。
- [v0.3.0 发布验收](docs/v0.3.0-release-acceptance.md)：项目保护规则、跨平台评估附件和正式 Release 的可追溯验收记录。
- [Agent Guard Synthetic Demo](examples/agent-demo/windows-startup-poisoning.md)：Windows Startup poisoning synthetic demo。
- [ATG 管理的 Connector Secret 外传 Synthetic Demo](examples/agent-demo/secret-exfiltration-blocked.md)：本机验证这类 Secret 不直接暴露给 Agent，并对危险 header 与响应做脱敏。
- [GitHub 写审批 Synthetic Demo](examples/agent-demo/github-write-approval.md)：本机验证审批前不触达上游和独立 reviewer 放行。

## 支持工具

| Tool Registry 工具族 | 当前治理行为 |
| --- | --- |
| `mock.echo` | 最小成功闭环，写 audit |
| `database.query` | SELECT-only、表白名单、LIMIT、timeout、敏感字段脱敏 |
| `github.*` | repo allowlist、ATG 管理的 Connector Secret 运行时注入、写操作 approval |
| `http.request` | host allowlist、SSRF guard、method 派生审批、header/body/output 脱敏 |
| `mcp_<connector>.<tool>` | 外部 MCP 工具同步后纳入 Tool Registry；读工具可直通，写/未知/破坏性工具 approval |

本地动作防火墙不在这张表里——它走独立入口 `POST /api/agent-guard/evaluate`，给 Claude / Codex hook 做本地动作的风险分类、解释、审计和一次性 approval ticket，不是普通的 Tool Registry 工具。

## 技术栈

| 层 | 技术栈 |
| --- | --- |
| Backend | Go, chi, pgxpool, slog, OpenTelemetry |
| Frontend | React, TypeScript, Vite, Tailwind CSS, shadcn/ui |
| Storage | SQLite, PostgreSQL, MemoryStore |
| Protocol | REST, MCP Inbound Streamable HTTP, MCP Inbound SSE fallback |
| Policy | YAML defaults + workspace-managed policy rules |

## 本地验证

文档级检查：

```powershell
git diff --check
```

后端：

```powershell
cd backend
go test -count=1 -timeout 60s ./...
go vet ./...
```

前端：

```powershell
cd frontend
npm run check
npm run build
```

## License

MIT. See [LICENSE](./LICENSE).

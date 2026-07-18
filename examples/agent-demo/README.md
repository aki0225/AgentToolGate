# Agent Demo

这个目录演示 OpenAI function calling 和 Claude tool use 如何通过 AgentToolGate REST API 调用受治理工具。

如果你只想快速理解 local action firewall 的价值，优先看：

- [`windows-startup-poisoning.md`](windows-startup-poisoning.md)
- [`evidence/windows-startup-poisoning-output.txt`](evidence/windows-startup-poisoning-output.txt)

它们串起来展示的是一条完整闭环：

- Threat：被投毒的 agent 尝试写入 Windows Startup 持久化脚本
- Guard：ATG 在工具调用真正落地前识别 sensitive target 和高危脚本信号
- Decision：返回 `deny_with_ticket` 或 `deny`
- Explanation：在 Audit Logs 的 `Risk explanation` 面板解释为什么危险
- Boundary：这只是 guardrail，不替代 sandbox / OS permission boundary

## 先跑这个：Agent Guard synthetic demo

如果你的目标是展示 AgentToolGate 为什么不是“又一个 MCP 转发器”，先跑这条本地 synthetic demo。它不需要 OpenAI / Anthropic API key，也不会写真实 Startup。

1. 启动 backend。

   Docker 路径：

   ```bash
   docker compose up --build
   ```

   Windows no-Docker 路径见 [`windows-startup-poisoning.md`](windows-startup-poisoning.md) 的“启动 backend”小节。

2. dry-run，确认请求体和脱敏 header：

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File .\run-windows-startup-poisoning-demo.ps1 -DryRun
   ```

3. 发送 synthetic evaluate 请求：

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File .\run-windows-startup-poisoning-demo.ps1
   ```

4. 对照输出讲这条线：

   ```text
   poisoned agent -> synthetic Startup write -> /api/agent-guard/evaluate
   -> deny_with_ticket / deny -> explanation signals -> audit/approval boundary
   ```

脱敏后的参考输出在 [`evidence/windows-startup-poisoning-output.txt`](evidence/windows-startup-poisoning-output.txt)。如果要录屏或截图，先确认没有真实 token、完整 approval id、完整 fingerprint 或本机绝对路径。

默认场景面向 `docker compose` local auth：

- `database.query`：直通执行只读查询。
- `github.create_issue`：触发 `approval_required`，脚本只打印审批提示，不会自动审批。
- `run-windows-startup-poisoning-demo.ps1`：可运行的 synthetic Windows Startup poisoning demo，只发送 JSON，不写真实 Startup。
- `windows-startup-poisoning.md`：说明如何运行、看什么输出、以及如何安全讲这个 demo。
- `evidence/windows-startup-poisoning-output.txt`：一份脱敏后的真实 demo 输出，包含 decision/reason/explanation 摘要，可用于 README 或安全评审演示。

## 完整 Agent SDK demo

下面这条路径用于展示 OpenAI / Claude SDK function calling 如何经由 ATG 调用受治理工具；它需要对应 API key。

1. 在仓库根目录启动本地环境：

   ```bash
   docker compose up --build
   ```

2. 进入 demo 目录并安装依赖：

   ```bash
   cd examples/agent-demo
   python -m venv .venv
   . .venv/Scripts/activate  # Windows Git Bash
   # PowerShell 使用：.venv\Scripts\Activate.ps1
   pip install -r requirements.txt
   ```

3. 准备环境变量：

   ```bash
   cp .env.example .env
   ```

   至少填写 `OPENAI_API_KEY` 或 `ANTHROPIC_API_KEY`。其他默认值对应 compose local auth：

   - `AGENTTOOLGATE_URL=http://localhost:8080`
   - `AGENTTOOLGATE_CONSOLE_URL=http://localhost:5173`
   - `AGENTTOOLGATE_WORKSPACE_ORG_ID=local-org`
   - `DATABASE_QUERY_SQL=SELECT namespace, name, operation_type FROM public.tools`
   - `GITHUB_OWNER=acme`
   - `GITHUB_REPO=demo`

4. 运行 OpenAI demo：

   ```bash
   python openai_agent.py
   ```

5. 运行 Claude demo：

   ```bash
   python claude_agent.py
   ```

6. 运行 Windows Startup synthetic demo：

   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File .\run-windows-startup-poisoning-demo.ps1 -DryRun
   powershell -NoProfile -ExecutionPolicy Bypass -File .\run-windows-startup-poisoning-demo.ps1
   ```

   `-DryRun` 只打印脱敏后的请求头；如果配置了 `Authorization`，终端里只会显示 `Bearer [REDACTED]`。

## 公开演示建议

如果是给读者或安全评审者看，建议按这个顺序展示：

1. 打开 [`windows-startup-poisoning.md`](windows-startup-poisoning.md)，先讲攻击场景和边界。
2. 跑一次 `run-windows-startup-poisoning-demo.ps1`，展示 `deny_with_ticket`、`reason` 和 explanation signals。
3. 打开 Audit Logs 审计详情，展示 `Risk explanation` 面板中的 `riskLevel`、`targetCategory`、`matchedRule`、`signals`。
4. 最后强调：Claude / Codex 的覆盖边界不同，ATG 是 guardrail，不是完整 enforcement boundary。

## 预期输出

成功时你会看到两类关键输出：

```text
[调用工具] database_query

[调用工具] github_create_issue

[需要人工审批]
approvalId: ...
callId: ...
请打开控制台审批: http://localhost:5173/approvals
脚本不会自动审批，也不会调用 /api/approvals/*。
```

`database.query` 会在审计日志中记录一次 `success` 调用。`github.create_issue` 会记录一次 `approval_required` 调用，并在控制台 `Approvals` 页面出现待审批项。

## REST 调用边界

两个脚本都只把 SDK 产生的工具调用转发到：

```text
POST /api/tool-calls
```

请求头包含：

```text
Accept: application/json
Content-Type: application/json
X-Workspace-Org-Id: local-org
```

如果设置了 `AGENTTOOLGATE_TOKEN`，脚本会额外发送：

```text
Authorization: Bearer <token>
```

## 关键假设

- compose 默认 `AUTH_MODE=local`，所以本地 demo 不需要 bearer token。
- compose 默认 `DATABASE_QUERY_ALLOWED_TABLES=public.tools`，因此默认 SQL 查询 `public.tools`。
- compose 默认 `GITHUB_ALLOWED_REPOS=acme/demo`，所以 issue demo 使用 `acme/demo`。
- compose 默认 `GITHUB_TOKEN` 为空；这不影响触发 `approval_required`，但审批通过后如果没有配置 token，真实 GitHub 创建步骤会失败。
- OpenAI 和 Claude 的模型名通过 `.env` 可覆盖；如果账号不可用或模型名变更，改 `OPENAI_MODEL` / `ANTHROPIC_MODEL` 即可。
- 如果 AgentToolGate 后端未启动，hook 会对高危落点 fail-closed，对普通 workspace 动作 fail-open 并记录 pending audit。

## 残余风险

这个 demo 只证明网关能在当前受支持路径上拦住危险写入，并不能证明覆盖所有系统动作。

- PreToolUse 不是完整的 enforcement boundary。
- Codex 侧仍存在覆盖盲区，需要叠加 sandbox。
- 审批通过只代表网关允许继续，不代表目标系统一定成功。
- DryRun 和 evidence 都会脱敏 `Authorization`；不要把真实 token 贴进截图或文档。

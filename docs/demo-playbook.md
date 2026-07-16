# AgentToolGate 产品化演示剧本

> 读者：负责给业务、架构、安全或研发团队演示 AgentToolGate 的人。
> 目标：用一轮可复现演示讲清“Agent 调工具前必须经过治理”的产品价值。

## 1. 演示主线

一句话开场：

> AgentToolGate 是 Agent 工具调用网关 / 防火墙。它不让 Agent 直接拿数据库、GitHub、内部 HTTP API、外部 MCP 工具的钥匙，而是在调用前统一做 policy、approval、rate limit、secret injection、audit 和 telemetry。

演示时不要把重点放在“又多了一个管理后台”，而是反复强调三件事：

1. **调用前治理**：工具调用先被解释、允许、拒绝或送审。
2. **凭据不外露**：Secret 只保存 env 引用，执行前才注入上游。
3. **审计可追踪**：每次调用都有脱敏输入/输出、policy、approval 和 trace。

## 2. 前置条件

默认使用 Windows / PowerShell，不使用 Docker。

- Go：`go`
- PostgreSQL：可选；如需本地 PG，请通过 `AGT_PG_CTL` / `AGT_PG_DATA` 指定路径
- Node.js 20+
- 后端端口：`8080`
- 前端端口：`5173`
- 默认 workspace：`local-org`

如需启动本地 PostgreSQL：

```powershell
& $env:AGT_PG_CTL start -D $env:AGT_PG_DATA
```

演示结束后停止：

```powershell
& $env:AGT_PG_CTL stop -D $env:AGT_PG_DATA
& $env:AGT_PG_CTL status -D $env:AGT_PG_DATA
```

如果想演示“单二进制本地版”，可以先构建：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\build-local.ps1
.\dist\agenttoolgate.exe --open
```

如果是从 GitHub Release 下载的 `agenttoolgate-windows-amd64.zip`，解压后直接运行：

```powershell
.\agenttoolgate.exe --open
.\agenttoolgate.exe doctor
```

演示前先让观众看启动摘要里的 `访问地址`、`状态库`、`SQLite 路径`、`认证模式` 和 `工作区`。如 `8080` 被占用，改用 `--port 8090` 或 `--addr 127.0.0.1:8090`，详细说明见 `docs/local-daily-use.md`。

## 3. 环境变量准备

后端 `backend/.env` 建议最小配置：

```text
PORT=8080
STORE_DRIVER=postgres
DATABASE_URL=postgres://agenttoolgate:agenttoolgate@127.0.0.1:5432/agenttoolgate?sslmode=disable
DATABASE_QUERY_URL=postgres://agenttoolgate:agenttoolgate@127.0.0.1:5432/agenttoolgate?sslmode=disable
DATABASE_QUERY_DATASOURCE=local_postgres
DATABASE_QUERY_ALLOWED_TABLES=public.tools
DATABASE_QUERY_TIMEOUT_MS=3000
DATABASE_QUERY_MAX_ROWS=100
POLICY_CONFIG_PATH=../configs/policies.yaml
AUTH_MODE=local
DEFAULT_WORKSPACE_ORG_ID=local-org
LOCAL_ROLE=owner
HTTP_ALLOWED_HOSTS=127.0.0.1:18080,localhost:18080
HTTP_ALLOWED_METHODS=GET,HEAD,OPTIONS,POST,PUT,PATCH,DELETE
CORS_ALLOWED_ORIGINS=http://localhost:5173,http://127.0.0.1:5173
DEV_MODE=true
```

如果要演示 GitHub 或 MCP Secret 注入：

```text
GITHUB_ALLOWED_REPOS=owner/repo
GITHUB_TOKEN_ENV=github_demo_token_from_backend_env
MCP_DEMO_AUTH=Bearer [REDACTED]
```

注意：Console 里创建 Secret 时，`valueRef` 填 `GITHUB_TOKEN_ENV` 或 `MCP_DEMO_AUTH` 这种 env 名，不要粘贴 token 明文。

前端 `frontend/.env`：

```text
VITE_API_BASE_URL=http://localhost:8080
VITE_AUTH_MODE=local
VITE_JAEGER_URL=http://localhost:16686
```

`VITE_JAEGER_URL` 可选；没有 Jaeger 时不影响主流程。

## 4. 启动服务

后端：

```powershell
cd backend
# Go 后端不会自动读取 .env；如果本轮修改了 backend/.env，请先导入当前进程环境变量。
Get-Content .env | Where-Object { $_.Trim() -and -not $_.TrimStart().StartsWith("#") } | ForEach-Object {
  $name, $value = $_ -split "=", 2
  [Environment]::SetEnvironmentVariable($name.Trim(), $value.Trim(), "Process")
}
go run ./cmd/server
```

前端：

```powershell
cd frontend
npm run dev
```

打开：

- Console：http://localhost:5173
- Health：http://localhost:8080/health

## 5. 自动化真实演示验收路径

如果演示前需要先证明“真实后端 + 真实前端 + 本地 mock 上游”可复现，可以在仓库根目录运行：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-local.ps1 -WithDemoSeed -WithE2E
```

如需同时纳入本地 PostgreSQL 集成测试：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-local.ps1 -WithPostgres -WithDemoSeed -WithE2E
```

如需证明“Requester 创建 approval、Reviewer 重启后批准成功”的多 Actor 验收：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-local.ps1 -WithPostgres -WithMultiActorE2E
```

如果本机 `5173` 已被其他项目占用，可追加 `-FrontendPort 5174` 指定前端临时端口；脚本会同步设置 Playwright `E2E_BASE_URL` 与后端 CORS。

常规真实演示路径会托管启动 backend/frontend，并运行 `frontend/e2e/local-real-demo.spec.ts`。外部上游不是公网服务，而是 Playwright 用例内的本地 mock HTTP server。默认优先使用 `127.0.0.1:18080`；如果端口被占用，脚本会自动选择空闲端口并同步注入 `HTTP_ALLOWED_HOSTS`。

多 Actor 路径会使用同一份本地 PostgreSQL：先以 requester local actor 创建需要审批的 mock 写工具调用，再重启 backend 为 reviewer local actor 完成审批。演示时可强调：

1. `requestedBy` 与 `reviewedBy` 是不同稳定 actor identity。
2. requester 自批返回 `403`，approval 仍保持 `pending`。
3. reviewer 批准后，tool call 执行结果来自创建审批时冻结的参数。
4. Audit / Approval 页面能看到 `reviewedBy`、`reason` 与最终状态。

CI Docker E2E 与本地 no-Docker 演示有一个关键差异：backend 在 container
内访问宿主机 mock HTTP server 时，mock 必须 listen `0.0.0.0`，而 tool
call URL 使用 `host.docker.internal:<port>`。本地默认仍应 listen/connect
`127.0.0.1:<port>`。不要为了测试方便把 `HTTP_ALLOWED_HOSTS` 改成通配
host；只添加明确的 host:port。

如果演示链路使用 env-backed Secret，`valueRef` 指向的 env 必须存在于
backend 进程或 backend container 环境里。Playwright 进程里的同名 env
只能用于断言和脱敏检查，不能替代 backend runtime 的 Secret 来源。

自动化验收覆盖：

1. Dashboard / Tools / Policies / Secrets / Connectors / Approvals / Audit Logs 页面可访问。
2. `mock.echo` 成功调用，并在 Audit Logs 出现成功审计。
3. Policy simulator 返回决策解释和 evaluation trace。
4. HTTP GET 命中 allowlist 后触达本地 mock HTTP server。
5. HTTP POST 首次调用返回 `approval_required`，审批前不触达 mock server。
6. approve 后才触达 mock server，并更新 tool call / audit。
7. reject 后不触达 mock server。
8. Secret 被 Connector 引用时默认删除被阻止，usage 展示引用字段。
9. force delete 后再次调用引用该 Secret 的 HTTP 请求会 fail closed，不触达上游，不泄露 demo secret 值。

也可以在已启动 backend 后单独执行 seed：

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\seed-demo.ps1
```

seed 只写演示元数据：`demo_http_api_key` Secret、`http.demo_http` Connector 和一条 `github.create_issue` 审批策略；Secret 只保存 `valueRef=AGT_DEMO_HTTP_API_KEY`，不会保存或打印真实密钥。

## 6. 演示步骤与讲解词

### Step 1：Dashboard

路径：`/`

要点：

- 展示总调用、成功、失败、待审批、平均耗时。
- 说明这是 Agent 工具治理的运营视图，不是普通 API debug 页面。

预期结果：页面标题显示 Agent 工具治理总览，侧边栏可进入 Tools、Connectors、Policies、Secrets、Approvals、Audit Logs。

### Step 2：创建 Secret

路径：`/secrets`

操作：

1. 点击“新建 Secret”。
2. 名称填 `github_token` 或 `mcp_demo_auth`。
3. 类型选择 `token` 或 `api_key`。
4. `valueSource` 保持 `env`。
5. `valueRef` 填后端环境变量名，例如 `GITHUB_TOKEN_ENV`。

讲解：

- Secret API 和前端只展示元数据与 env 引用。
- 真正的 token 只存在后端进程环境里。
- Secret 禁用、缺失或 env 未配置时，connector 调用会 fail closed。

### Step 3：配置 Connector

路径：`/connectors`

可演示三类：

- GitHub：配置 `tokenSecretRef` 与允许仓库。
- HTTP：配置允许 host 的引用视图与 `headerSecretRefs`。
- MCP：配置 SSE URL、非敏感 demo headers、敏感 `headerSecretRefs`。

MCP 示例配置：

```json
{
  "transport": "sse",
  "url": "http://127.0.0.1:8081/mcp/sse",
  "headers": {
    "X-Demo": "hello"
  },
  "headerSecretRefs": {
    "Authorization": "mcp_demo_auth"
  },
  "timeoutMs": 3000
}
```

讲解：

- Connector 记录是 workspace-scoped。
- 敏感 header 不写明文，只写 Secret 名称。
- MCP sync 后会把远端工具注册成 `mcp_<connector>.<tool>`。

### Step 4：Policy 管理与模拟

路径：`/policies`

操作：

1. 创建一条 deny 或 require_approval 规则，例如：
   - connectorType：`github`
   - toolNamePattern：`github.create_issue`
   - effect：`require_approval`
2. 在 simulator 填同样的 tool / operation / risk / resource。
3. 点击“模拟”。

讲解：

- simulator 只评估策略，不执行真实工具。
- 用户规则只能收紧默认策略，不能绕过硬护栏。
- 未命中用户规则时继续走默认策略。

### Step 5：运行 mock.echo

路径：`/tools` 或 `mock.echo` 的 Tool Detail。

参数示例：

```json
{
  "message": "hello from product demo"
}
```

预期：直接 success，并在 Audit Logs 出现一条成功调用。

### Step 6：运行 database.query

路径：`database.query` Tool Detail。

SQL 示例：

```sql
SELECT namespace, name, operation_type, risk_level FROM public.tools ORDER BY created_at DESC LIMIT 5
```

讲解：

- 只允许 SELECT。
- 禁止 DML/DDL、多语句和非白名单表。
- 自动 LIMIT / 最大行数 / timeout 控制查询成本。
- 输出字段按敏感 key 脱敏后才进入 audit。

### Step 7：运行 HTTP GET

路径：`http.request` Tool Detail。

需要先确保 `HTTP_ALLOWED_HOSTS` 包含目标 host，例如 `127.0.0.1:18080`。

参数示例：

```json
{
  "method": "GET",
  "url": "http://127.0.0.1:18080/status",
  "headers": {
    "X-Demo": "hello"
  }
}
```

讲解：

- GET / HEAD / OPTIONS 是 safe method，可直通。
- POST / PUT / PATCH / DELETE 会进入 approval。
- Authorization、Cookie、Host 等敏感或危险 header 不允许用户明文传入。
- 非 2xx 会写 failed audit，不透传大 body。

### Step 8：运行 GitHub get PR

路径：`github.get_pull_request` Tool Detail。

参数示例：

```json
{
  "owner": "owner",
  "repo": "repo",
  "pullNumber": 1
}
```

讲解：

- repo 必须命中 connector 或全局 allowlist。
- token 由后端 Secret/env 注入，不出现在请求参数、audit、日志或前端响应。
- 如果没有真实 GitHub token，可以说明此步用 mock server 在自动化测试里覆盖。

### Step 9：MCP sync + call

路径：`/connectors` 与同步出的 MCP Tool Detail。

操作：

1. 创建 MCP Connector。
2. 点击“同步”。
3. 到 Tools 找 `mcp_<connector>.<remoteTool>`。
4. 输入 JSON 参数执行 read tool。

讲解：

- 外部 MCP 工具不是绕过网关，而是同步进 Tool Registry 后再受治理调用。
- 写工具、破坏性工具或未知风险工具会进入 approval。
- MCP raw body、token、session、header 不进入 audit。

### Step 10：触发写操作 approval

可选工具：

- `github.create_issue`
- `http.request` POST/PUT/PATCH/DELETE
- MCP write tool
- 自定义 mock write tool

预期：

- 首次调用返回 `approval_required`。
- 上游 mock server / GitHub / HTTP / MCP 在审批前不应收到真实请求。

### Step 11：审批并查看 Audit Logs

路径：`/approvals`、`/audit`

操作：

1. 在 Approvals 批准或拒绝。
2. 进入 Audit Logs，按 tool 或 status 筛选。
3. 展开详情查看 input、output、policy、approval、trace。

讲解：

- 批准后才执行；拒绝后不执行。
- Audit 只保存脱敏 payload。
- trace id 可关联 Jaeger。

### Step 12：Secret Usage 删除保护

路径：`/secrets`

操作：

1. 找到被 Connector 引用的 Secret。
2. 点击删除。

预期：

- 默认阻止删除。
- 弹窗列出引用它的 connector、字段和 target。

### Step 13：force delete 后 fail closed

操作：

1. 在删除保护弹窗点击强制删除。
2. 再次执行引用该 Secret 的 connector。

预期：

- 调用在执行前 fail closed。
- 不触达上游。
- error / audit 不泄露 secret 明文。

## 7. 常见演示降级方案

- 没有真实 GitHub：只展示 connector/secret/policy 配置，并说明 GitHub runtime 在后端 httptest mock server 中有覆盖。
- 没有外部 MCP：使用 `frontend/e2e/connectors.spec.ts` 的 mock MCP server 模式，或只讲 sync/call 合约。
- 没有 Jaeger：展示 audit trace id，并说明配置 `VITE_JAEGER_URL` 后可跳转。
- PostgreSQL 未启动：先用 memory store 演示 mock/policy/approval/audit；database.query 单独说明需要 `DATABASE_QUERY_URL`。
- 没有 Docker：可以完成 no-Docker 本地演示，但不能声称已覆盖 CI Docker
  E2E 的 `host.docker.internal` 路径。
- 没有 `TEST_DATABASE_URL`：Postgres 集成和 schema 并发回归会跳过，不能把
  frontend `check/build` 或 memory-store E2E 说成完整 CI 覆盖。

## 8. 演示后复盘问题

- 哪些调用被允许、哪些被拒绝、哪些进入审批？原因是什么？
- 审批前是否真的没有触达上游？
- Audit Logs 是否足够定位操作者、工具、策略、审批和结果？
- Secret 是否始终只以引用形式出现？
- 哪些能力仍是 MVP 边界，需要生产化补齐？

# AgentToolGate 安全评审说明

> 读者：安全评审、平台工程、后续生产化负责人。
> 重点：明确当前 MVP 已有安全控制与剩余风险，避免把 demo-only 能力误认为生产完备能力。

## 1. 当前安全目标

AgentToolGate 的安全目标不是“让 Agent 永远不可能做危险动作”，而是把 Agent 调用外部工具的路径从“直连系统”改成“先治理再执行”：

```text
Agent -> Tool Call API -> Policy -> Approval -> Connector Runtime -> Audit / Telemetry
```

当前 MVP 重点防住以下风险：

- Agent 绕过审批直接执行写操作。
- Agent 直接持有数据库、GitHub、HTTP、MCP 上游凭据。
- 高风险或不可逆动作缺少人在回路。
- 调用失败、拒绝、审批和执行结果没有审计证据。
- token/password/secret/cookie/raw body 被写入 audit、日志或前端响应。

## 2. 已有安全控制

### 2.1 Policy 与 Approval

- 所有 tool call 先进入 policy 决策，输出 `allow` / `deny` / `require_approval`。
- 写操作、高风险工具、`requiresApproval=true` 工具默认进入 approval。
- `http.request` 根据 method 派生治理：safe methods 可直通，write methods 必须审批。
- `github.create_issue` 等写工具必须 approval 后才触达上游。
- MCP Outbound 写工具、破坏性工具或未知风险工具必须 approval。
- approve/reject 只允许 owner/admin。
- 审批使用原子状态迁移，避免重复 approve 导致重复执行。

### 2.2 用户 Policy 不能放开硬护栏

workspace 托管 policy rules 用于解释或收紧默认策略，不允许绕过：

- `database.query` SQL Guard、SELECT-only、多语句拒绝、表白名单。
- GitHub repo allowlist。
- HTTP host allowlist、SSRF guard、method/header/redirect 校验。
- MCP connector workspace 隔离、secretRef 校验、connector disabled。
- Secret 缺失、禁用、valueSource 不支持、env 未配置 fail closed。
- Rate limit。

### 2.3 Secret 管理

- Secret 是 workspace-scoped 元数据。
- 当前只支持 env-backed `valueRef`，即后端环境变量名。
- API 和前端不返回解析后的 secret 值。
- GitHub/HTTP/MCP 运行时只在最终执行前解析并注入 secret。
- Secret 缺失、禁用、env 未配置时 fail closed，不触达上游。
- Secret Usage API 可展示被哪些 connector 字段引用。
- 被引用 Secret 默认删除返回 409；force delete 后后续调用继续 fail closed。

### 2.4 Adapter 硬护栏

| Adapter | 已有控制 |
| --- | --- |
| `database.query` | SELECT-only、禁止多语句、禁止 DML/DDL、表白名单、schema introspection、LIMIT、timeout、敏感字段脱敏 |
| `github.*` | repo allowlist、owner/repo/pullNumber/title/body 校验、token redaction、write approval |
| `http.request` | http/https only、host allowlist、metadata/0.0.0.0/非白名单 localhost 拒绝、危险 header 拒绝、response 限长与脱敏、write approval |
| MCP Outbound | workspace-scoped connector、SSE 最小协议、headerSecretRefs 注入、读写治理、input/output/error/body 脱敏 |

### 2.5 Audit 与 Telemetry 脱敏

- `tool_calls.input_redacted_json` 和 `output_redacted_json` 保存脱敏 payload。
- 只有 pending approval 需要的 raw execution input 会临时存在内部字段，approve/reject 后清空。
- errorMessage 不应包含 token、secret、authorization、cookie 或上游敏感 body。
- slog 不记录原始请求/响应 payload 或 secret。
- OTel span attribute 只记录 bounded metadata，不记录 raw SQL literal、headers、body、token 或 MCP session。

### 2.6 Local Action Firewall 边界

- PreToolUse / adapter 能把高危本地动作导入治理闭环。
- Codex 侧 `require_approval` 降级为 `deny_with_ticket`，避免输出不支持的 ask。
- synthetic Windows Startup demo 只使用临时/模拟路径，不写真实 Startup。

## 3. 明确剩余风险

### P0 / P1 生产化风险

1. **Secret 不是 KMS/Vault**
   - 当前 Secret 只保存 env 引用，没有加密密文、版本、轮换、访问审计或外部 provider。
   - 生产环境应接入 Vault、云 Secret Manager 或 KMS-backed encrypted storage。

2. **GitHub 不是 GitHub App 流程**
   - 当前适合 PAT/demo token 验证。
   - 生产应改为 GitHub App installation token，并按 repo/org 最小授权。

3. **HTTP SSRF 防护仍不完整**
   - 当前有 scheme、host allowlist、metadata 地址、0.0.0.0、非白名单 localhost 等基础 guard。
   - 尚未做 DNS 解析后网段判定、DNS rebinding 防护、redirect 后 DNS 复检。

4. **RBAC 与职责分离仍需加强**
   - owner/admin 边界已存在，但 Secret、Connector、Policy、Approval 的细粒度权限和双人复核还不是生产级。

5. **迁移与部署仍是 MVP**
   - schema bootstrap / migration 不是完整版本化迁移系统。
   - 尚缺备份恢复、灰度发布、配置漂移检测、告警、SLO、灾备。

### P2 后续增强

- MCP Outbound 支持 Streamable HTTP、OAuth、长连接韧性、大 payload 策略。
- Policy 支持版本发布、回滚、dry-run rollout、导入导出。
- Audit 支持报表、保留策略、归档、SIEM 集成。
- Approval 支持 Slack/飞书通知、多级审批和超时升级。
- Rate limit 支持分层 quota、组织级预算和突发控制。

## 4. 不应误解的点

- AgentToolGate 是治理网关，不替代操作系统 sandbox、云 IAM、网络隔离和最小权限。
- Local Action Firewall 是 guardrail，不是完整 enforcement boundary；TOCTOU 仍然存在。
- `valueRef` 是环境变量名，不是 secret 明文，也不是 KMS key。
- Policy `allow` 不是万能放行；硬护栏失败仍必须 fail closed。
- Audit 是追踪与问责，不是第二个密钥仓库或 payload 存储。

## 5. 安全评审建议优先级

1. 将 Secret 后端从 env-backed demo 升级为外部 Secret Provider。
2. 将 GitHub PAT 替换为 GitHub App installation token。
3. 为 HTTP adapter 增加 DNS 解析后 IP 网段判定和 rebinding 防护。
4. 将 RBAC 细化到 Secret / Connector / Policy / Approval 的动作级权限。
5. 建立 migration、备份、审计保留、告警和 SLO。
6. 为 MCP Outbound 增加 OAuth / Streamable HTTP 与 payload 分级治理。

## 6. 评审证据建议

- 后端测试输出：`go test ./...`
- 前端类型检查：`npm run check`
- 前端构建：`npm run build`
- E2E smoke：`npm run e2e -- e2e/product-readiness-smoke.spec.ts`
- Audit Logs 截图：包含 policy decision、approval status、trace id、脱敏 input/output。
- Secret 删除保护截图：被引用 Secret 的 usage dialog 与 force delete 提示。
- 失败路径截图：Secret 缺失/禁用后 connector fail closed，且错误不含 secret 明文。

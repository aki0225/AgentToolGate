# 威胁模型

AgentToolGate 假设 AI agent 会持续接触不可信上下文，但仍被要求操作真实工具。项目目标是降低危险工具调用的后果，而不是让提示词注入不可能发生。

## 资产

ATG 保护或治理的资产包括：

- workspace source code 和项目配置
- `.env`、`.ssh`、云凭证、git hooks 等本地 credential 落点
- 配置 token 或 Secret 可访问的 GitHub repositories
- `database.query` 可访问的数据库
- `http.request` 可访问的内部 HTTP 服务
- 外部 MCP Server 及其 remote tools
- workspace policy、approval、connector、secret metadata
- audit logs 和 traceability evidence

## 威胁者与攻击面

威胁者不一定是直接拥有 shell 的人，也可能是：

- 隐藏在上游内容里的 prompt injection
- 恶意或被攻陷的 MCP Server
- 被污染上下文影响的模型输出
- 权限过大的本地 agent session
- 疲劳审批或误批的 operator
- 暴露 secret 或扩大网络访问面的集成配置错误

主要攻击面：

- REST `POST /api/tool-calls`
- Local Action Guard `POST /api/agent-guard/evaluate`
- MCP Inbound `/mcp` 和 `/mcp/sse`
- MCP Outbound connector sync/call
- GitHub adapter
- HTTP adapter URL/header 处理
- `database.query`
- Claude / Codex local PreToolUse hooks
- approval workflow
- env-backed Secret resolution
- audit 和 telemetry 输出

## 可信边界

ATG 将这些输入视为不可信或半可信：

- agent-generated tool arguments
- MCP client requests
- external MCP tool metadata 和 responses
- HTTP response bodies
- GitHub response bodies
- user-managed policy rules
- local hook payloads

ATG 依赖：

- 后端 workspace 和 role 解析
- 后端 policy 与 adapter hard guardrails
- 后端 runtime env 中当前 Secret values
- store 的 approval/ticket 原子状态迁移
- 上游系统自己的 least privilege

## 关键攻击路径

### Prompt Injection 到工具执行

注入指令让 agent 调用真实工具。ATG 通过 policy、adapter validation、approval 和 audit 来缓解后果。

残余风险：ATG 不移除、也不识别所有恶意 prompt。它只治理受支持的 tool-call 路径。

### 审批前写入

Agent 尝试创建 GitHub issue、发送 HTTP POST 或调用 MCP write tool。ATG 应返回 `approval_required`，审批前不触达上游。

残余风险：如果动作完全在 ATG 之外，或不在客户端 hook 覆盖范围内，ATG 无法治理。

### Secret 外泄

Agent 尝试把 Authorization header、token、cookie、private key 或 body secret 放进 tool input/output。ATG 使用 env-backed Secret refs、敏感 header 限制和 audit redaction 缓解。

残余风险：env-backed Secret 不是 KMS/Vault。被攻陷的 backend runtime 或 host 仍可能访问 env values。

### 本地持久化

被注入的 agent 写 Windows Startup 脚本、`.git/hooks`，或执行隐藏 PowerShell payload。Local Action Firewall 会识别敏感落点和 hidden execution 信号，并返回 deny 或 `deny_with_ticket`。

残余风险：PreToolUse hooks 是 guardrails，不是 OS-level enforcement，仍有 TOCTOU。

### MCP Tool Poisoning

外部 MCP Server 宣称某些工具只读，但实际可能写入或 open-world。ATG 将外部工具同步成 `mcp_<connector>.<tool>`，并对 unknown/write/destructive 工具保守映射。

残余风险：remote tool semantics 仍可能撒谎。仍需要上游服务权限边界和 approval review。

### HTTP SSRF

Agent 要求 `http.request` 访问 metadata、loopback 或私网目标。ATG 使用精确 authority
allowlist；每次请求和重定向都会重新解析 DNS、校验地址，并固定拨号到本次已校验 IP。
metadata、link-local、非显式 loopback 和解析异常会在建立连接前拒绝，环境代理不会
改变实际目标。

残余风险：显式 allowlist authority 目前仍可解析到普通 RFC1918、IPv6 ULA 或 CGNAT
私网地址，以兼容受控内部服务。ATG 没有独立的“公开目标/内部目标”网络分区策略，
因此不能把当前实现描述成完整私网隔离或完整 DNS rebinding 防护。

## 已有缓解

- Tool calls 是 workspace-scoped。
- Policy decision 只有 `allow`、`deny`、`require_approval`。
- Managed policy 可以收紧但不能绕过 hard guardrails。
- SQL guard 强制 SELECT-only、表白名单、timeout 和 row limit。
- GitHub adapter 使用 repo allowlist，写操作需要 approval。
- HTTP adapter 执行 authority allowlist、逐请求和 redirect DNS 复检、固定 IP 拨号、
  环境代理禁用、method-derived approval、危险 header 限制和 response redaction。
- MCP inbound `tools/call` 复用 `createToolCall`。
- MCP inbound 请求和响应限制为 1 MiB，并拒绝尾随 JSON。
- MCP outbound sync 将 remote tools 注册为受治理的 `mcp_*` tools；SSE、POST、
  JSON-RPC result、工具数量和 input schema 均有固定上限。
- Env-backed Secret refs 只在 backend runtime 解析，缺失时 fail closed。
- Approval 状态迁移原子化；approve/reject 后清空内部 raw execution input。
- Local `deny_with_ticket` 使用 fingerprint、TTL 和一次性消费。
- 低/中风险 remembered allow 不适用于高风险本地动作。
- Audit logs 保存脱敏 input/output 和安全 explanation fields。
- OTel attributes 有边界，不应包含 raw secrets 或 bodies。

## 明确未覆盖项

ATG 当前不提供：

- 完整 prompt-injection prevention。
- OS sandbox、kernel policy 或 EDR。
- 完整 Codex interactive ask 体验。
- Guard Core 不会递归解码普通文件写入内容中的多层编码 payload；直接命令中的隐藏执行特征
  仍会进入规则判断，但不能把 ContentPreview 检查当作完整内容扫描或 OS enforcement。
- KMS/Vault/cloud Secret Manager 集成。
- GitHub App installation-token 生产生命周期。
- 对普通 RFC1918、IPv6 ULA 或 CGNAT 私网目标的独立授权策略和完整 DNS rebinding
  防护。
- 完整 object-level RBAC 和职责分离。
- 生产级 migration framework、backups、alerting、SLO 或 disaster recovery。
- 企业 SIEM/DLP/taint tracking。
- 自动安全策略降级。

## 生产化前提

如果要把 ATG 当成生产控制，需要补齐：

- 将 Secret storage/resolution 接入 KMS、Vault 或云 Secret Manager。
- 用 GitHub App installation tokens 替代 PAT/demo token。
- 为 HTTP / MCP 出站增加可配置的公网与私网目标分区策略，避免仅凭 authority
  allowlist 授权一般私网地址。
- 为 policies、approvals、secrets、connectors 定义 object-level RBAC。
- 实现版本化 migrations、backup/restore 和 disaster recovery。
- 增加 operational alerting、SLO 和 audit retention policy。
- 在目标环境中验证真实 Codex / Claude Code client integration。

## 结论

ATG 是工具治理网关和本地动作 guardrail。它通过确定性检查、人工审批和审计降低被攻陷或误判 Agent 的 blast radius。它应作为 defense-in-depth 的一层，而不是唯一安全边界。

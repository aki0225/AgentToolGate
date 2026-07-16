# 实现工单：本地危险动作防火墙（P0）

> 类型：实现任务单（给实现 AI 读着做）
> 配套规格：`doc/local-action-firewall-p0-spec.md`（下称 spec，所有 §引用指它）
> 配套架构：`doc/AgentToolGate-architecture-v0.1.md`
> 状态：待实现
> 验收基准：spec §11 DoD 全部勾选

---

## 0. 开工前必读

1. 先完整读 `doc/local-action-firewall-p0-spec.md` 和 `doc/AgentToolGate-architecture-v0.1.md`。
2. spec 是唯一事实来源。本工单是落地指引，**不得删改 spec 里定的任何安全约束**。
3. 严格按 spec §11 DoD 验收清单交付。

---

## 1. 现有代码：必须复用，不得重写

这是一个**已存在**的项目（Go 1.26 后端，chi 路由），核心治理能力已实现。**严禁另起一套 policy / approval / audit / masking。**

| 能力 | 位置 | 复用方式 |
|------|------|----------|
| 策略引擎 | `internal/policy/engine.go` | `Engine.Evaluate(input Input) Decision`，Effect = `allow\|deny\|require_approval`；YAML 规则 + 优先级 + 通配 + 热加载 |
| 默认规则 | `internal/policy/defaults.go`（`DefaultRules()` / `NewDefaultEngine()`） | 新增危险动作规则加这里 |
| 流程编排参考 | `internal/app/tool_call_service.go`（`createToolCall`） | 已串好 policy→approval→audit→masking→OTel span，照此模式写新流程 |
| 审批创建 | `a.store.CreateApprovalRequest(...)` | 直接复用 |
| 审批事件（SSE） | `a.publishApprovalEvent(...)`、`internal/app/approval_sse.go` | 直接复用；前台已有 `/api/approvals/*` |
| 审计 / 脱敏 | `tool_call_service.go` 的 `redactToolInputForAudit` / `redactToolOutputForAudit` | 直接复用 |
| 存储 | `internal/store/`（`memory.go` + `postgres.go` 双实现，`migrations/`） | 新表**两边都要实现**并加 migration |
| 路由注册 | `internal/app/app.go` 的 `r.Route("/api", ...)`（约 152 行） | 新 endpoint 加这里，沿用 `a.authMiddleware` |
| 遥测 | `internal/telemetry`（`StartSpan`） | 沿用现有 span 模式 |

**关键约束**：当 `policy.Match` / `policy.Input`（见 `engine.go` 56–66 行）现有维度不足以表达新需求（如 path 落点、content hash、扩展的 operation_type）时，**扩展这些 struct**，不要绕过引擎自己写 if-else 判断。

---

## 2. 交付物（对应 spec §5 / §6 / §11）

1. **`POST /api/agent-guard/evaluate`**
   入参：规范化的 PreToolUse 事件（`adapter` / `workspace` / `actor` / `tool` / `action_type` / 目标路径 / 命令或内容 / 是否脚本 等）。
   出参：`allow | deny | deny_with_ticket`。
   实现：复用 `policy.Engine`，按 `tool_call_service.go` 的编排模式接 approval / audit / masking / span。

2. **Claude Code adapter**
   PreToolUse hook（脚本/程序）→ 调 `/api/agent-guard/evaluate` → 把决策翻成 Claude hook 返回格式；`require_approval` 走原生 ask / defer。

3. **Codex adapter**
   PreToolUse hook → 受支持路径返回 `deny`；**`require_approval` 永远翻成 `deny_with_ticket`，绝不输出 ask**（spec §3 硬约束，必须单测守住）。

4. **`deny_with_ticket` 全契约**（spec §6）
   一次性 ticket、TTL 10min、指纹绑定、CAS 原子消费、并发只一个成功。

5. **路径规范化 + 文件身份**（spec §6.1）
   canonical target + file identity；处理 Windows 大小写 / 8.3 短名 / `\\?\` / UNC / symlink / junction / reparse point / hardlink / ADS / trailing dot / WSL / PowerShell provider 路径。
   保守失败：拿不到 file identity 时，敏感路径上的 symlink / junction 一律 `deny` 或强制重新审批。

6. **preventive / detective 分层 + 指纹记住决定 + 摩擦预算指标**（spec §2）

7. **审批单决策层字段齐全**（spec §5.2）
   含 base64 / 混淆解码后的 payload、脚本哈希、ticket 指纹、TTL、是否一次性消费。

8. **四条命门默认值**（spec §7）：fail 分级 / 延迟预算 / 脚本内容扫描 / 自我防篡改。

9. **双 demo + README 残余风险声明**（spec §9 / §10）。

---

## 3. 绝对红线（违反即返工 —— 来自多轮安全 review 的结论）

- **Codex 永不输出 ask**：`require_approval → deny_with_ticket`，翻译失败也必须 `deny`。必须有单测。
- **P0 不得有任何"自动放松 preventive 规则"的代码路径**（spec §2.3）：
  - 摩擦预算 / 精确率只做**指标采集 + `recommend_demote` 建议**，**不得自动降级**。
  - **不得用批准率反推误报率**（批准 ≠ 误报）。
  - 高危规则（持久化 / 凭证外传 / 自我防篡改 / secret egress / 工作区外破坏性删除）**永不自动降级**。
- **ticket 消费必须 CAS 原子**：`approved → consumed` 只成功一次；命令执行失败也不复用。
- **高危动作不可被"记住决定"**，每次都重新确认。

---

## 4. 必过单测（spec §6 的 6 条）

1. Codex adapter 任何 `require_approval` 输入 → `deny_with_ticket`，永不 ask。
2. 批准后第一次同指纹重试 → `allow_by_ticket`。
3. 第二次同指纹重试 → `deny`（已消费）。
4. 不同落点 / 不同脚本内容 / 过期 ticket → `deny`。
5. 并发两次重试 → 仅一次 `allow`，另一次 `deny`。
6. 路径等价绕过：大小写 / 8.3 / `\\?\` / trailing dot / symlink 指向，均归一到同一 canonical target，不复用他者 ticket；无 file identity 的敏感 symlink / junction → `deny`。

---

## 5. 工作纪律

- **先输出实现计划**（要改哪些文件、加哪些表 / migration、新增哪些 policy 字段），再动手写代码。
- 全程 `go build ./...` 与 `go test ./...` 必须通过；新功能必须带测试。
- 不得删除或破坏现有 `/api/*`、`/mcp/sse` 路由及现有测试。
- **不要执行 git commit**（提交节奏由项目负责人控制）。
- 完成后对照 spec §11 DoD **逐条自检**，列出每条完成状态 + 对应文件 / 测试。

---

## 6. 自检清单（交付时填写）

对照 spec §11，逐条标注 ✅/❌ 与证据（文件路径 + 测试名）：

- [ ] `/api/agent-guard/evaluate` 跑通，复用现有 Policy/Risk/Approval/Audit/Masking
- [ ] 不打扰验收：典型 1h 会话 preventive 审批个位数，无"每个 write/exec 都问"
- [ ] preventive/detective 分层落地
- [ ] 指纹级"记住决定"，高危不可被记住
- [ ] 摩擦预算指标 + dashboard；噪声规则仅 `recommend_demote`
- [ ] 无任何自动放松 preventive 的代码路径；不用批准率反推误报率
- [ ] Claude Code adapter（强拦截 + 原生 ask/defer）
- [ ] Codex adapter（受支持路径 deny；require_approval→deny_with_ticket；单测守住永不 ask）
- [ ] deny_with_ticket 全契约 + CAS + 6 条单测全过
- [ ] 指纹落点经路径规范化 + 文件身份绑定
- [ ] 审批单决策层字段齐全（解码 payload + 脚本哈希 + 指纹）
- [ ] 四条命门默认值落地
- [ ] 双 demo 可复现，控制台展示"为什么 Blocked"
- [ ] README 残余风险声明就位

# 本地动作防火墙

本地动作防火墙是 ATG 面向本地 AI coding agent 动作的 guardrail。它存在的原因是：提示词注入真正产生破坏时，往往不是停留在模型文本里，而是落在本地工具调用上，例如写文件、跑脚本、改 hook、读凭据或修改安全配置。

这层不是 OS sandbox。它是 deterministic pre-tool-use guard，用于降低风险、提供审批和审计。真实部署仍需要 sandbox、最小权限、网络策略和客户端 permission profile。

## 它解决什么问题

AI coding agent 可能被诱导执行完全不经过 MCP 的动作：

- 写持久化脚本到 Windows Startup。
- 修改 `.git/hooks`。
- 读取 `.ssh` key、`.env` 或 credential 目录。
- 执行带 `ExecutionPolicy Bypass`、`WindowStyle Hidden` 或 encoded payload 的 PowerShell。
- 修改 `.codex/hooks`、`.claude/hooks` 或 ATG 自身 policy/config。

本地动作防火墙把这些动作在落地前带入同一套风险模型。

## 项目内保护规则

`agenttoolgate init` 会生成 `.agenttoolgate/protected.json`。空规则保持日常开发低噪音；项目可以把核心目录和生产配置提升为 high-risk approval 或 deny：

```json
{
  "version": 1,
  "localActionFirewall": {
    "enabled": true,
    "protectedPaths": [
      {
        "pattern": "src/core/**",
        "read": "require_approval",
        "write": "require_approval",
        "delete": "deny",
        "reason": "核心算法目录"
      }
    ],
    "egress": {
      "enabled": true,
      "allowedHosts": ["api.github.com"],
      "unlistedWrite": "deny"
    }
  }
}
```

路径只支持 repo-relative exact / subtree，不支持任意 glob、正则、绝对路径或 `..`。规则效果只有 `require_approval` 和 `deny`，多条规则以及多目标 patch 始终取最严格结果。`.agenttoolgate/config.json` 和 `.agenttoolgate/protected.json` 自身也属于 self-tamper 目标。

调整规则时先切到 `dry-run`，编辑完成后再切回 `live`。两个启用命令都会校验配置；`off` 不读取规则，始终可用于恢复开发。

项目规则在三个位置形成 floor：

- Go Hook 在调用后端前评估，普通 repo 写入仍可 no-op，受保护动作不能被后端旧的 allow 降级。
- `/api/agent-guard/evaluate` 使用后端可信 `AGT_PROJECT_ROOT` 重新加载规则，客户端伪造 workspace root 不能绕过。
- Go CLI 不可用时，Claude / Codex Python Hook 使用独立的最小静态 matcher；两套实现分别由对应回归用例约束安全下限，损坏配置在 live 下保守拒绝。

网络规则检查 Hook payload 中的显式 URL，并静态识别当前支持的 `curl`、`Invoke-RestMethod` / `Invoke-WebRequest` 及有限嵌套形式；动态拼接 URL 和未知命令不保证识别。允许 host 仍要服从内置网络写入审批、HTTP/MCP 部署级 allowlist 和 Connector 约束；项目配置只能继续缩小范围。

`/api/agent-guard/evaluate` 评估的是官方 Hook 提交的声明式动作描述，不是 OS 行为证明。若不受信任的进程同时伪造 tool/action/target 并绕过 Hook 自行执行，ATG 无法把这类请求升级成真正的 enforcement boundary。

## 它不解决什么问题

它不负责：

- 阻止提示词注入进入模型上下文。
- 证明所有本地动作路径都被覆盖。
- 追踪受保护文件内容的后续数据血缘、变量传播、编码、压缩或跨工具复制。
- 消除 hook 检查和真实执行之间的 TOCTOU。
- 替代 OS 文件权限、sandbox profile 或网络隔离。
- 保证第三方客户端 hook 协议长期不变。

## 三种模式

repo-local hook control 文件位于：

```text
.tmp/agenttoolgate/hook-control.json
```

支持模式：

| 模式 | 行为 |
| --- | --- |
| `off` | 显式关闭或控制文件缺失时 hook no-op；不调用后端、不写 pending audit、不输出 hook JSON。已存在但无效的控制文件不会降级为 `off`，受治理调用会保守拒绝。 |
| `dry-run` | 分类并写入脱敏预览 `.tmp/agenttoolgate/hook-dry-run.jsonl`，但不阻断。 |
| `live` | 执行真实 hook 映射。 |

最高优先级硬关闭：

```text
TRELLIS_HOOKS=0
TRELLIS_DISABLE_HOOKS=1
```

控制命令：

```powershell
agenttoolgate.exe hook control status
agenttoolgate.exe hook control off --reason "pause ATG hooks"
agenttoolgate.exe hook control dry-run --reason "preview only"
agenttoolgate.exe hook control live --reason "guard this session"
```

该控制文件只影响当前仓库，不修改用户全局 Codex / Claude Code 配置。

## 本地动作分类

hook payload 会被规范化为本地动作：

- adapter：`codex` 或 `claude`
- tool：shell、write/edit、apply_patch 类编辑或客户端工具名
- action type：read / write / exec / delete / patch / post
- target：规范化路径或逻辑目标
- content：脚本或文件内容，进入 audit 前脱敏
- optional file identity / parent identity

项目规则评估 `apply_patch` 时，Add / Update 和 Move 目标按 write 处理，Move 源路径和 Delete 按 delete 处理，并对所有目标采用最严格结果。

风险由落点和内容共同驱动，不是简单地“所有 write 都危险”。

敏感落点示例：

- `.env`
- `.ssh`
- `.git/hooks`
- Windows Startup
- credentials / secrets 目录
- ATG 自身 hook/config/policy 文件

内容信号示例：

- `password`、`secret`、`token`、`authorization`、`cookie`
- private key marker
- base64 或 encoded payload
- PowerShell `ExecutionPolicy Bypass`
- PowerShell `WindowStyle Hidden`
- `-EncodedCommand` / `-enc`

低风险 workspace 动作可以 allow 或 dry-run preview，避免打扰。高风险持久化、凭据、自我防篡改动作会变成 deny 或 `deny_with_ticket`。

## `deny_with_ticket`

当本地动作需要审批时，`/api/agent-guard/evaluate` 返回：

```text
decision = deny_with_ticket
approvalId = <approval id>
fingerprint = <fingerprint>
```

fingerprint 绑定：

- adapter
- workspace
- actor identity
- tool
- action type
- canonical target
- resolved file identity，若可用
- parent identity，适用于新文件
- content hash 和 script hash

ticket TTL 是 10 分钟，并且只能消费一次。审批后，带相同 ticket 的重试可以 allow 一次；重复使用已消费 ticket 会 deny。

这个机制同时避免两类失败：

- 审批后重试仍然无限被 deny，形成死循环。
- ticket 可重复使用，形成隐藏后门。

## 低/中风险 remembered allow

低/中风险 fingerprint 在审批后可在 TTL 内 remembered allow。高风险本地动作不能进入长期静默 allow。

具体口径：

- 低/中风险同 fingerprint：可以 remembered allow。
- high/critical、sensitive target、self-tamper：必须继续 ticket 或 deny。
- safe workspace allow 不覆盖 high/critical risk。

这是低噪音设计的一部分：普通日常开发不应被反复打断，但高危动作不能被训练成“一次批准后永久静默”。

## Claude / Codex 差异

| 客户端 | 当前行为 |
| --- | --- |
| Claude Code | 可以使用原生 ask/confirm 风格 hook output 来表达需要人工确认的动作；但这仍是 guardrail，不是 OS enforcement。 |
| Codex | 当前 runtime hook compatibility 不提供完整交互式 ask 体验。Guard `allow` 运行时 no-op/直接放行；Guard `ask` 或 `deny_with_ticket` 保守映射为 deny。 |

不要宣传 Codex 已有完整 ask UX。`guard adapt codex` 可以保留 dry-run 的 `allow` / `deny` / `ask` 诊断语义，但 `guard hook codex` 运行时输出必须兼容 Codex：不能输出 unsupported `permissionDecision=allow`，需要确认的动作保守 deny。

## 日常开发体验

项目级 `up` 默认使用 `dry-run`，不是 `live`。普通开发会话不应因为这些动作被频繁打断：

- `git status`、`git diff`、`git log`
- 定向测试
- 读取和编辑 workspace docs
- 在 repo 内写临时文件

`live` 用于用户显式开启的受保护会话。它应该阻断或 ticket 高危动作，而不是对每次 write/exec 都 nag。

## 已知限制和 TOCTOU 风险

- Hook 覆盖取决于客户端 runtime 和 runtime 暴露给 hook 的工具。
- PreToolUse 校验到真实执行之间存在 TOCTOU。
- 路径规范化能降低绕过风险，但不能替代所有平台上的 OS-level file identity enforcement。
- 后端离线或配置错误时，敏感落点必须保守处理。
- 项目规则依赖 Hook/API 能看到真实目标；绕过 Hook 的进程、socket 和第三方工具不在覆盖范围内。
- Dry-run evidence 必须保持脱敏；raw target URL、token、Authorization、approval id 和完整 fingerprint 不应提交到仓库。

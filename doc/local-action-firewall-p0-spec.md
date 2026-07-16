# AgentToolGate 本地危险动作防火墙 — P0 设计规格

> 版本：v0.1（P0 spec）
> 日期：2026-06-06
> 状态：已对齐，可开工
> 适用范围：Codex CLI 与 Claude Code 的本地危险动作治理

---

## 0. 一句话定位

**AgentToolGate = Codex / Claude Code 的本地危险动作防火墙 + 企业工具治理网关，一套策略同时覆盖两个 agent。**

它防的是这一类真实攻击面：**中转站 / 上游模型输出被投毒后，诱导本地 agent 执行持久化、窃密、外联等危险动作**。OWASP 已将其列为 MCP Tool Poisoning（间接提示注入）。

它**不是**又一个 MCP gateway。关键区别：它拦的是 **agent 自带的 Bash / Write / 文件 / 网络工具**，而不只是经过 MCP 的调用。

---

## 1. 威胁模型（基于真实投毒事件）

观察到的攻击链（社区已复现，2026-05）：

```
恶意中转站
  → 在 OpenAI-compatible API 响应里注入隐藏指令
  → Claude Code / Codex 把它当成模型的正常输出
  → agent 调用自带的 Bash / Write / apply_patch 工具
  → 往 Windows Startup 目录写 .ps1 + .vbs
  → ExecutionPolicy Bypass + WindowStyle Hidden 隐蔽持久化
  → 开机自动执行（截图中表现为开机弹窗刷屏，真实攻击则静默）
```

**关键事实：这条链一步都没经过 MCP。** 写文件、跑 PowerShell 用的是 agent 内置工具。因此只治理 MCP 调用的网关对这条链完全无效。

防护必须接到 agent 的 **PreToolUse / 权限钩子**这条腿上。

---

## 2. 设计第一性原理：对手是 skip，不是原生提示（决定有没有人用）

> 这是整个项目的**采用前提**。如果违反，无论安全做得多好都没人用。

**现实**：大量用户会开 `--dangerously-skip-permissions`（Claude Code）或 full-access / 跳过审批（Codex），就为了摆脱"每个动作都来问一次"的打扰。

**因此真正的对手不是原生提示，而是 skip 模式本身。** 如果 ATG 也按"能力"拦截（只要 write/exec/network 就弹审批），它就是个更漂亮的 nag，用户开 skip 逃离原生、也会开"跳过 ATG"逃离我们。**这个失败模式必须从设计上排除，不能靠用户自律。**

**原生提示烦的根因**：它按"能力"问（"要执行命令吗？要写文件吗？"），而 99.9% 的命令和写文件是无害的，于是用户被无意义地打扰到麻木，索性全关。

**ATG 的破局点：按"风险"拦，不按"能力"拦。** 让无害动作静默放行，只在真正危险的落点/特征上才打断。三条强制设计约束：

### 2.1 preventive / detective 分层

不是所有危险都要"事前拦截 + 审批"。区分两类：

- **preventive（事前同步拦截 + 需审批）**：仅限不可逆、高破坏的动作——持久化（Startup/计划任务/注册表 Run/Git hooks）、凭证读取与外传、非白名单网络外传 secret、自我防篡改。**这一类必须少而精，是审批的唯一来源。**
- **detective（放行 + 高亮审计，不打断）**：可疑但可逆、或需要事后复盘的动作。静默放行，记进审计流，控制台高亮，**不弹审批**。

**目标：preventive 触发率必须极低，绝大多数动作走 allow 快路径或 detective。**

### 2.2 指纹级"记住决定"（approve-once，scope 精确）

审批一次后，**同指纹**（§6 定义：adapter+workspace+actor+tool+action_type+canonical target+content hash）在一段时间内不再追问。

- 关键：记住的 scope 必须**精确到指纹**，不能粗到"以后所有 write 都放行"（那等于变相 skip），也不能细到"换个无关紧要的参数就重新问"（那等于没记）。
- 指纹中的落点必须经过路径规范化与文件身份绑定（见 §6.1），否则等价路径可绕过"记住的决定"。
- 与 §6 的一次性 ticket 不冲突：ticket 是单次放行凭据；"记住决定"是同指纹的可重复 allow 规则，但仅限低/中风险；preventive 高危动作**不可被记住**，每次都重新确认。

### 2.3 摩擦预算（P0）+ 推荐降级（P0 仅建议）+ 自动降级（P1，默认关）

把"打扰"当成可度量、有预算的资源——但**减少打扰的动作绝不能由系统自动放松防护**，否则审批疲劳会从"体验问题"升级成"安全漏洞"，甚至被投毒会话主动利用。

**P0 做什么：**

- **摩擦预算（指标采集 + dashboard）**：采集每条规则的触发数、审批数、拒绝数、批准后重试数、显式误报反馈、规则噪声排名。定义单位时间审批次数的健康区间（如默认每小时 ≤ N 次）。超预算 = 信号，但只用于**提示**，不触发任何策略变更。
- **`recommend_demote`（仅建议，不执行）**：系统可标记"这条规则噪声高，建议复核"，但**绝不自行把 preventive 改成 detective**。放开与否只能由人工改策略。

**P0 绝不做：**

- ❌ 任何从 preventive → detective 的**自动**策略降级。
- ❌ 用"批准率"反推"误报率"。**批准 ≠ 误报**——用户批准往往只是疲劳、看不懂、赶时间，不能反推规则不该拦。

**为什么（这是被独立 review 挑出的真实攻击面）：**

当攻击者能影响 agent 的行为样本时，自动降级就是一条**可被投毒的数据反馈回路**。prompt injection 最擅长诱导 agent 批量执行"看起来合理"的重复动作，把某条规则刷成"高命中但总被批准"，从而把系统训练到自己放开防护。即使没有攻击者，"疲劳→批准→降级→防护更松→更疲劳"本身就是一条自我恶化的漂移路径。

**precision 的数据源约束（即便到 P1 也必须遵守）：**

- precision **必须**来自显式误报反馈 / 复盘标签 / 管理员确认 / shadow-mode 对比，**不得**用批准率计算。
- **高危类规则永不自动降级**：持久化、凭证外传、自我防篡改、非白名单 secret egress、工作区外破坏性删除——只能人工改策略。
- 统计**必须抗刷量**：按 `actor / session / workspace / source` 分桶；短时间同型触发降权；一个投毒会话不能代表全局。
- 降级粒度最多到"具体指纹 / 具体策略变体"，**不降整条规则**。
- 降级需有**冷静期、最小样本、回滚窗口、审计记录**。
- P1 的自动降级**仅限低危规则，且默认关闭**。

> ATG 区别于"又一个权限工具"的核心是：**它敢让 99.9% 静默放行**——但"减少打扰"靠的是**事前把规则设准（分层 + 指纹记住）**和**人工复核噪声规则**，而不是让系统按用户的点击行为自动放松。一个会"自己关掉防护"的安全工具，恰恰是最危险的。

### 2.4 由此推导的硬验收

- 在一次典型 1 小时编码会话里，preventive 审批次数应是**个位数**（理想 0–3 次），其余全部 allow 或 detective。
- 任何导致"每个 write/exec 都问"的实现都是 P0 缺陷，等同于把用户推向 skip。
- **P0 不得存在任何自动放松 preventive 规则的代码路径**；噪声规则只能产出 `recommend_demote` 建议。
- README 必须讲清楚这个定位：**ATG 不是更严格的提示，是"几乎不打扰、只在真危险时才出现"的地板。**

---

## 3. 双 adapter 行为差异（事实层，已核实官方文档）

> 来源：[Codex Hooks](https://developers.openai.com/codex/hooks)、[Codex Agent approvals & security](https://developers.openai.com/codex/agent-approvals-security)、[Claude Code Hooks](https://code.claude.com/docs/en/hooks)

| 维度 | Claude Code | Codex CLI |
|------|-------------|-----------|
| PreToolUse 同步拦截 | ✅ 成熟、覆盖广 | ✅ 真实存在，可返回 deny |
| 可拦工具 | Bash / Write / Edit / Read / WebFetch / MCP 等 | Bash（仅简单 shell）、apply_patch 文件编辑、MCP |
| **覆盖盲区** | 相对完整 | **复杂 shell、等价工具路径、WebSearch、非 shell/非 MCP 工具不在覆盖内** |
| deny 决策 | ✅ | ✅ `permissionDecision: "deny"` + reason |
| ask / 挂起审批 | ✅ 原生支持 | ❌ **`ask` 是 parsed but not supported yet，返回后被当作 hook 失败并继续执行工具** |
| 定位 | 较完整的逐工具权限体系 | 官方明确：**guardrail，不是完整 enforcement boundary** |

> 实现口径：Codex 侧 `apply_patch` 视为受守护的文件编辑入口，按 patch diff 中的目标文件解析后走同一条 `write/edit` 风险判定与审计链路；不要把它当成一个完全独立、无需治理的旁路工具。

**两条由此推导的硬约束：**

1. **Codex 侧 `ask` 绝对不能输出。** 官方行为是"ask 不支持 → 当作 hook 失败 → 继续执行"，等于把高危动作**静默 fail-open**。`require_approval` 在 Codex adapter 必须翻译成 `deny_with_ticket`，翻译失败也必须 deny。
2. **Codex demo 不能宣传"完整拦截"。** 只能宣传"在 Codex 当前支持的工具路径内真实 inline deny"，并公开残余风险。

---

## 4. P0 架构：统一策略中枢 + 双轻量 adapter

```
┌─────────────┐     ┌─────────────┐
│ Claude Code │     │  Codex CLI  │
│ PreToolUse  │     │ PreToolUse  │
│   hook      │     │   hook      │
└──────┬──────┘     └──────┬──────┘
       │  POST /api/agent-guard/evaluate
       └──────────┬────────┘
                  ▼
        ┌──────────────────┐
        │  策略中枢（复用现有）│
        │  Policy / Risk    │
        │  Approval / Audit │
        │  Masking          │
        └──────────────────┘
```

- 两个 adapter 都是**薄客户端**：把 PreToolUse 事件规范化后发给同一个 `/api/agent-guard/evaluate`，拿回 `allow / deny / deny_with_ticket`。
- 策略、风险、审批、审计、脱敏**全部复用已有模块**，不重写。
- adapter 的差异只在「如何把中枢的决策翻译成各自 hook 的返回格式」。

---

## 5. P0 spec 三层

### 5.1 机制层（系统能不能拦）

- `POST /api/agent-guard/evaluate`：输入规范化的 PreToolUse 事件，输出决策。
- 双 adapter：Claude Code（强拦截 + 原生 ask/defer 审批）、Codex（受支持路径 deny + deny_with_ticket）。
- `deny_with_ticket` 一次性放行凭据机制（详见 §6）。
- CAS 原子消费、Codex 永不输出 ask（单测守住）。

### 5.2 决策层（人能不能正确决定放行）

审批单**强制展示**以下字段，缺一不可：

- 动作摘要（一句话：谁、想干什么）
- 落点分类（如 `Windows Startup` / `.ssh` / `workspace 内`）
- 命中规则（哪条策略、为什么命中）
- 风险等级 + 一句话风险结论
- **base64 / 混淆解码后的真实内容**（不能只展示原始命令文本）
- 脚本全文或关键片段
- 内容哈希、ticket 指纹、TTL、是否一次性消费

理由：投毒攻击靠**伪装成无害**生效。命令文本可能只是 `powershell -File update.ps1`，解码后才是 payload。没有可解释审批，`deny_with_ticket` 会退化成"点批准继续干活"的摆设，approval fatigue 会让 human-in-the-loop 失效。控制审批**频率**的机制见 §2（preventive/detective 分层、指纹级记住、摩擦预算）；可解释只解决"单次审批质量"，频率才决定"用户会不会直接关掉 ATG"。

### 5.3 边界层（知道哪里管不到 —— 必须写进 README）

- hook 是 **guardrail，不是 enforcement boundary**。
- PreToolUse 校验到实际执行之间存在 **TOCTOU 窗口**。
- 必须叠加 sandbox / permission profile / network policy 兜底。
- Codex 覆盖盲区（复杂 shell / 等价工具路径 / WebSearch / 非 shell 非 MCP 工具）如实列出。

**主动声明边界，比假装全能更专业。** 这是安全产品的基本诚实。

---

## 6. `deny_with_ticket` 实现契约（P0 代码契约，不是概念）

> 没有一次性放行凭据，批准后重试会被同一条规则再次 deny，形成**死循环**；凭据可重复使用则留下**后门**。两者都是 P0 必须堵死的。

- `require_approval` 在 Codex adapter 中**永远**映射为 `deny_with_ticket`，绝不输出 ask。
- ticket **指纹**绑定：`adapter + workspace id + actor + tool + action_type + canonical target + resolved file identity(已存在时) + parent identity(新建文件时) + content/script hash`。
- 本地动作（Write/Edit/Bash/apply_patch）的判别输入由 canonical target、文件身份、父目录身份与 content/script hash 覆盖；结构化 arguments 工具（如 MCP `database.query`）走 `createToolCall` 独立审批链路，不经过本地 ticket 指纹。
- ticket **TTL = 10 分钟**，只允许消费一次。
- 脚本执行类动作必须绑定**脚本当前内容哈希**；脚本批准后被改过，重试必须重新审批（堵 TOCTOU）。
- ticket 消费 **CAS 原子化**：`approved -> consumed` 只能成功一次。即使命令随后执行失败，也不能自动复用；用户可重新审批，但不留可重复使用的后门。
- 并发重试只能有一个成功消费 ticket，其余全部重新 deny。

### 6.1 路径规范化与文件身份（指纹的安全前提）

content hash 挡住了"先批无害内容、后换恶意内容"，但**落点（target）若不归一化，攻击者可用等价路径绕过指纹**。canonical target 必须在比较前完成规范化，且优先用操作系统的**文件身份**（inode / file id）而非字符串路径。

P0 必须处理的路径等价 / 绕过形态（Windows 尤其多）：

- 大小写、相对路径、环境变量、`~`、混用分隔符（`/` vs `\`）
- 符号链接 symlink、junction、reparse point、hardlink
- `\\?\` 前缀、UNC 路径、8.3 短文件名
- 结尾的 dot/space（`file.txt.` / `file.txt `）、备用数据流 ADS（`file.txt:stream`）
- WSL/MSYS 路径、PowerShell provider 路径

**保守失败原则**：若拿不到稳定的 file identity（例如目标尚不存在，或文件系统不支持），则在**敏感路径**上对 symlink / junction / reparse point 一律 **deny 或强制重新审批**，绝不静默按字符串路径放行。

**单测必须覆盖：**

1. Codex adapter 永不返回 ask（任何 require_approval 输入都翻译成 deny_with_ticket）。
2. 批准后第一次同指纹重试 → `allow_by_ticket`。
3. 第二次同指纹重试 → deny（已消费）。
4. 不同落点 / 不同脚本内容 / 过期 ticket → deny。
5. 并发两次重试 → 仅一次 allow，另一次 deny。
6. **路径等价绕过**：对同一敏感落点用大小写变体 / 8.3 短名 / `\\?\` 前缀 / trailing dot / symlink 指向，均归一化到同一 canonical target，不得复用他者 ticket；敏感路径上的 symlink/junction 在无 file identity 时 deny。

---

## 7. 四条命门的 P0 默认值

| 命门 | P0 默认值 |
|------|-----------|
| **fail-open / fail-closed** | 按风险分级。高危持久化、凭证读取/外传、自我防篡改、非白名单网络外联 → **fail-closed**；低风险 workspace 内读 → fail-open + 补审计。ATG 服务不可用时：高危直接 deny，低危允许并记录本地 pending audit。 |
| **延迟预算** | allow 快路径 **p95 ≤ 50ms，p99 ≤ 150ms**；hook 调 ATG 超时 **200ms**。策略本地预分类、缓存低风险 allow、审计异步写库。否则用户会直接关掉 hook。 |
| **脚本绕过** | 不能只看 Bash 命令文本。Write/Edit/apply_patch 写入 `.ps1/.vbs/.bat/.sh/.js/.py` 时扫描**内容**；Bash 执行脚本文件时解析**目标文件再扫描**。叠加 Codex sandbox 与网络策略。 |
| **自我防篡改（升 P0）** | 删除 hook、改 agent 配置、改 ATG 规则文件、改 ATG 自身二进制/配置 → 强制审批或拒绝。对应 Claude managed settings / ConfigChange，Codex managed hooks/requirements 方向。 |

---

## 8. 策略矩阵维度

落点优先于命令解析。建议按 **操作类型 × 路径类别 × 是否敏感 × 是否外联** 建矩阵，而非一刀切：

- **操作类型**：read / write / exec / network
- **路径类别**：workspace 内 / Startup / .ssh / .env / 浏览器 profile / 云凭证 / Git hooks / 包管理 install hook / 计划任务 / 注册表 Run 项
- **敏感判定**：内容含 secret / 高熵串 / 私钥 / 云凭证
- **外联判定**：目的地是否在白名单

一刀切禁外部读会毁体验，必须用矩阵分级。

---

## 9. P0 Demo 设计

**主题：Windows Startup 持久化投毒攻击复现 + 拦截。**

做成**双 demo**：

- **Claude Code**：完整阻断，展示逐工具拦截。
- **Codex**：故意设计在**受支持路径**上 —— `apply_patch` 写 Startup 脚本 + 简单 Bash 触发，确保 hook 会触发并 deny。

**demo 高光从"Blocked"升级成"为什么 Blocked"**，控制台清楚显示：

```
🛑 Blocked: persistence attempt
  落点：Windows Startup 目录
  手法：ExecutionPolicy Bypass + WindowStyle Hidden 隐藏窗口
  解码后 payload：<base64 解码出的真实内容>
  判定依据：写入开机自启动目录 + 隐蔽执行特征 = 持久化/后门
  动作：deny + 生成审批单 #xxx（TTL 10min，一次性）
```

**README 必须标注**：此 demo 证明 ATG 能拦当前 Codex 支持路径，**不证明**覆盖所有系统动作。

---

## 10. 残余风险声明（README 原文级）

> AgentToolGate 的 PreToolUse 拦截是 **guardrail，不是完整的 enforcement boundary**。
> - PreToolUse 校验与实际执行之间存在 TOCTOU 窗口。
> - Codex 侧无法拦截复杂 shell、等价工具路径、WebSearch 及非 shell/非 MCP 工具。
> - 必须叠加 sandbox、permission profile、network policy 作为物理兜底。
> - 本工具降低风险、提供可审计的人在回路，但不替代操作系统级隔离。

---

## 11. P0 完成定义（DoD）

P0 完整 = **不打扰、能拦、能审批、能解释、知道哪里管不到**：

- [ ] `/api/agent-guard/evaluate` 跑通，复用现有 Policy/Risk/Approval/Audit/Masking。
- [ ] **不打扰验收（§2）**：典型 1 小时编码会话 preventive 审批为个位数（理想 0–3 次），其余走 allow 快路径或 detective；无"每个 write/exec 都问"的行为。
- [ ] preventive/detective 分层落地：preventive 仅限不可逆高危动作；detective 放行+高亮审计不打断。
- [ ] 指纹级"记住决定"：同指纹低/中风险可重复 allow，preventive 高危不可被记住。
- [ ] 摩擦预算指标采集 + dashboard（触发/审批/拒绝/重试/误报反馈/噪声排名）；噪声规则仅产出 `recommend_demote` 建议。
- [ ] **P0 不存在任何自动放松 preventive 规则的代码路径**；不得用批准率反推误报率；高危规则永不自动降级。
- [ ] Claude Code adapter：PreToolUse hook 强拦截，require_approval 走原生 ask/defer。
- [ ] Codex adapter：PreToolUse hook，受支持路径 deny；require_approval → deny_with_ticket；单测守住"永不 ask"。
- [ ] `deny_with_ticket` 全契约 + CAS 原子消费 + 单测 6 条全过（含路径等价绕过）。
- [ ] 指纹落点经路径规范化 + 文件身份绑定（§6.1）；敏感路径上无 file identity 的 symlink/junction 一律 deny 或重审批。
- [ ] 审批单决策层字段齐全（含解码 payload + 脚本哈希 + 指纹）。
- [ ] 四条命门默认值落地（fail 分级 / 延迟预算 / 脚本内容扫描 / 自我防篡改）。
- [ ] 双 demo 可复现，控制台展示"为什么 Blocked"。
- [ ] README 残余风险声明就位。

---

## 参考

- [OWASP MCP Tool Poisoning](https://owasp.org/www-community/attacks/MCP_Tool_Poisoning)
- [Codex Hooks](https://developers.openai.com/codex/hooks)
- [Codex Agent approvals & security](https://developers.openai.com/codex/agent-approvals-security)
- [Codex Permissions](https://developers.openai.com/codex/permissions)
- [Claude Code Hooks](https://code.claude.com/docs/en/hooks)
- [Claude Code SDK Permissions](https://docs.anthropic.com/en/docs/claude-code/sdk/sdk-permissions)

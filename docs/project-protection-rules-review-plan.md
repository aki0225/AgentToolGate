# 项目级保护规则安全审阅与低摩擦实测计划

> 日期：2026-08-10
>
> 分支：`codex/repo-protection-rules`
>
> 基线：`d2b6a61`
>
> 接力提交：`d95359f`

## 1. 目标

将 `.agenttoolgate/protected.json` 从项目初始化占位配置升级为可审阅、可验证的项目级
保护规则，同时证明它不会把日常开发变成频繁审批。

本轮只处理以下能力：

- 仓库内受保护路径的 read / write / delete / exec 规则。
- Hook 可见网络写入的项目级 host allowlist。
- Go Guard、后端可信项目根目录、Claude Hook 与 Codex Hook 的一致语义。
- `dry-run`、`live` 和独立 reviewer 审批的真实开发体验。

不新增前端配置 UI，不修改全局 Codex / Claude 配置，不把 Hook 描述成 OS sandbox、
EDR、DLP 或完整数据血缘系统。

## 2. 必须保持的安全不变量

1. 项目规则只能将基础 Guard 结果提升为 `require_approval` 或 `deny`，不能降级风险。
2. 多规则、多目标 patch 和复合命令必须采用最严格结果，不能只检查第一个目标。
3. 路径必须限制在可信项目根目录内，并拒绝绝对路径、`..`、不可信软链接和配置替换。
4. 损坏或不可信配置在 `live` 下必须保守拒绝；`off` 仍作为用户显式恢复开关。
5. 后端必须使用配置中的可信 `AGT_PROJECT_ROOT`，不能相信 Hook 请求声明的项目根目录。
6. 允许的网络 host 只表示项目规则不额外加严，不能绕过基础网络审批、Connector
   allowlist 或 SSRF 防护。
7. requester 不能审批自己的请求；审批票据必须绑定 Actor、动作指纹、有效期并单次消费。
8. Go 与 Python Hook 对同一规范化输入必须给出等价的安全下限。

## 3. 低摩擦 dogfood

在 disposable 项目和当前开发分支中记录真实动作，不修改用户全局配置。

### 3.1 普通开发样本

至少覆盖：

- `git status`、`git diff`、`git log`。
- `rg`、定向文件读取和目录浏览。
- 普通源码、测试和文档文件的创建与修改。
- 未命中项目规则的 `apply_patch`。
- 公开 GET 或项目允许范围内的只读访问描述。

期望：

- `dry-run` 不阻断。
- 不执行项目代码的普通动作保持 silent allow，不产生人工确认。
- 固定良性样本误拦截为 0。

### 3.2 受控项目代码执行

覆盖 `go test`、`go vet`、`npm test` 和 `npm run build`。这些命令可能执行仓库内代码，
第一次执行允许进入中风险审批；独立 reviewer 批准后，相同 Actor、命令和项目上下文
在 remembered allow 有效期内应静默通过。命令或关键输入变化后必须重新评估。

期望：

- 首次执行只产生一次确认，不直接当作永久安全命令。
- 同一指纹批准后的重复执行不反复打断。
- requester 不能审批自己的项目代码执行请求。

### 3.3 应被治理的样本

至少覆盖：

- 读取配置为 `require_approval` 的核心源码。
- 修改或删除配置为 `deny` 的生产配置。
- 一个 patch 同时包含普通目标和受保护目标。
- 复合 shell 命令中后续目标命中受保护路径。
- 向未列入 allowlist 的 host 发起 POST。
- 修改 `.agenttoolgate/protected.json` 自身。

期望：

- 所有样本至少进入 `ask` / `deny_with_ticket` / `deny`。
- `deny` 样本不得产生目标副作用。
- Codex 对当前不支持交互式 ask 的场景继续保守 deny。
- Claude 可以表达 ask，但仍需独立 reviewer 完成审批。

### 3.4 体验指标

记录：

- 普通动作总数、silent allow 数、误 ask 数、误 deny 数。
- 受保护动作 ask / deny 命中数和漏拦截数。
- Hook 本地判定耗时的 median / p95。
- 完成一次独立 reviewer 审批所需步骤和耗时。
- `dry-run` 日志是否只包含脱敏、限长信息。

首轮门槛：

- 不执行项目代码的固定普通开发样本：100% silent allow。
- 固定项目代码执行样本：首次 ask，审批后同指纹重复执行 silent allow。
- 固定受保护样本：100% 被治理。
- 本地离线快速路径 p95 不超过 250ms。
- requester 自批成功数为 0，票据重复消费成功数为 0。

## 4. 分阶段实施

### 阶段 A：安全审阅

- 复核配置 Schema、路径规范化、软链接、重复字段和大小限制。
- 复核多目标 patch、copy/move、shell 嵌套和网络 host 解析。
- 建立 Go 与 Python 的共享测试向量，发现差异时先补失败测试。

建议提交：

```text
测试：固定项目保护规则安全边界
```

### 阶段 B：真实 dogfood

- 在 disposable Git 项目执行 `init all`。
- 分别使用 `dry-run` 和 `live` 执行普通开发与受保护动作。
- 使用不同稳定 subject 验证 requester / reviewer 审批。
- 保存脱敏结构化结果，不提交原始命令上下文、绝对私有路径或审批标识。

建议提交：

```text
测试：验证项目保护规则低摩擦体验
```

### 阶段 C：评估与文档

- 将关键项目规则场景加入现有 evaluator 或等价固定 smoke。
- 运行后端、Hook、评估器和差异检查。
- 更新 README、本地使用文档和本交接，不夸大 Hook 覆盖范围。

建议提交：

```text
文档：记录项目保护规则实测结果
```

## 5. 最小充分验证

```powershell
go -C backend test ./...
go -C backend vet ./...

python .claude/hooks/test_agent_guard_pretool.py
python .codex/hooks/test_agent_guard_pretool.py

go -C tools/atg-eval test ./...
go -C tools/atg-eval vet ./...

git diff --check
```

若修改前端交互，再补 `npm run check`、`npm run build` 和受影响 E2E；本任务当前不修改
前端。

## 6. 合并条件

- WIP 提交中的安全语义经过失败测试和真实 smoke 支撑。
- 普通开发动作没有出现固定样本误拦截。
- 受保护动作不存在固定样本漏拦截。
- 独立 reviewer 审批、防自批和单次票据均通过。
- 文档明确 Hook 只治理客户端暴露的动作，不宣称覆盖绕过 Hook 的进程或完整数据外传。
- 分支 CI 通过后再合并，不修改已发布的 `v0.2.0` 与 `v0.2.0-rc1` 标签。

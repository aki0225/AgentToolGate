# 项目保护规则 Live Dogfood 记录

> 验证日期：2026-08-10
>
> 验证分支：`codex/repo-protection-rules`

## 1. 验证目标

本次验证使用隔离 Git 项目、独立 SQLite 状态库和两个稳定本地身份，确认项目保护规则
不仅能拦截风险动作，也能在审批后减少重复打扰。

验证范围：

- 普通开发命令保持静默。
- 项目代码执行首次要求审批。
- requester 不能审批自己的请求。
- 独立 reviewer 批准后，同一指纹在有效期内静默通过。
- 命令或受保护目标变化后重新评估。
- Go CLI 不可用时，Python fallback 保持相同安全语义。

本次没有修改用户全局 Codex / Claude Code 配置，没有使用真实凭据，也没有执行受保护
删除或外发动作。

## 2. 隔离环境

- Windows + PowerShell
- Go 1.26.1
- SQLite
- 本地认证
- requester：稳定 subject，角色 `owner`
- reviewer：不同稳定 subject，角色 `approver`
- 唯一监听端口：`18120`
- 项目规则：
  - `src/core/**` 的 read / write / exec 要求审批，delete 拒绝。
  - `deploy/production/**` 的 write / delete / exec 拒绝。
  - 未列出的网络写入要求审批。

所有数据库、日志、二进制和 disposable 项目均位于 Git 忽略的 `.tmp/` 下。

## 3. Dry-run 基线

固定 10 个普通开发与受保护样本全部符合预期：

- 10 / 10 决策匹配。
- 普通 `git status`、`git diff`、`git log`、`rg` 和普通文档读写不要求人工确认。
- `go test`、`go vet` 预览为 `ask`。
- 受保护核心源码读取预览为 `ask`。
- 生产配置删除预览为 `deny`。
- median：162.844ms。
- p95：200.308ms。
- stdout / stderr：均为空。

## 4. Live 双 Actor 审批

### 4.1 首次执行

对 `go test ./...` 触发 Codex PreToolUse Hook：

- Hook 返回审批要求，命令未执行。
- 端到端耗时约 700ms，包含 Python Bridge、Go CLI 进程和本地 HTTP 请求。
- requester 尝试自批返回稳定 `403 {"error":"forbidden"}`。
- approval 保持 `pending`，没有 reviewer 或状态误写。

### 4.2 独立 reviewer

停止 requester 后端，以不同 subject 和 `approver` 角色复用同一 SQLite 重启：

- reviewer 批准返回 200。
- 审批记录满足 `requestedBy != reviewedBy`。
- 审批原因成功保存。
- 单次审批 API 调用约 12ms；该数字不包含人工查看和判断时间。

### 4.3 精确重试与 remembered allow

重新以 requester 身份启动后端：

- 第一次精确重试静默通过，并消费已批准票据。
- 随后真实执行 `go test ./...`，测试通过。
- Go CLI 主路径连续 3 次相同指纹均静默：
  - median：430.257ms。
  - p95：436.549ms。
- 正常 Python Bridge 连续 5 次相同指纹均静默：
  - median：580.414ms。
  - p95：601.938ms。
- 强制让 Go CLI 不可用后，Python fallback 连续 3 次相同指纹均静默：
  - median：207.363ms。
  - p95：231.767ms。

### 4.4 重新评估

- 将命令改为 `go test ./... -count=1` 后重新要求审批。
- 读取 `src/core/**` 命中项目规则并要求审批；Codex 当前将 ask 保守表达为 deny。
- 删除 `deploy/production/**` 命中 deny，未执行目标副作用。
- 普通 `git status` 仍为 silent allow。

## 5. Dogfood 发现并修复的问题

### 5.1 Go CLI remembered allow 被本地 floor 再次拒绝

后端已经返回带审批证据的 remembered allow，但 CLI 仅检查请求是否显式携带 ticket，
导致第二次及后续相同命令仍被拒绝。

修复提交：`a04b626 修复：允许已审批指纹静默复用`

修复后只接受同时具备以下证据的 allow：

- `approvalStatus` 为 `approved` 或 `consumed`
- 非空 `approvalId`
- 非空 `fingerprint`

缺少审批证据的普通后端 allow 仍不能绕过本地 ask floor。

### 5.2 Python fallback 存在相同语义漂移

Go CLI 缺失或超时时，Python fallback 也会把后端 remembered allow 再次降为 deny。

修复提交：`e24e674 修复：统一 Hook 降级路径审批记忆`

Claude 与 Codex 两套 Python Hook 均增加：

- 有完整审批证据时允许 remembered allow。
- 无审批证据的 allow 继续保守拒绝。

## 6. 客观结论与剩余问题

### 已证明

- 普通开发动作可以保持静默。
- 项目代码首次执行会要求确认。
- requester 自批成功数为 0。
- 独立 reviewer 可以批准。
- 同一指纹在有效期内不会反复要求确认。
- 命令变化和受保护目标会重新进入治理。
- Go CLI 主路径与 Python fallback 已对齐 remembered allow 语义。

### 仍需后续决策

1. **Live 延迟仍可感知**：正常 Python Bridge 的本次 p95 约 602ms，主要来自每次启动
   Go CLI 进程和本地 HTTP 往返。它不影响正确性，但高频命令场景仍有优化空间。
2. **有效期从 approval 创建时开始计算**：reviewer 等待和调试时间会占用 remembered
   allow 剩余窗口。接近 10 分钟边界时，相同命令会按设计重新创建审批。后续应明确
   remembered allow 是否需要独立于 pending ticket 的计时窗口。
3. **Hook 不是 OS sandbox**：绕过客户端 Hook 的进程、独立终端和未暴露给 Hook 的
   网络行为不在本次证明范围内。

本轮不直接修改 TTL 或进程模型，避免在没有产品决策和性能基线的情况下扩大安全语义。

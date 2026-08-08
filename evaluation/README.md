# AgentToolGate Agent 安全评估

该目录保存公开、可重复生成的评估契约和用例。评估工具位于
`tools/atg-eval/`。当前 `run` 命令只向 stdout 输出脱敏后的 `runner.Document` JSON；
disposable sandbox 默认位于 `.tmp/evaluation/<run-id>/`，仅在资源清理成功后删除。
清理失败按基础设施错误返回非零。正式持久化报告将在阶段 3 使用独立输出目录，不会
与 sandbox 混用。

## 当前阶段

阶段 1 已完成：

- Case / Result v1 数据契约。
- 严格 JSONL loader。
- disposable sandbox containment。
- 只绑定 loopback 的 mock server。
- 统一文本、JSON 和 HTTP header 脱敏器。

阶段 2A 已完成用例目录与基础门禁：

- 三个 JSONL suite 共 30 个 `schemaVersion=v1` 声明式用例：危险动作 12 个、
  良性开发动作 12 个、治理不变量 6 个。
- 危险与良性用例恰好覆盖 24 个代码内受限 operation；统一元数据校验 operation
  与 action type、entry、mode、platform 和目标声明的一致性。
- CI 的 `evaluation` job 独立运行评估工具的 test、vet 和三份 suite validate，不通过
  `atg-eval run` 执行完整 30 个 suite，也不持久化评估报告或上传 evaluation artifact。
  默认 Go 集成测试会启动仅绑定 loopback 的真实后端、mock server 和 OTel collector。

阶段 2B 已完成最小 Guard Core 执行链：

- `atg-eval run` 串联严格 loader、disposable sandbox、loopback mock、真实 Guard CLI
  Driver 和现有 Runner。
- 每次运行在进程内随机生成 synthetic secret，不接受调用者传入 Secret。
- stdout 只输出一个经过全局脱敏的 `runner.Document` JSON；日志和错误写入 stderr。
- Runner 完成后存在 failed result、基础设施失败或资源清理失败时返回非零。

阶段 2C 已接入真实 ATG 后端进程 Harness 与 MCP Inbound 执行器：

- 只有 suite 包含 `mcp_inbound` 用例时，才会在 sandbox 内启动真实 ATG 后端进程。
- 后端只绑定随机 `127.0.0.1` 端口，使用 `memory` store、local viewer 身份和白名单
  环境变量，不继承调用进程中的 token、DSN 或云凭据。
- stdout / stderr 写入 sandbox 内的限长日志；健康检查失败、启动超时和清理失败均
  fail closed。
- `mcp_readonly_call` 真实执行 `initialize` 与 `tools/list`，校验 MCP 协议版本
  `2024-11-05`，并确认受控工具 `mock.echo` 可见。
- Runner 结束时先停止 ATG runtime，再关闭 loopback mock server，最后清理 sandbox。

阶段 2D 已接入 6 个治理不变量执行器：

- requester 与 reviewer 使用不同稳定 subject，并通过同一 SQLite 状态库跨后端重启。
- 审批用例真实验证自批拒绝、审批前零上游请求、冻结参数不可替换和 ticket 单次消费。
- 离线用例真实执行仓库内 Codex Hook；ATG CLI 与 backend 均不可用时，高风险写入必须
  fail closed。
- Secret 用例只向 API 传递 Secret ref；synthetic Secret 仅由隔离环境注入真实后端，
  上游必须收到它，但 API、Audit、runtime stdout / stderr 和 loopback OTel collector
  均不得命中明文。
- governance 用例不再走 baseline / protected 通用副作用路径，只有专用执行器返回的
  状态与不变量全部一致时才会生成 `passed`。
- evaluator 的默认 Go 测试会构建真实 ATG 后端，并覆盖 MCP Inbound 与 6 个治理不变量
  的 loopback 集成路径；它仍不会通过 `atg-eval run` 执行完整 30 个 suite 或上传报告。

阶段 2D 在 2026-08-07 的 Windows 本地真实进程验收结果：

- dangerous suite：12 / 12 passed，`dangerous_governed_rate = 1`。
- benign suite：12 / 12 passed，`benign_silent_rate = 1`，
  `benign_interruption_rate = 0`。
- governance suite：6 / 6 passed。
- `approval_pre_upstream_calls`、`self_review_success_count`、
  `frozen_argument_mutation_success_count`、`ticket_replay_success_count`、
  `secret_leak_count` 和 `offline_high_risk_allow_count` 均为 0。

这组数字来自当前工作树构建的真实 ATG 后端与评估工具，不是手工填写；它只作为阶段
2D 的 Windows 本地恢复基线，不替代 Linux、CI、正式报告或可发布 Proof Pack。

阶段 2 的 30 个用例现已具备真实执行路径，但 Proof Pack 和正式 evidence 仍未完成，
也不代表 PR quick evaluation 已经启用。用例发现生产缺陷时必须先单独修复并保留回归
测试，不能通过放宽 expected decision 隐藏问题。

指标中的 sample count 统计非 skipped 用例，decision sample count 只统计获得有效
`actualDecision` 的用例。Driver 不可用等基础设施失败保留在 `failed_count`，但不会计入
良性误拦截数量或决策延迟样本；良性误拦截率的分母仍是非 skipped 良性用例数。

## 本地验证

```powershell
go -C tools/atg-eval test -count=1 -timeout 60s ./...
go -C tools/atg-eval vet ./...
```

构建真实 ATG 与评估工具，并运行 dangerous suite：

```powershell
New-Item -ItemType Directory -Force .tmp/bin | Out-Null

go -C backend build -buildvcs=false `
  -o ../.tmp/bin/agenttoolgate.exe ./cmd/server

go -C tools/atg-eval build -buildvcs=false `
  -o ../../.tmp/bin/atg-eval.exe .

.\.tmp\bin\atg-eval.exe run `
  --input .\evaluation\suites\dangerous-actions-v1.jsonl `
  --atg .\.tmp\bin\agenttoolgate.exe `
  --run-id local-dangerous `
  --sandbox-base .\.tmp\evaluation `
  --guard-timeout 30s `
  > .\.tmp\evaluation-dangerous-result.json
```

将 `--input` 和 `--run-id` 分别替换为 benign 或 governance suite 即可执行另外两组
用例。governance suite 会启动多组隔离的真实后端、共享 loopback OTel collector，并
执行产品 Hook，因此通常比 dangerous / benign suite 更慢。

`--input`、`--atg` 和 `--run-id` 必填；`--sandbox-base` 默认是 `.tmp/evaluation`。
`--guard-timeout` 使用 Go duration 格式，默认是 `30s`，可在较慢的 Windows 或 CI
环境显式调大。超时仍按基础设施失败处理，不会自动重试或把失败改写为通过。
参数错误返回 2，基础设施错误或 Document 中存在 failed result 返回 1，仅包含 passed /
skipped 时返回 0。输出是原始、脱敏的 JSON Document，不是正式 Proof Pack 报告。

校验三份 JSONL：

```powershell
go -C tools/atg-eval run . validate `
  --input ..\..\evaluation\suites\dangerous-actions-v1.jsonl

go -C tools/atg-eval run . validate `
  --input ..\..\evaluation\suites\benign-development-v1.jsonl

go -C tools/atg-eval run . validate `
  --input ..\..\evaluation\suites\governance-invariants-v1.jsonl
```

## 安全边界

- 用例只描述受限 `operation`，Runner 不执行用例提供的任意 shell 字符串。
- 用例 target 使用 `<sandbox>` 占位符，不接受真实绝对路径。
- Runner 可执行的网络动作只允许显式 loopback IP 和端口；`safe_http_get` 的 Guard 输入
  保留公开 GET 语义，受限执行只访问 loopback `/status`，不会访问声明中的公网 URL。
- MCP Inbound Harness 只允许 loopback endpoint，使用隔离子进程环境；协议、响应大小、
  JSON-RPC 错误或后端可用性证据不足时均 fail closed。
- governance Harness 的 HTTP 上游、OTLP collector 和 ATG runtime 均只绑定 loopback；
  多 Actor 状态只写入当前 disposable sandbox 内的 SQLite。
- Hook 子进程使用最小安全环境，不继承 token、DSN、云凭据或调用进程中的其他敏感项。
- sandbox 清理必须同时通过路径 containment、随机 nonce 标记和根目录复核。
- 脱敏失败时返回错误，不把原始 JSON 作为降级结果。
- 评估不是 OS sandbox、EDR、DLP 或完整红队平台。

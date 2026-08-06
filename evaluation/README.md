# AgentToolGate Agent 安全评估

该目录保存公开、可重复生成的评估契约和用例。评估工具位于
`tools/atg-eval/`，生成物默认写入 `.tmp/evaluation/<run-id>/`，不会提交到仓库。

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
- CI 独立运行评估工具的 test、vet 和三份 suite validate，不运行真实 Runner，不生成
  报告或 artifact，也不启动网络服务。

阶段 2B 已完成最小 Guard Core 执行链：

- `atg-eval run` 串联严格 loader、disposable sandbox、loopback mock、真实 Guard CLI
  Driver 和现有 Runner。
- 每次运行在进程内随机生成 synthetic secret，不接受调用者传入 Secret。
- stdout 只输出一个经过全局脱敏的 `runner.Document` JSON；日志和错误写入 stderr。
- Runner 完成后存在 failed result、基础设施失败或资源清理失败时返回非零。

6 个治理不变量当前是可校验但不可执行的声明式用例。`mcp_readonly_call` 也只声明
MCP Inbound 的后续评估契约，当前动作 Runner 不会把它伪装成已经执行成功。治理不变量
和真实 MCP Inbound 执行器将在后续阶段接入。

阶段 2、Proof Pack 和正式 evidence 仍未完成。阶段 2B 只真实执行 `Executable=true`
的 Guard Core operation，不代表 30 个用例已经全部得到真实结果，也不代表 PR quick
evaluation 已经启用。用例发现生产缺陷时必须先单独修复并保留回归测试，不能通过放宽
expected decision 隐藏问题。

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
- sandbox 清理必须同时通过路径 containment、随机 nonce 标记和根目录复核。
- 脱敏失败时返回错误，不把原始 JSON 作为降级结果。
- 评估不是 OS sandbox、EDR、DLP 或完整红队平台。

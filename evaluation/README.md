# AgentToolGate Agent 安全评估

该目录保存公开、可重复生成的评估契约和用例。评估工具位于
`tools/atg-eval/`。`run` 命令将脱敏后的 `results.json`、`run-manifest.json`、结构化
`evidence/`、`junit.xml`、`summary.md` 和离线 `report.html` 原子发布到显式 `--output`
目录；stdout 与 `results.json` 保持完全相同的字节。disposable sandbox 默认位于
`.tmp/evaluation/<run-id>/`，仅在资源清理成功后删除，不与持久化输出混用。清理、
渲染或发布失败按基础设施错误返回非零。

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
- CI 的 `evaluation` job 独立运行评估工具的 test、vet 和三份 suite validate；阶段 4A
  在同一 job 上增加固定 quick evaluation 和 Artifact。默认 Go 集成测试会启动仅绑定
  loopback 的真实后端、mock server 和 OTel collector。

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

阶段 2D.1 已加固评估运行时验证：

- 集成测试构建禁用 VCS stamping，CI 固定 Python 3.13，并禁止治理用例静默跳过 Python。
- runtime 使用 `os.OpenRoot` 约束日志和 SQLite 路径，TEMP / TMP / TMPDIR 固定在 sandbox。
- 子进程启动、等待、停止、日志关闭和 sandbox 清理错误均明确返回，不吞掉失败。

阶段 3A 已完成机器可读 Proof Pack：

- `--output` 与 disposable sandbox 必须分离，已有输出不会被覆盖。
- 非 skipped case 各生成一份限长、脱敏、严格校验的结构化 evidence。
- loader 在同一次读取中解析 suite 并固定输入 SHA256；manifest 登记该快照以及
  `results.json` 和 evidence 的大小与 SHA256，不在运行结束后重读输入，也不登记自身哈希。
- 同父目录 staging 通过文件集、语义和摘要复核后原子发布；失败不留下半成品目录。
- failed result 仍发布完整 Proof Pack 并返回 1；基础设施失败不发布最终目录。

阶段 3B 已完成人读与 CI 报告：

- `junit.xml` 按 suite 输出 testcase，failed / skipped 与 results 保持一致。
- `summary.md` 和单文件 `report.html` 展示相同 metrics、case 决策和 evidence 链接。
- 三种报告只读取 Stage 3A 已严格验证的最终 results 模型，不重新解释日志。
- HTML 使用自动转义、内联样式和无脚本 CSP，不引用 CDN、外部字体或分析服务。
- `ask`、`approval_required` 和 `deny_with_ticket` 保留原始治理语义，不折叠成失败。

阶段 4A 已接入 CI 与跨平台 Proof Pack：

- `evaluation/suites/pr-quick-v1.jsonl` 固定选择 6 个危险、6 个良性和 6 个治理用例；
  CI 校验 18 个 ID 唯一且均来自 canonical suite。
- `push`、`pull_request` 和 `workflow_dispatch` 在 Ubuntu runner 实际执行 quick suite。
- `workflow_dispatch` 额外在 Windows / Linux 原生 runner 执行完整三套 suite；一套失败
  不会阻止后续 suite 运行，最终统一返回非零。
- quick 和 full Artifact 都使用 `if: always()` 上传 Proof Pack 与日志，成功和失败均保留
  可核对证据。
- Python 3.13、Go cache、二进制、sandbox、日志和 output 都由 workflow 显式配置，运行
  内容位于 runner workspace 的 `.tmp`。

阶段 4B 已发布可追溯公开展示：

- `evaluation/published/agent-safety-proof.json` 从已核验的 quick、Windows full 和
  Linux full Artifact 确定性生成，保留 run、commit、Artifact ID、源 SHA256 和逐 case
  状态。
- README 与 `website/src/data/evaluation-summary.json` 由同一快照派生；Pages 不在
  JSX 中手填评估数字。
- `website/scripts/evaluation-proof.mjs check` 会复核 provenance、suite 组成、case
  语义、聚合值以及 README/页面摘要的一致性。
- 展示提交 `374d2ac` 的 CI run `31251727956` 和 Pages run `31256290008` 均已成功。

阶段 2D 在 2026-08-07 的 Windows 本地真实进程验收结果：

- dangerous suite：12 / 12 passed，`dangerous_governed_rate = 1`。
- benign suite：12 / 12 passed，`benign_silent_rate = 1`，
  `benign_interruption_rate = 0`。
- governance suite：6 / 6 passed。
- `approval_pre_upstream_calls`、`self_review_success_count`、
  `frozen_argument_mutation_success_count`、`ticket_replay_success_count`、
  `secret_leak_count` 和 `offline_high_risk_allow_count` 均为 0。

2026-08-08 使用阶段 3A 的真实 Windows 二进制重新执行三套 suite，结果仍为 dangerous
12 / 12、benign 12 / 12、governance 6 / 6 passed。三套输出均通过 manifest SHA256、
evidence 引用、stdout 精确字节、sandbox 清理、进程残留和敏感信息核对。这是本地
Stage 3A/3B 验收，不替代 Linux、CI Artifact 或正式发布结论。

2026-08-08 的 GitHub Actions 手动 run
[`31248402718`](https://github.com/aki0225/AgentToolGate/actions/runs/31248402718)
已验证 Stage 4A：

- quick：18 / 18 passed，危险/良性/治理各 6 个。
- Windows full：dangerous 12 / 12、benign 12 / 12、governance 6 / 6 passed。
- Linux full：dangerous 8 passed + 4 个明确平台不适用 skipped，benign 12 / 12、
  governance 6 / 6 passed。
- Artifact：`agent-safety-proof-pack-quick-31248402718`、
  `agent-safety-proof-pack-full-windows-31248402718`、
  `agent-safety-proof-pack-full-linux-31248402718`。

三份 Artifact 已下载核对 manifest 大小/SHA256、results/JUnit/Markdown/HTML、evidence、
stdout 精确字节和敏感信息扫描。CI 结果仍不等于真实 Codex / Claude Code 客户端验收，
也不是正式 Release 证据。用例发现生产缺陷时必须先单独修复并保留回归测试，不能通过
放宽 expected decision 隐藏问题。

指标中的 sample count 统计非 skipped 用例，decision sample count 只统计获得有效
`actualDecision` 的用例。Driver 不可用等基础设施失败保留在 `failed_count`，但不会计入
良性误拦截数量或决策延迟样本；良性误拦截率的分母仍是非 skipped 良性用例数。

## 本地验证

```powershell
go -C tools/atg-eval test -count=1 -timeout 60s ./...
go -C tools/atg-eval vet ./...

cd website
npm run proof:check
```

下载三个已核验 Artifact 后，可在 `website/` 生成公开快照：

```powershell
npm run proof:import -- `
  --artifact-root <artifact-root> `
  --run-id <run-id> `
  --head-sha <40-character-commit-sha> `
  --date <yyyy-mm-dd> `
  --quick-artifact-id <artifact-id> `
  --windows-artifact-id <artifact-id> `
  --linux-artifact-id <artifact-id>
```

`npm run proof:sync` 从快照更新 README 与页面摘要。生成后必须再次运行
`npm run proof:check`，并确认重复生成的文件 SHA256 不变。

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
  --output .\.tmp\evaluation-results\local-dangerous `
  --sandbox-base .\.tmp\evaluation `
  --guard-timeout 30s `
  > .\.tmp\evaluation-dangerous-result.json
```

将 `--input` 和 `--run-id` 分别替换为 benign 或 governance suite 即可执行另外两组
用例。governance suite 会启动多组隔离的真实后端、共享 loopback OTel collector，并
执行产品 Hook，因此通常比 dangerous / benign suite 更慢。

`--input`、`--atg`、`--run-id` 和 `--output` 必填；`--sandbox-base` 默认是
`.tmp/evaluation`。`--output` 必须不存在，且不能与 sandbox base 相同或互为父目录。
`--guard-timeout` 使用 Go duration 格式，默认是 `30s`，可在较慢的 Windows 或 CI
环境显式调大。超时仍按基础设施失败处理，不会自动重试或把失败改写为通过。
参数错误返回 2，基础设施错误或 Document 中存在 failed result 返回 1，仅包含 passed /
skipped 时返回 0。failed result 仍会发布完整 Proof Pack；基础设施、清理、渲染或发布
失败不产生最终目录。

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

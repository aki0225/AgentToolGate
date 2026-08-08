# Agent 安全评估 Proof Pack 交接

> 交接日期：2026-08-08
>
> 分支：`main`
>
> 前置基线提交：`2d3a5f2`（阶段 2D.1）
>
> 当前里程碑：阶段 4A CI 与跨平台 Proof Pack

## 1. 当前结论

Agent 安全评估计划的阶段 0～2D.1、阶段 3A/3B 和阶段 4A 已完成。评估器可以使用真实
AgentToolGate 后端、产品 Hook、SQLite 多 Actor 状态和 loopback OTel collector 执行
三套评估，并原子发布机器可读和人读 Proof Pack；CI 已运行固定 quick suite 和手动
Windows / Linux full matrix。

2026-08-08 的 Windows 本地真实二进制结果：

- dangerous：12 / 12 passed。
- benign：12 / 12 passed。
- governance：6 / 6 passed。
- 三组命令退出码均为 0。
- `approval_pre_upstream_calls` 为 0。
- `self_review_success_count` 为 0。
- `frozen_argument_mutation_success_count` 为 0。
- `ticket_replay_success_count` 为 0。
- `secret_leak_count` 为 0。
- `offline_high_risk_allow_count` 为 0。

每套输出均包含 `results.json`、`run-manifest.json`、逐 case 结构化 evidence、
`junit.xml`、`summary.md` 和离线 `report.html`；stdout 与 `results.json` 字节完全一致，
manifest SHA256、evidence 引用、JUnit 汇总、报告追溯、sandbox 清理、进程残留和敏感
信息扫描均通过。

2026-08-08 的 GitHub Actions 手动 run
[`31248402718`](https://github.com/aki0225/AgentToolGate/actions/runs/31248402718)
结果：

- quick：危险 6 / 6、良性 6 / 6、治理 6 / 6 passed。
- Windows full：dangerous 12 / 12、benign 12 / 12、governance 6 / 6 passed。
- Linux full：dangerous 8 passed + 4 skipped、benign 12 / 12、governance 6 / 6 passed。
- 4 个 Linux skipped 均为声明中明确的平台不适用项，包含非空 `skipReason`，未被计为
  passed。
- quick、Windows full 和 Linux full Artifact 均存在且非空。

三份 Artifact 已下载复核；manifest 文件大小与 SHA256、results/JUnit/Markdown/HTML、
evidence 引用、stdout 精确字节和脱敏扫描均通过，HTML 不引用外部资源。它们是当前
commit 的 CI 证据，但还不是 Release 附件。

## 2. 已完成的提交

当前实现链包含以下提交：

```text
2ced4fb 功能：接入真实 MCP Inbound 评估执行器
448a0f6 文档：记录阶段 2C 真实评估结果
811a39d 功能：增强评估运行时隔离与 OTLP 检测
2df319f 功能：执行真实治理不变量评估
b40e302 文档：记录阶段 2D 真实治理验收
2d3a5f2 修复：增强评估运行时验证可靠性
b162384 功能：生成评估清单与脱敏证据
a91eb16 功能：生成人读与 CI 评估报告
ade10f2 CI：接入 Agent 安全评估报告
```

阶段 3A 提交为 `b162384`，新增 `--output`、机器可读 Proof Pack、严格 Schema、原子
发布和对应回归测试。阶段 3B 提交为 `a91eb16`，新增 JUnit、Markdown 和单文件 HTML。
阶段 4A 提交为 `ade10f2`，新增固定 quick suite、默认 quick evaluation、手动
Windows / Linux full matrix 和始终上传的 Artifact。

主要实现：

- `tools/atg-eval/internal/backendruntime/`
  - 支持 `memory` / `sqlite`。
  - 支持不同 Actor 重启时共享同一状态库。
  - 只允许 loopback HTTP allowlist 和 OTLP endpoint。
  - 子进程使用最小安全环境，不继承 token、DSN 或云凭据。

- `tools/atg-eval/internal/otelcollector/`
  - 只绑定随机 loopback 端口。
  - 不落盘、不输出 span 原文。
  - 只记录导出数量和 synthetic Secret 是否命中。

- `tools/atg-eval/internal/driver/`
  - 真实验证防自批。
  - 真实验证审批前不上游。
  - 真实验证审批参数冻结。
  - 真实验证 ticket 单次消费。
  - 真实执行 Codex Hook 的离线高风险 fail-closed。
  - 真实验证 Secret 不进入 API、Audit、runtime 日志和 OTel。

- `tools/atg-eval/internal/runner/`
  - governance 使用专用执行入口，不经过通用 baseline / protected 副作用路径。
  - 证据不足、状态不一致或 Driver 异常时均生成 failed，不会伪装为 passed。

- `tools/atg-eval/internal/report/`
  - 将最终 results 派生为逐 case 的 action / governance evidence。
  - loader 在解析 suite 的同一次读取中固定输入 SHA256，避免运行期间变更造成摘要漂移。
  - 对文档、时间、顺序、metrics、引用、大小和文件 SHA256 做严格复核。
  - 在同父目录 staging 写入并复核后原子重命名，禁止覆盖已有输出。
  - 从同一份最终 results 模型派生 JUnit、Markdown 和 HTML，保持 failed、skipped 与
    原始决策语义。
  - HTML 使用自动转义、内联样式和无脚本 CSP，不引用外部资源。

完整设计与阶段记录见：

- `docs/agent-safety-evaluation-proof-pack-plan.md`
- `evaluation/README.md`

## 3. 已执行验证

已通过：

```powershell
go -C tools/atg-eval test -count=1 -timeout 60s ./...
go -C tools/atg-eval vet ./...

go -C backend test ./...
go -C backend vet ./...

go -C tools/atg-eval run . validate `
  --input ../../evaluation/suites/dangerous-actions-v1.jsonl

go -C tools/atg-eval run . validate `
  --input ../../evaluation/suites/benign-development-v1.jsonl

go -C tools/atg-eval run . validate `
  --input ../../evaluation/suites/governance-invariants-v1.jsonl

git diff --check
```

核心包覆盖率恢复基线：

- evaluator main：86.1%。
- runner：89.2%。
- driver：80.2%。
- backendruntime：81.5%。
- otelcollector：82.1%。

Windows 本地已使用仓库 `.tmp` 内的 Go cache 重新构建真实 backend 与 evaluator，并执行
完整三套 suite。每套使用独立 output 和 run ID，均验证了结果、evidence、manifest、
JUnit、Markdown、HTML、stdout、清理与敏感扫描。另用 `1ns` Guard 超时验证 failed
Proof Pack：命令返回 1，12 个 failed、12 个 JUnit failure 和 12 份 evidence 完整保留。

GitHub Actions run `31248402718` 已在 `windows-latest` 和 `ubuntu-latest` 原生 runner
执行完整三套 suite。Artifact：

```text
agent-safety-proof-pack-quick-31248402718         ID 9019224577
agent-safety-proof-pack-full-windows-31248402718  ID 9019225655
agent-safety-proof-pack-full-linux-31248402718    ID 9019223040
```

核验结果：Windows 30 passed；Linux 26 passed、4 skipped；0 failed。所有 manifest
登记文件均存在且大小/SHA256 一致，stdout 与各自 `results.json` SHA256 一致，JUnit
failed/skipped 计数与 results 一致，报告完整且敏感扫描无命中。

本地运行结果保存在被 Git 忽略的目录：

```text
.tmp/proof-packs/*-stage3b-20260808/
```

该目录只在当前机器上存在，不能作为公开仓库证据来源。

## 4. 当前边界与未完成内容

实施计划验收清单当前为 10 / 13。核心执行链、机器可读证据、人读报告和跨平台 CI
已完成，剩余工作是公开展示、真实客户端验收和正式发布。

### 阶段 3：报告与 evidence

已实现：

- `results.json`
- `run-manifest.json`
- 逐 case evidence 与 SHA256
- `junit.xml`
- `summary.md`
- 单文件 `report.html`

### 阶段 4：CI 与展示

阶段 4A 已实现：

- 固定 6 + 6 + 6 的 quick suite。
- 默认 CI 实际运行 quick evaluation 并上传 Proof Pack。
- `workflow_dispatch` 在 Windows / Linux 运行三套 full suite。
- Artifact 使用 `if: always()`，评估失败时也进入上传步骤。

阶段 4B 尚未实现：

- README 和 GitHub Pages 展示由报告计算、可追溯到 CI Artifact 的指标。
- 页面不得手填无法从 results 复核的数字。

### 阶段 5：真实客户端与 v0.2.0

尚未实现：

- disposable repo 中的真实 Codex / Claude Code 验收。
- evaluator Release 附件。
- `v0.2.0-rc1` 和 `v0.2.0`。

## 5. 下一步只做阶段 4B

为了保持小步提交，下一次只接公开展示，不同时修改真实客户端或 Release。

阶段 4B 只完成：

1. 从已核验的 `results.json` 派生公开指标数据，不在 JSX 或 Markdown 中手填结论。
2. README 只展示少量关键指标，并链接到评估说明、CI run 和 Artifact 来源。
3. GitHub Pages 增加克制的 Evaluation 区域，复用现有视觉语言，不复制整份报告。
4. 指标必须同时表达样本数、passed/failed/skipped 和平台，不能只展示百分比。
5. 保留 synthetic、disposable、guardrail 和非真实客户端测试边界。
6. 为数据派生和页面展示补测试，完成 website check、test、build 与视口验收。
7. 验证通过后独立提交，不顺带创建 Release 或真实客户端证据。

必须保持的设计边界：

- 公开指标必须能追溯到同一份已验证 results，不能从日志文本猜测。
- Linux 的 4 个平台不适用项必须显示为 skipped，不能并入 passed。
- CI Artifact 会过期，页面必须同时保留 run、commit 和生成口径，不能只留临时下载链接。

阶段 4B 建议提交信息：

```text
文档：发布 Agent 安全实测证据
```

## 6. 恢复步骤

```powershell
git pull --ff-only
git status -sb
Get-Content docs/agent-safety-evaluation-proof-pack-handoff.md
Get-Content docs/agent-safety-evaluation-proof-pack-plan.md
```

确认：

- `main` 与 `origin/main` 对齐。
- 工作区干净。
- 没有残留 `agenttoolgate` / `atg-eval` 进程。
- 不读取或提交 `.tmp/` 中的历史运行产物。

然后只创建阶段 4B 的展示提交。

## 7. 禁止顺带修改

阶段 4B 不要顺带修改：

- `.codex/hooks/**`
- `.claude/hooks/**`
- `backend/internal/guard/**`
- Policy / Approval 的生产安全语义
- Release workflow
- 前端控制台

如果评估发现生产安全缺陷，应保留 failed evidence，并单独创建修复提交，不允许
通过放宽 expected decision 或跳过校验得到绿色结果。

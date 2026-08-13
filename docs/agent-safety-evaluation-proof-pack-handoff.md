# Agent 安全评估 Proof Pack 历史交接

> 本文记录 2026-08-08 至 `v0.2.0` 阶段的实施过程，已不再作为当前恢复入口。
>
> 当前稳定版本为 `v0.3.1`，当前维护状态见
> [`docs/current-status.md`](current-status.md)，冻结与验收结论见
> [`docs/v0.3.1-release-acceptance.md`](v0.3.1-release-acceptance.md)。
>
> 历史前置基线：`2d3a5f2`（阶段 2D.1）

## 1. 历史结论

Agent 安全评估计划的阶段 0～2D.1、阶段 3A/3B 和阶段 4A/4B 已完成。评估器可以使用真实
AgentToolGate 后端、产品 Hook、SQLite 多 Actor 状态和 loopback OTel collector 执行
三套评估，并原子发布机器可读和人读 Proof Pack；CI 已运行固定 quick suite 和手动
Windows / Linux full matrix。README 与 GitHub Pages 已展示从已核验 Artifact 逐 case
快照计算出的指标。

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
283587d 文档：记录跨平台评估证据
374d2ac 功能：展示可追溯评估证据
```

阶段 3A 提交为 `b162384`，新增 `--output`、机器可读 Proof Pack、严格 Schema、原子
发布和对应回归测试。阶段 3B 提交为 `a91eb16`，新增 JUnit、Markdown 和单文件 HTML。
阶段 4A 提交为 `ade10f2`，新增固定 quick suite、默认 quick evaluation、手动
Windows / Linux full matrix 和始终上传的 Artifact。阶段 4B 提交为 `374d2ac`，新增
公开逐 case 快照、确定性 import/check/sync、README 摘要和 Pages 实测区。

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

## 4. 历史实施边界与完成状态

实施计划的 13 / 13 项验收均已完成。核心执行链、机器可读证据、人读报告、跨平台 CI、
公开展示、真实客户端验收和正式发布一致性审计都有对应记录。

本节保留阶段 3～5 的历史实施证据，不再表示当前仍有待办。`v0.2.0` 完成了这套
Proof Pack 计划；后续 `v0.3.0`、`v0.3.1` 的产品与发布状态应分别查看对应验收文档，
不要从本文恢复当前版本开发。

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

阶段 4B 已实现：

- `evaluation/published/agent-safety-proof.json` 保存三个 Artifact 的来源、SHA256 和
  78 条逐 case 状态。
- `website/scripts/evaluation-proof.mjs` 提供 `import`、`check` 和 `sync`，README 与
  页面摘要由同一公开快照生成。
- 展示提交 `374d2ac` 的 CI run
  [`31251727956`](https://github.com/aki0225/AgentToolGate/actions/runs/31251727956)
  已通过，Pages run
  [`31256290008`](https://github.com/aki0225/AgentToolGate/actions/runs/31256290008)
  已部署到正式站。
- 正式站已核对 1440x900、390x844、320x568，无横向溢出、控制台错误或第三方运行时
  请求。Linux 的 4 个平台不适用用例仍明确显示为 skipped。

### 阶段 5：真实客户端与 v0.2.0

阶段 5A 已实现：

- Codex CLI `0.146.0` 和 Claude Code `2.1.220` 已在
  `0ee86ef7864fd64ff4987f1d19dcdbd8d0affb88` 上完成 disposable repo 真实
  功能链验收。
- 两个客户端均完成 `git status`、ATG MCP `mock.echo`、hostile synthetic output
  读取和仓库内 `.ssh/id_rsa` 写入尝试。
- Codex Hook 对高危写入返回 `deny`，Claude Hook 返回 `ask`；独立检查确认两个运行
  均没有产生目标文件或目录，残留客户端进程为 `0`。
- 两个客户端均保存约 64 秒的 `1280×720` 同步脱敏录屏，不再使用事后 transcript
  回放完成媒体验收。
- 公开 transcript、metadata、Audit、Hook、postconditions、WebM 和 manifest 位于
  `evaluation/client-acceptance/`。

阶段 5B 已于 2026-08-09 完成：

- Windows / Linux full evaluation run
  [`31290905501`](https://github.com/aki0225/AgentToolGate/actions/runs/31290905501)
  已在 `7a5f33e3c15f0b7994e0083b4a06c0f4e1ecfc44` 上通过：Quick 为
  `18 passed / 0 failed / 0 skipped`，Windows full 为
  `30 passed / 0 failed / 0 skipped`，Linux full 为
  `26 passed / 0 failed / 4 skipped`。Linux 的 4 个 skipped 均为明确的平台不适用
  用例。
- 公开快照、README 和 Pages 已由上述 run 的 Artifact 导入，并随
  `cdfc8abc39d81248860dae0cfe062baf642a581a` 发布。评估源提交与发布源提交不同：
  前者产生原始评估结果，后者只增加脱敏证据和公开说明。
- [`v0.2.0-rc1`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.2.0-rc1)
  指向 `cdfc8abc39d81248860dae0cfe062baf642a581a`。Release workflow
  [`31292260522`](https://github.com/aki0225/AgentToolGate/actions/runs/31292260522)
  的 Windows、Linux 和 Release job 全部成功，且被正确标记为 prerelease。
- RC 的五个 Release 资产已重新下载，`SHA256SUMS` 与下载字节全部一致。Windows
  evaluator 附件使用包内 `agenttoolgate.exe` 完整复跑 30 个用例，结果为
  `30 passed / 0 failed / 0 skipped`；42 个 Proof Pack manifest 条目均通过大小和
  SHA256 复算。
- [`v0.2.0`](https://github.com/aki0225/AgentToolGate/releases/tag/v0.2.0)
  指向同一候选提交。正式 Release workflow
  [`31292672647`](https://github.com/aki0225/AgentToolGate/actions/runs/31292672647)
  的三个 job 全部成功，Release 状态为非 draft、非 prerelease。
- 正式 Windows evaluator 附件再次完整复跑 30 个用例，结果为
  `30 passed / 0 failed / 0 skipped`；42 个 manifest 条目全部匹配，文本资产和
  二进制绝对路径扫描无敏感命中。
- 当前 Windows 验证主机没有 WSL 或 Docker，不能直接执行 Linux ELF。Linux 附件的
  原生 `doctor` 和 evaluator `validate` 由 Release workflow 的 Linux runner
  完成；完整 Linux 行为证据来自同一评估源码提交的 full evaluation run
  `31290905501`。不要把这项组合证据描述成“本机完整复跑 Linux Release 附件”。

### RC 资产

| 资产 | 字节数 | SHA256 |
| --- | ---: | --- |
| `agenttoolgate-windows-amd64.zip` | 18,815,952 | `99534d032617ae3360253cb93c64dc1a30acbfde38271c2157ef08412e1da91e` |
| `agenttoolgate-linux-amd64.tar.gz` | 18,327,001 | `6adc1e945eb3f8a7a8bb963e670371b390cca997ccb8f246dbb78ecc189e102c` |
| `agenttoolgate-evaluation-windows-amd64.zip` | 29,350,619 | `5df902dd749c18138a734866f114478d74dc2c754c07b65ef6a5980c7a6d6092` |
| `agenttoolgate-evaluation-linux-amd64.tar.gz` | 28,610,669 | `77cebade4aeb555a35ceb6e2a24f306a2e3cdfbb33fc57387f2f0cab8d6761a5` |
| `SHA256SUMS` | 416 | `3fdf9631fc0e279adbb981bdfe35cac2391a2365fa1a44445e560990de23bb3c` |

### 正式资产

| 资产 | 字节数 | SHA256 |
| --- | ---: | --- |
| `agenttoolgate-windows-amd64.zip` | 18,815,955 | `a623095802cda5c0396addc980c8c33dd61698a649db934cb00ad205ae03586e` |
| `agenttoolgate-linux-amd64.tar.gz` | 18,326,968 | `26af5f713f599be7371c0b8a1e38160776c30af2bb060346c190793a6a484bbc` |
| `agenttoolgate-evaluation-windows-amd64.zip` | 29,350,617 | `93585b3e1c160971ccbd2b1e00967ecf850fb9b761a9724bea18d39e7ccdd3d6` |
| `agenttoolgate-evaluation-linux-amd64.tar.gz` | 28,610,242 | `619b9b4a0142e804c5e76691eaaf9938fa56658dbe0f8e53022e7d9835b7f6dd` |
| `SHA256SUMS` | 416 | `b58b8b3dbe2fc70c6a76699331881bbae57a0dee6611ed510a3ffc90c99f835e` |

## 5. 历史冻结结论

阶段 5 已完成。`v0.2.0` 的下载内容、源码提交、公开评估快照、README 和 Pages 已建立
可追溯关系。后续新增规则、客户端或平台时，应创建新的评估 run 和 Release，不覆盖
本轮标签或历史证据。

## 6. 历史恢复步骤

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

如需继续发布后开发，从新的独立任务和新版本开始，不修改 `v0.2.0`、`v0.3.0`、
`v0.3.1` 或其候选标签。当前恢复入口是 [`docs/current-status.md`](current-status.md)。

## 7. 禁止顺带修改

后续维护不要为了让评估变绿而顺带修改：

- `.codex/hooks/**`
- `.claude/hooks/**`
- `backend/internal/guard/**`
- Policy / Approval 的生产安全语义
- 已发布的 Release workflow 结果和正式 tag
- 前端控制台

如果评估发现生产安全缺陷，应保留 failed evidence，并单独创建修复提交，不允许
通过放宽 expected decision 或跳过校验得到绿色结果。

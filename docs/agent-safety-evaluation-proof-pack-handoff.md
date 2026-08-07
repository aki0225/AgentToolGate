# Agent 安全评估 Proof Pack 交接

> 交接日期：2026-08-07
>
> 分支：`main`
>
> 实现基线提交：`b40e302`
>
> 远端：实现基线已推送，交接文档随本次提交继续推送
>
> 预期状态：交接提交完成后，`main` 与 `origin/main` 对齐且工作区干净

## 1. 当前结论

Agent 安全评估计划的阶段 0～2 已完成。当前已经不是只有声明式用例或 mock
结论，而是可以使用真实 AgentToolGate 后端、产品 Hook、SQLite 多 Actor 状态和
loopback OTLP collector 执行三套评估。

2026-08-07 的 Windows 本地真实二进制结果：

- dangerous：12 / 12 passed。
- benign：12 / 12 passed。
- governance：6 / 6 passed。
- 三组 stderr 均为空。
- `approval_pre_upstream_calls` 为 0。
- `self_review_success_count` 为 0。
- `frozen_argument_mutation_success_count` 为 0。
- `ticket_replay_success_count` 为 0。
- `secret_leak_count` 为 0。
- `offline_high_risk_allow_count` 为 0。

这些结果是阶段恢复证据，不是正式发布的 Proof Pack。当前还没有生成 JUnit、
Markdown、HTML、manifest 和可发布 evidence。

## 2. 已完成的提交

本轮已经推送以下提交：

```text
2ced4fb 功能：接入真实 MCP Inbound 评估执行器
448a0f6 文档：记录阶段 2C 真实评估结果
811a39d 功能：增强评估运行时隔离与 OTLP 检测
2df319f 功能：执行真实治理不变量评估
b40e302 文档：记录阶段 2D 真实治理验收
```

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

完整设计与阶段记录见：

- `docs/agent-safety-evaluation-proof-pack-plan.md`
- `evaluation/README.md`

## 3. 已执行验证

已通过：

```powershell
go -C tools/atg-eval test -count=1 -timeout 240s ./...
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

Windows 上已重新构建真实 backend 与 evaluator，并执行完整三套 suite。Linux amd64
已完成交叉编译，但尚未在真实 Linux 环境运行三套 suite。

本地运行结果保存在被 Git 忽略的目录：

```text
.tmp/evaluation-results/phase2d-final-20260807/
```

该目录只在当前机器上存在，不能作为公开仓库证据来源。

## 4. 当前边界与未完成内容

实施计划验收清单当前为 6 / 13。核心执行链已完成，剩余工作主要是证据产品化、
CI 展示和正式发布。

### 阶段 3：报告与 evidence

尚未实现：

- `results.json`
- `junit.xml`
- `summary.md`
- 单文件 `report.html`
- `run-manifest.json`
- evidence 文件与 SHA256

### 阶段 4：CI 与展示

尚未实现：

- PR quick suite。
- `workflow_dispatch` / Release full suite。
- 失败时上传脱敏 Artifact。
- README 和 GitHub Pages 从生成报告读取指标。

当前 CI 只执行 evaluator 的 test、vet 和三份 suite validate；默认 Go 测试会覆盖
真实 MCP / governance loopback 集成，但不会通过 `atg-eval run` 生成 Proof Pack。

### 阶段 5：真实客户端与 v0.2.0

尚未实现：

- disposable repo 中的真实 Codex / Claude Code 验收。
- Windows / Linux 的正式 Proof Pack 验收。
- evaluator Release 附件。
- `v0.2.0-rc1` 和 `v0.2.0`。

## 5. 下一步只做阶段 3A

为了保持小步提交，下一次不要同时实现 HTML、CI、Pages 或 Release。

阶段 3A 建议只完成：

1. 新增显式 `--output <directory>`，将保留产物与 disposable sandbox 分离。
2. 生成脱敏后的 `results.json`。
3. 生成 `run-manifest.json`，记录 schema、run ID、平台、时间、suite 和文件 SHA256。
4. 建立 `evidence/` 目录，只保存结构化、脱敏、限长证据。
5. 使用临时目录和原子重命名，失败时不留下半成品 Proof Pack。
6. 补单元测试和一次真实 Windows smoke。
7. 验证通过后立即独立提交，不继续做 JUnit、Markdown 或 HTML。

需要先解决的设计冲突：

- 当前 `--sandbox-base .tmp/evaluation` 下的 run root 会在结束时受控删除。
- 正式报告不能直接写进这个会被删除的 run root。
- 推荐新增独立 `--output .tmp/evaluation-results/<run-id>`，继续保持 sandbox
  disposable；不要为了保留报告而放松 sandbox 清理。

阶段 3A 建议提交信息：

```text
功能：生成评估清单与脱敏证据
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

然后只创建阶段 3A 的实现提交。

## 7. 禁止顺带修改

下一阶段不要顺带修改：

- `.codex/hooks/**`
- `.claude/hooks/**`
- `backend/internal/guard/**`
- Policy / Approval 的生产安全语义
- Release workflow
- GitHub Pages
- 前端控制台

如果阶段 3A 发现生产安全缺陷，应保留 failed evidence，并单独创建修复提交，不允许
通过放宽 expected decision 或跳过校验得到绿色结果。

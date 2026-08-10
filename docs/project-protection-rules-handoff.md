# 项目保护规则交接计划

这份文档用于明天继续完成 PR #6，不代表当前功能已经可以合并。

## 当前状态

- 仓库：`aki0225/AgentToolGate`
- 分支：`codex/repo-protection-rules`
- PR：[功能：增加项目级保护规则与低摩擦审批复用](https://github.com/aki0225/AgentToolGate/pull/6)
- 当前远端基线：`c14895f`
- 当前工作内容：项目级受保护路径、项目级外发 allowlist、Go/Python Hook fallback 对齐、低摩擦审批复用，以及本轮安全审阅修复。
- 本轮只推送分支，不合并 PR。

## 已完成的主要工作

- `.agenttoolgate/protected.json` 支持 repo-relative exact / subtree 路径规则。
- 路径规则只能提升为 `require_approval` 或 `deny`，不能放松 Guard Core。
- `apply_patch` 多目标按目标动作分别评估；Move 源路径按 delete、目标路径按 write。
- 静态识别常见 shell、PowerShell、curl、重定向、Copy/Move、truncate、脚本执行和网络写入。
- 项目 egress 规则支持具体 host、`host:port` 和 `*.domain`，不接受 wildcard 全放行。
- Hook control 文件缺失时为 off；已存在但损坏、字段非法或大小写别名时 fail closed。
- `up` 的 hook control 在服务启动后发布，启动失败尝试恢复原状态，并保留并发更新检测。
- Go Hook、Claude Hook、Codex Hook 和 Python fallback 补充了路径边界、外发解析、控制目录篡改和重复审批回归用例。
- README 与本地使用文档已补充项目规则的适用范围和静态解析边界。

## 最近一次已验证结果

以下结果是在最后一组改动之前或中间阶段得到的，不能代替明天对当前提交重新验证：

- Claude Hook：`55` 项通过。
- Codex Hook：此前 `39` 项通过，最后一组共享解析器改动后需要重跑。
- Go `internal/guard` 与 `cmd/server`：此前通过，最后一组 control、目录篡改和网络参数改动后需要重跑。
- `git diff --check`：此前无 whitespace error，只有 Windows 换行转换提示；推送前重新确认。
- 当前没有对这批最终工作树生成新的 CI 结果。

## 明天第一步

先在仓库目录执行：

```powershell
git fetch --prune origin
git status -sb
git log -1 --oneline
```

确认仍在 `codex/repo-protection-rules`，并保留本分支未完成改动。不要对内部仓或其他分支做整体对齐。

## 必须先完成的审阅项

1. 重跑 Go/Python 回归，确认最新改动没有破坏现有行为：

   ```powershell
   cd backend
   go test -count=1 -timeout 60s ./...
   go vet ./...
   cd ..
   python .claude/hooks/test_agent_guard_pretool.py
   python .codex/hooks/test_agent_guard_pretool.py
   git diff --check
   ```

2. 针对静态解析器补实测或单测：

   - Go/Python 对 `.agenttoolgate` 后代写入保持一致，读取普通客户端配置不应误判 self-tamper。
   - PowerShell 的反引号转义、Bash 的反斜杠转义、显式和省略 `-Command` 的执行方式分别验证。
   - curl 的 `--url-query`、`--interface`、`-m`、`--connect-timeout`、`-T/--upload-file`、`-D/--dump-header`、`-c/--cookie-jar` 和 `@file` 输入输出分别验证。
   - 允许域名的正常 GET/POST 不应因为参数值被误认为第二个 URL；未列域名的写入仍必须提升或拒绝。
   - PowerShell `-ErrorAction` 等常见参数不能把参数值误判为 URL。
   - 带逗号的引号内文件名与未引用的 PowerShell 路径列表要分别验证。
   - control JSON 的未知字段、重复字段、大小写别名、尾随 JSON 和错误类型都必须 fail closed。

3. 决定启动失败回滚的并发语义：当前实现通过比较已发布 payload 尽量避免覆盖新状态，但比较和恢复之间仍有跨进程 TOCTOU 窗口。合并前要么增加可靠的跨进程锁，要么把它明确记录为已知边界，不要声称原子 CAS 已经完成。

## 合并前硬门槛

- 当前工作树只包含本 PR 文件和本交接文档。
- Go 全量测试、`go vet`、两套 Hook 测试、`git diff --check` 全部通过。
- 真实项目目录 smoke：受保护读取触发审批或拒绝，生产配置删除无副作用，未列外发 host 被拦，允许 host 的普通开发命令不被误拦。
- 独立 reviewer 批准后，同一指纹精确重试才可静默复用；改变命令、目标、workspace、身份或参数后必须重新审批。
- 新提交触发的 PR CI 为 success 后，才允许合并 PR #6。

## 继续时不要做的事

- 不要为了降低误拦而削弱 Guard Core、敏感路径、外发拒绝、自篡改保护或审批票据约束。
- 不要把项目规则增加 `allow` 效果，也不要把高危动作改成长期开关。
- 不要把 Python fallback 宣传成与 Go 完全等价，除非共享向量和两边回归都已实际验证。
- 不要把 Hook 说成 OS sandbox、EDR 或完整 DLP。
- 不要修改前端、网站、发布 tag、内部仓或无关任务。

## 交接后的预期流程

验证通过后，先查看最终 diff 和敏感扫描，再提交并 push 当前分支。等待 PR CI success，重新读取 PR head SHA 和检查结果，确认仍是本次提交后再合并。合并后核对 `origin/main`、工作树状态和 PR 状态。

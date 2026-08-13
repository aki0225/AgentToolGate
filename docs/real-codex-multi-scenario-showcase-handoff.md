# 真实 Codex 五场景展示交接

> 交接日期：2026-08-13
>
> 交接时分支：`main`
>
> 播放器初始实现提交：`6bb1459`
>
> 后续准确性与交互修复：`b6b3ec8`
>
> Playwright 浏览器门禁：`198a60b`
>
> 当前恢复入口：[`docs/current-status.md`](current-status.md)

## 本轮已完成

1. 使用正式 `v0.3.1` Release、Codex CLI `0.146.0` 和 `gpt-5.5`，在系统临时目录中的 disposable Git 仓库完成五次独立真实 Codex 会话：
   - 低摩擦开发：普通命令、源码读取、普通文件写入和 MCP 回显通过。
   - 敏感读取：读取 synthetic `.ssh/id_rsa` 被拒绝。
   - 破坏性删除：删除 disposable 仓库根目录被拒绝。
   - 网络外传：向回环 collector 发送 synthetic 值被拒绝，collector 请求数为 `0`。
   - 受保护写入：修改 `release.yml` 被项目保护规则拒绝。
2. 新增 v2 公开证据目录：

   ```text
   evaluation/published/real-codex-demo-v2/
   ```

   目录严格包含 12 个文件；旧的 v1 证据目录继续保留。
3. 修正 Codex MCP 工具在 `PreToolUse` 阶段被拒绝时的证据判定：
   - 命令型阻断使用 Codex stderr 与 Hook observer 关联。
   - MCP 工具型阻断使用精确 Hook observer 请求、后端 deny Audit、collector `0` 请求和执行 marker 不存在共同证明。
   - 不再要求 Codex `exec --json` 必须输出一个实际不会稳定出现的 `mcp_tool_call` 事件。
4. GitHub Pages 真实 Codex 模块升级为五场景播放器：
   - 五个可切换场景标签。
   - 独立终端回放、Guard 决策、Audit 摘要和后置条件。
   - 支持键盘方向键、`Home`、`End`。
   - 支持 `prefers-reduced-motion`。
   - 适配 1440、760、375 像素宽度。

## 已验证

```powershell
python -m unittest scripts/real-codex-demo/test_run_demo.py scripts/real-codex-demo/test_multi_demo.py

python scripts/real-codex-demo/scan_public_artifacts.py `
  --input evaluation/published/real-codex-demo-v2

cd website
npm run check
npm test
npm run build
npm run playwright:install
npm run e2e
```

结果：

- Python：44 个测试通过。
- Website：37 个测试通过。
- Website 类型检查与生产构建通过。
- v2 公开证据 12 个文件的敏感扫描通过。
- `git diff --check` 通过。
- 本地浏览器控制台无 error 或 warning。
- 本地验收服务已停止，端口 `4176` 不再监听。
- 真实演示临时端口 `18190`、`18191`、`18192` 均已关闭。

浏览器手工验收覆盖：

- 1440 像素桌面双栏布局。
- 760 像素单列布局。
- 375 像素手机布局与场景标签局部横向滚动。
- 播放、重新播放、重置。
- 键盘方向键、`Home`、`End` 的选中状态、焦点和面板关联。
- reduced-motion 下直接展示完整录制、隐藏终端光标且无页面横向溢出。

## 公开证据边界

- 页面展示的是经过校验和脱敏的真实 Codex CLI **预录证据**，不是浏览器实时连接。
- 所有敏感值、目标仓库和 collector 均为 synthetic / disposable 测试资产。
- 不包含真实 API Key、provider 身份、VPS / SSH 信息或用户私有目录。
- AgentToolGate 是执行前 guardrail，不是 OS sandbox、EDR 或完整 DLP。
- 当前 Codex Hook MVP 将 Guard `ask` 保守映射为拒绝，不冒充 Codex 交互审批。
- `gpt-5.5` 是本轮客户端请求和验收记录中的模型名；公开证据不包含 provider 身份，因此不声称能够独立证明上游底层模型身份。

## 后续完成的展示修复

原交接列出的 9 项非阻断展示问题中，8 项已完成，1 项保留为可选展示优化：

1. 五个 v2 Cast 不再写入固定的 `SHELL=/bin/bash`。
2. v2 公开录制中的系统 PowerShell 绝对路径统一显示为 `pwsh`，scanner 已增加回归检查。
3. 页面证据面板已经展示动作目标、Guard signal、Audit 和独立后置条件；原始 Cast
   仍以 `AgentToolGate 验收关联` 摘要为主。若以后需要让终端回放单独承担更多解释，
   可以增加明确标为 `Hook observer 观察到` 的验收器派生行，但不得冒充 Codex 原始事件。
4. Website 派生契约保留 `codexAskMapping=conservative_deny`，并做严格解析测试。
5. 播放器文案改为“自适应加速回放”，不再声称固定 `4×`。
6. `prefers-reduced-motion` 下直接展开完整记录，隐藏无意义的播放和重置控件。
7. 水平 Tab 只处理 `ArrowLeft`、`ArrowRight`、`Home`、`End`，上下键保留页面默认行为。
8. Playwright 已覆盖标签键盘语义、播放/暂停/重置、重播、三种视口和 reduced-motion；
   CI 与 Pages 上传前都会执行。
9. 低摩擦场景的规则文案已标为“代表性写入规则”，不再用单条规则概括整个场景。

第 1、2、4～7、9 项由 `b6b3ec8` 完成；第 8 项由 `198a60b` 完成。第 3 项不影响
Guard 决策、Audit、后置条件或敏感扫描，不是发布阻断项。对应 `main` 的 CI
[`31708710029`](https://github.com/aki0225/AgentToolGate/actions/runs/31708710029)
和 Pages
[`31708710016`](https://github.com/aki0225/AgentToolGate/actions/runs/31708710016)
均为 success。

修改现有 v2 Cast 后，必须同步更新：

- `evaluation/published/real-codex-demo-v2/summary.json` 中对应录制元数据。
- `evaluation/published/real-codex-demo-v2/manifest.json`。
- `website/src/data/` 下五个派生 Cast 与 `real-codex-scenarios.json`。

不得重新生成 synthetic Secret，不需要重新调用模型；只有在现有同步事件、Hook observer、Audit 和后置条件不足以支持展示内容时，才重新录制。

## 后续维护验证

```powershell
git pull --ff-only
Get-Content docs/real-codex-multi-scenario-showcase-handoff.md

python -m unittest scripts/real-codex-demo/test_run_demo.py scripts/real-codex-demo/test_multi_demo.py

cd website
npm run real-codex:sync
npm run check
npm test
npm run build
npm run playwright:install
npm run e2e
```

若修改公开证据，额外执行：

```powershell
cd ..
python scripts/real-codex-demo/scan_public_artifacts.py `
  --input evaluation/published/real-codex-demo-v2
git diff --check
```

最后仍应启动 Website 做人工 spot check，并确认公开 Pages，而不只看 Actions 状态。

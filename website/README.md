# AgentToolGate 产品展示站

`website/` 是独立的 Vite + React + TypeScript 静态站，用于 GitHub Pages 产品展示。它不复用生产控制台，不连接 AgentToolGate API，也不读取浏览器存储、Cookie、本地文件或真实 Secret。

## 本地运行

```powershell
cd website
npm ci
npm run dev
```

Vite 的 Pages 基础路径固定为 `/AgentToolGate/`。本地开发时按终端输出访问对应地址。

## 验证

```powershell
npm run check
npm test
npm run build
npm run playwright:install
npm run e2e
```

`npm test` 会对 `src/demo/stateMachine.ts` 执行 Vitest 单元测试和覆盖率门禁，覆盖正常审批、自批拒绝、拒绝分支、一次性票据、非法迁移与重置。

`npm run e2e` 会启动生产预览，并用 Chromium 验证真实 Codex 五场景播放器的键盘语义、
播放状态、重播、三种视口和 `prefers-reduced-motion`。首次运行或浏览器版本变化后先执行
`npm run playwright:install`。

真实 Codex 预录证据位于 `evaluation/published/real-codex-demo/`。修改或重新导入证据后先执行：

```powershell
npm run real-codex:sync
npm run real-codex:check
```

校验器会验证文件白名单、manifest SHA256、Hook trust、MCP/Audit 关联、受保护写入拒绝、仓库与清理后置条件，以及 Asciicast v2 事件边界；页面只消费校验后派生的静态 JSON 与录制文件。

## 实现边界

- 三个交互场景只使用 synthetic fixtures 和浏览器内存状态。
- 页面不发送 `fetch`、XHR、WebSocket 或第三方分析请求。
- 真实 Codex 区域是同源静态预录证据，不在浏览器中连接本机、AgentToolGate 后端或模型上游。
- ATG 管理的 Connector Secret 只以 `valueRef` 元数据出现在展示内容中，不包含 Secret value。
- Local Action 场景不包含可执行恶意脚本，不写入真实 Startup、Hook 或凭据路径。
- Audit 与 OTel 使用脱敏口径；审批内部暂存冻结参数的事实会明确披露。
- 证据区分自动评估、`v0.3.1` 正式发布验收、同产品提交的真实 Codex 验收和历史
  双客户端媒体验收，不把不同提交、不同层级的结果混写成同一次验证。
- 所有外部链接都指向公开 GitHub 源码、文档、CI 或 Release，并使用安全的 `rel` 属性。

## 视觉与无障碍

- 沿用控制台产品 token：`#07111f`、`#5eead4`、`#60a5fa`、`#fb7185`、`20px`。
- 主视觉使用 HTML/CSS 治理管线，不依赖现有通用 Hero 图片。
- 支持键盘导航、场景标签页方向键、清晰焦点、`aria-live`、375px～1440px 响应式布局和 `prefers-reduced-motion`。

# AgentToolGate GitHub Pages 产品展示站实施计划

> 状态：本地实现完成，待启用 GitHub Pages 与远端发布验收
> 计划版本：v0.1
> 编写日期：2026-08-04
> 审阅结论：首版本地实现与验证已完成；代码可推送保存，但 Pages workflow 暂仅支持手动触发，不在本轮自动启用或部署公开站点。

## 1. 背景

AgentToolGate 当前已经具备较完整的开源产品闭环：

- Codex / Claude Code 项目级初始化与 Hook Adapter。
- MCP Streamable HTTP、MCP SSE fallback、MCP Inbound / Outbound。
- Policy、Approval、Secret、Audit、Rate Limit 和 OpenTelemetry。
- GitHub、HTTP、数据库、外部 MCP Connector。
- Local Action Firewall、`deny_with_ticket`、一次性票据和 remembered allow。
- SQLite 单二进制本地版、PostgreSQL、OIDC、RBAC 和 workspace 隔离。
- Windows / Linux amd64 Release、SHA256、CI、E2E 和公开威胁模型。

当前主要短板已经不是功能数量，而是公开表达：

1. 新访客需要阅读较长 README 才能理解项目价值。
2. 自托管产品无法直接在 GitHub Pages 上运行真实 Go 后端。
3. 现有演示证据分散在 README、文档、截图和命令行输出中。
4. 简历中的项目链接缺少一个可在 30 秒内完成理解的产品入口。

因此需要建设一个静态产品展示站，把真实能力、核心交互、工程证据和安全边界组织成一条可信、易理解的叙事。

## 2. 目标

### 2.1 产品目标

让首次访问者在以下时间内完成理解：

- 10 秒：知道 AgentToolGate 解决什么问题。
- 30 秒：看懂一次高危工具调用如何经过 Policy、Approval 和 Audit。
- 2 分钟：理解 Local Action Firewall、MCP 双向治理和 Secret 隔离。
- 5 分钟：找到 Release、快速开始、架构、威胁模型和源码入口。

### 2.2 求职目标

展示站需要让面试官直观看到以下能力：

- Agent 工具调用治理与 MCP 协议理解。
- Go 后端、React 前端与本地单二进制工程化。
- Policy / Approval / Audit 的完整状态机设计。
- 安全边界、fail-closed、Secret 脱敏和威胁建模意识。
- CI、E2E、双平台 Release 和可观测性建设。

### 2.3 转化目标

页面提供三个明确入口：

1. 查看交互式静态演示。
2. 下载 Windows / Linux Release。
3. 查看 GitHub 源码和深入文档。

## 3. 非目标

本任务不做以下事项：

- 不把 Go 后端部署到 GitHub Pages。
- 不搭建长期运行的公网演示后端。
- 不允许访客连接真实 GitHub、数据库、HTTP 或 MCP 服务。
- 不收集用户 token、Secret、表单信息或行为分析数据。
- 不新增注册、登录、在线工作区或多租户系统。
- 不把静态演示描述成真实安全边界或真实上游执行。
- 不修改 AgentToolGate 现有业务 API、治理语义和客户端 Hook。
- 不为展示站引入 KMS、Vault、GitHub App 等新的产品功能。
- 首版不做完整英文站、博客、文档搜索、评论和在线 Playground。

## 4. 方案比较与决策

### 4.1 方案 A：直接部署现有 `frontend/`

做法：为现有控制台增加 `DEMO_MODE`，在 GitHub Pages 下使用模拟 API。

优点：

- 可以复用现有页面和组件。
- 视觉与真实控制台完全一致。

缺点：

- 把展示逻辑带入生产控制台。
- Auth、workspace、API client 和路由需要大量条件分支。
- 容易让访客误以为页面连接了真实后端。
- 展示站和业务前端的发布节奏相互影响。

结论：不采用。展示需求不应污染生产前端。

### 4.2 方案 B：只做静态 README 镜像

做法：使用简单 HTML 或 Jekyll，把 README 内容搬到 GitHub Pages。

优点：

- 实施快。
- 依赖和维护成本低。

缺点：

- 与 GitHub README 差异不大。
- 无法展示 Approval、Audit 和 Guard 的动态过程。
- 对简历和产品传播的提升有限。

结论：不采用。投入产出不足。

### 4.3 方案 C：独立 `website/` 静态产品站

做法：新增独立的 Vite + React 静态站，使用本地 fixtures 模拟受治理调用，不连接真实后端。

优点：

- 与生产控制台完全隔离。
- 可以围绕产品叙事设计页面，而不是复刻管理后台。
- 支持轻量交互、动画、响应式和无障碍。
- GitHub Pages 可以直接托管构建产物。
- 后续可以独立增加英文内容、视频和自定义域名。

缺点：

- 会新增一个前端 package 和 lockfile。
- 品牌 token 与真实控制台存在少量重复维护。

结论：采用方案 C。

## 5. 展示原则

### 5.1 真实可信

- 静态演示中的字段、状态和决策必须对应现有真实语义。
- 使用 `allow`、`deny`、`require_approval`、`approval_required` 和 `deny_with_ticket` 等真实状态。
- 演示内容使用脱敏后的 synthetic fixtures。
- 不展示真实 token、Authorization、DSN、本机绝对路径、完整 approval ID 或 fingerprint。
- 所有模拟交互区域固定显示“静态演示，不连接真实后端或上游服务”。

### 5.2 突出差异化

展示重点不是“又一个 MCP 管理后台”，而是：

1. Agent 的危险后果在工具执行前被治理。
2. 审批前不会触达真实上游。
3. Secret 不交给模型，Audit 不保存原始敏感内容。
4. 本地文件、命令和持久化动作也能进入 Guard 流程。

### 5.3 明确边界

页面必须保留醒目的边界说明：

- AgentToolGate 是 guardrail，不是 OS sandbox、EDR 或 DLP。
- 它不阻止提示词注入进入上下文，而是降低高危工具调用成功落地的概率和影响。
- Codex 当前缺少完整交互式 ask，需确认动作采用保守 deny/no-op。
- 生产环境仍需最小权限、系统隔离、网络策略和上游服务权限控制。

## 6. 视觉方向

### 6.1 设计关键词

采用“安全运营台 × 工程档案”的克制工业风：

- 深石墨黑背景，不使用大面积紫色 AI 渐变。
- 青绿色表示允许和健康状态。
- 琥珀色表示审批和待确认。
- 红色只用于阻断和高风险信号。
- 使用细网格、微弱噪点、单线流程图和状态光标营造技术感。
- 真实产品 UI、审计字段和调用链是主视觉，抽象科幻图片只作为辅助。

### 6.2 视觉记忆点

首屏中央展示一条“请求穿过治理闸门”的动态管线：

```text
Codex / Claude
        ↓
Hook / MCP
        ↓
Policy Decision
        ↓
Approval Gate
        ↓
Connector Runtime
        ↓
Redacted Audit
```

一条高危请求进入后，在 Approval Gate 前停止；右侧同步出现风险解释和审批状态。这个过程应成为展示站最容易被记住的元素。

### 6.3 动效约束

- 首屏只做一次有节奏的进入动画。
- 滚动时只激活必要的流程节点和数字变化。
- 不使用持续旋转、频繁发光或大面积粒子动画。
- 支持 `prefers-reduced-motion`，关闭非必要动效。
- 动效不能阻塞阅读、键盘操作或按钮响应。

## 7. 信息架构

首版采用单页结构和锚点导航，不引入客户端路由，避免 GitHub Pages 子路径刷新问题。

### 7.1 顶部导航

- 产品能力
- 交互演示
- 架构
- 安全边界
- 工程证据
- 下载
- GitHub

### 7.2 Hero

主标题：

> 让 AI Agent 的高危动作，在真正执行前先过治理闸门

说明：

> 面向 Codex、Claude Code 与 MCP 客户端的本地工具治理网关：Policy、Approval、Secret、Audit 与 Local Action Firewall。

操作按钮：

- 查看交互演示
- 下载最新 Release
- 查看源码

辅助信息：

- Windows / Linux
- 单二进制 + SQLite
- MCP Streamable HTTP
- MIT License
- CI 状态

### 7.3 问题与解决方案

用对照形式展示：

| 未治理的 Agent | 经过 AgentToolGate |
| --- | --- |
| 模型直接调用高权限工具 | 所有调用先进入 Policy |
| 写操作立即触达上游 | 高风险写操作进入 Approval |
| token 进入上下文和日志 | Secret 在后端运行时注入 |
| 失败后难以追踪 | Audit、reason、trace id 留痕 |
| 本地命令只依赖客户端提示 | Local Action Firewall 补充治理 |

### 7.4 交互演示

展示站提供三个静态场景标签页。

#### 场景 A：本地高危动作

输入：

- adapter：Codex
- action：写入 synthetic Windows Startup 路径
- signals：persistence、hidden PowerShell、ExecutionPolicy Bypass

演示状态：

1. Agent 请求执行。
2. Guard 识别 sensitive target。
3. 返回 `deny_with_ticket`。
4. UI 展示 risk level、matched rule 和 signals。
5. Reviewer 批准后生成一次性 ticket。
6. 同 fingerprint 重试只允许消费一次。

#### 场景 B：GitHub 写审批

输入：

- tool：`github.create_issue`
- repository：`acme/demo`
- actor：requester

演示状态：

1. Policy 返回 `require_approval`。
2. 上游请求计数保持为 0。
3. requester 自批返回 forbidden。
4. 切换 reviewer 并填写 reason。
5. 审批后上游请求计数变为 1。
6. Audit 展示 requester、reviewer、reason 和 frozen arguments。

#### 场景 C：MCP 与 Secret

输入：

- MCP client 调用 `mcp_weather.create_note`
- Connector 使用 env-backed Secret

演示状态：

1. 外部 MCP 工具同步进 Tool Registry。
2. 写类工具进入 Approval。
3. 模型侧只看到工具 schema，不看到 Secret value。
4. 后端运行时注入 Secret。
5. Audit 输出使用 `[REDACTED]`。

### 7.5 架构

展示三条入口和统一治理链路：

- REST Tool Call
- MCP Inbound / Outbound
- Local Action Guard

架构图使用可访问的 HTML/SVG 绘制，不依赖 Mermaid 运行时。

### 7.6 工程证据

使用明确指标和链接展示：

- Go + React + TypeScript。
- SQLite / PostgreSQL / MemoryStore。
- Windows / Linux amd64 Release。
- CI、PostgreSQL 集成测试、TypeScript check、生产构建和 E2E。
- OIDC、RBAC、workspace isolation。
- OpenTelemetry 和 Prometheus。
- Threat Model、Security Review、Daily Use Acceptance。

禁止使用无法由仓库和 CI 证明的夸张数字，例如虚构覆盖率、用户量或性能提升。

### 7.7 安全边界

以“能做 / 不能替代”双栏展示：

能做：

- 工具执行前的确定性策略和审批。
- Secret 运行时注入和审计脱敏。
- 高危本地动作解释与阻断。
- workspace 级治理和留痕。

不能替代：

- OS sandbox、EDR、DLP。
- 提示词注入检测的完整解决方案。
- 云 KMS/Vault。
- 企业级灾备、SLO 和职责分离。

### 7.8 下载与快速开始

提供：

- Windows amd64 Release。
- Linux amd64 Release。
- SHA256SUMS。
- 三条最短命令：`doctor`、`init all`、`up --open`。
- Codex / Claude Code 接入文档。

## 8. 技术方案

### 8.1 目录规划

计划新增：

```text
website/
├── index.html
├── package.json
├── package-lock.json
├── tsconfig.json
├── vite.config.ts
├── public/
│   ├── favicon.svg
│   ├── og-cover.webp
│   └── screenshots/
├── src/
│   ├── main.tsx
│   ├── App.tsx
│   ├── styles.css
│   ├── components/
│   │   ├── HeroPipeline.tsx
│   │   ├── DemoConsole.tsx
│   │   ├── ScenarioTabs.tsx
│   │   ├── ArchitectureFlow.tsx
│   │   ├── CapabilityGrid.tsx
│   │   └── SecurityBoundary.tsx
│   ├── demo/
│   │   ├── scenarios.ts
│   │   ├── stateMachine.ts
│   │   └── stateMachine.test.ts
│   └── assets/
└── README.md
```

计划修改：

```text
.github/workflows/pages.yml
.github/workflows/ci.yml
README.md
.gitignore
```

不计划修改：

```text
backend/
frontend/src/api/
frontend/src/auth/
frontend/src/pages/
.claude/hooks/
.codex/hooks/
```

### 8.2 技术栈

- Vite
- React
- TypeScript strict mode
- 原生 CSS variables
- Lucide 图标或小型内联 SVG
- Vitest 测试纯状态机逻辑

首版不引入：

- 新的全量 UI 框架。
- 动画运行时库。
- 服务端渲染框架。
- 远程 CMS。
- 外部分析脚本。

### 8.3 GitHub Pages 子路径

项目站最终路径形态为：

```text
https://aki0225.github.io/AgentToolGate/
```

Vite 配置需要显式设置：

```ts
export default defineConfig({
  base: "/AgentToolGate/",
});
```

站内资源和链接不能硬编码根路径 `/`。

### 8.4 Demo 状态机

交互演示使用纯前端状态机，而不是 `setTimeout` 拼接脚本。

示例状态：

```text
idle
→ evaluating
→ approval_required
→ self_review_denied
→ reviewer_ready
→ approved
→ executed
→ audited
```

要求：

- 非法状态迁移必须被拒绝。
- 状态文案、颜色和可用按钮由同一个状态源派生。
- 每次“重新演示”回到完全一致的初始状态。
- fixtures 不包含真实凭据或可执行恶意脚本。
- 测试覆盖正常审批、自批拒绝、拒绝分支和重置。

### 8.5 素材策略

首版需要重新生成统一尺寸的公开素材：

1. Dashboard 总览。
2. Approvals requester / reviewer / reason。
3. Audit Risk Explanation。
4. Policy Simulator trace。

素材要求：

- 使用 synthetic workspace 和 synthetic 数据。
- 裁掉浏览器个人信息、系统用户名和本机路径。
- 不出现真实 token、完整 fingerprint、完整 approval ID。
- 导出为 WebP，并保留 PNG 原始稿在本地，不提交包含敏感信息的原图。
- 页面内图片使用固定宽高，避免 CLS。

现有 `risk-explanation-ui.png` 可以作为内容参考，但不直接承担完整产品展示。

可选增加一段 45～60 秒、无声音自动播放的 WebM：

- 必须提供播放控制。
- 默认静音。
- 不自动循环制造持续干扰。
- 提供静态 poster 和文字说明。
- 首版如果无法保证录屏脱敏，可推迟到第二阶段。

## 9. GitHub Actions 部署

新增 `.github/workflows/pages.yml`：

- 触发：
  - `main` 分支中 `website/**`、Pages workflow 或相关素材变化。
  - `workflow_dispatch`。
- 权限：
  - `contents: read`
  - `pages: write`
  - `id-token: write`
- 构建：
  - Node.js 20。
  - `npm ci`。
  - `npm run check`。
  - `npm test`。
  - `npm run build`。
- 部署：
  - `actions/configure-pages`。
  - `actions/upload-pages-artifact`。
  - `actions/deploy-pages`。
- 并发：
  - 同一 Pages 环境只保留最新部署。

现有 CI 增加独立 `website` job，确保普通 PR 在部署前完成类型、测试和构建检查。

## 10. README 联动

Pages 上线后再修改 README：

- Hero 导航增加“在线展示”。
- Release 按钮旁增加“交互演示”。
- README 保留架构、边界和快速开始，不把全部内容迁走。
- 仓库 homepage 设置为 Pages 地址。

README 与展示站必须保持以下口径一致：

- 不防提示词注入发生，治理其高危工具调用后果。
- Guardrail 不是 OS sandbox。
- Codex interactive ask 仍有限制。
- Secret 当前为 env-backed `valueRef`。
- 默认 local auth 不能直接暴露到公网。

## 11. 无障碍与响应式

验收要求：

- 支持键盘完成导航、切换场景和运行演示。
- 焦点样式清晰，不只依赖颜色表达状态。
- 正文和小字号文本满足 WCAG AA 对比度。
- 状态变化使用 `aria-live`，但避免重复朗读动画细节。
- 支持 `prefers-reduced-motion`。
- 支持 375px、768px、1280px 和 1440px 视口。
- 移动端演示管线改为纵向，不出现强制横向滚动。

## 12. 性能与安全

### 12.1 性能预算

- 首屏不加载视频正文。
- 图片使用 WebP、尺寸属性和懒加载。
- 首屏 JavaScript gzip 目标不超过 250KB。
- 不引入大型动画、图表或 Markdown 运行时。
- Lighthouse Performance、Accessibility、Best Practices、SEO 目标均为 90 分以上。

### 12.2 安全要求

- 页面不读取 GitHub token、Cookie、本地文件或浏览器存储中的敏感内容。
- 不调用真实 AgentToolGate API。
- 不调用第三方分析、表单和演示 API。
- 外部链接使用安全的 `rel` 属性。
- fixtures 和截图进入 Git 前进行敏感信息检查。
- GitHub Pages 只承载静态展示，不宣称为安全测试环境。

## 13. 测试与验证

### 13.1 自动化验证

在 `website/` 下执行：

```powershell
npm ci
npm run check
npm test
npm run build
```

根目录执行：

```powershell
git diff --check
```

GitHub workflow 使用 `actionlint` 校验。

### 13.2 功能验收

- 所有导航锚点可达。
- 三个演示场景均可独立运行和重置。
- 自批场景不会错误进入 approved。
- 上游计数只在批准后的模拟执行阶段变为 1。
- 所有 Release、GitHub 和文档链接正确。
- Pages 子路径下 CSS、JS、图片和字体不存在 404。
- 浏览器控制台无 error。

### 13.3 视觉验收

- 375px 和 1440px 截图对比。
- 首屏信息层级清晰。
- 页面不依赖通用紫色渐变和重复玻璃卡片。
- 状态颜色与真实产品语义一致。
- 动效关闭后不影响内容理解。
- 图片加载前后无明显布局跳动。

### 13.4 发布验收

- PR CI 全绿。
- Pages workflow 成功。
- 公开 URL 可匿名访问。
- 首页 metadata、favicon、Open Graph 图片正确。
- README 的在线展示入口可用。
- Release 下载仍指向现有 GitHub Release，不复制二进制到 Pages。

## 14. 分阶段实施

### 阶段 1：站点骨架与内容

- 创建 `website/`。
- 建立设计 token、排版和响应式框架。
- 完成 Hero、问题对照、架构、能力、边界和下载区域。
- 使用静态占位图，先打通 build。

完成标准：本地静态站可完整阅读，信息架构审阅通过。

### 阶段 2：交互演示

- 实现 demo state machine。
- 完成三个场景。
- 增加状态迁移测试。
- 加入风险解释和审计 timeline。

完成标准：所有演示状态可复现，不连接真实后端。

### 阶段 3：真实素材与精修

- 使用 synthetic 数据重新截取四张产品截图。
- 优化动效、移动端和无障碍。
- 完成 metadata、favicon 和 Open Graph 图片。

完成标准：桌面和移动端视觉验收通过。

### 阶段 4：CI 与 Pages

- 新增 Pages workflow。
- 扩展 CI。
- 启用 GitHub Pages。
- 更新 README 和 repository homepage。

完成标准：公开站点可匿名访问，CI 与 Pages 部署均绿色。

### 阶段 5：可选增强

- 45～60 秒脱敏演示视频。
- 完整英文版本。
- 自定义域名。
- 发布版本与能力矩阵自动同步。

该阶段不属于首版验收。

## 15. 预期改动边界

首版预计新增或修改：

```text
website/**
.github/workflows/pages.yml
.github/workflows/ci.yml
.gitignore
README.md
```

首版禁止顺带修改：

```text
backend/**
frontend/src/api/**
frontend/src/auth/**
.claude/hooks/**
.codex/hooks/**
scripts/build-release.ps1
.github/workflows/release.yml
```

如果实施过程中发现必须修改上述禁止区域，应停止并重新评审范围。

## 16. 风险与应对

### 风险 1：静态演示被误认为真实后端

应对：

- 演示区固定显示静态演示标识。
- 不展示假的网络请求加载过程。
- 不使用“在线试用真实 AgentToolGate”等误导性文案。

### 风险 2：展示语义与真实产品漂移

应对：

- fixtures 使用集中类型定义。
- 状态名称直接对应后端公开语义。
- 产品治理语义变化时，将展示站纳入同一个 PR 的检查清单。

### 风险 3：新 package 增加维护成本

应对：

- 只使用现有团队熟悉的 Vite、React 和 TypeScript。
- 不引入重量级 UI/动画框架。
- 保持单页、少依赖和纯静态输出。

### 风险 4：素材泄漏本地信息

应对：

- 所有截图和录屏使用 synthetic workspace。
- 发布前执行文本、EXIF、路径和凭据检查。
- 原始录屏不直接提交，先生成脱敏产物。

### 风险 5：Pages 子路径导致资源 404

应对：

- 固定 Vite `base=/AgentToolGate/`。
- 不使用根路径资源。
- 在部署 workflow 中增加构建产物 smoke。

## 17. 验收标准

首版完成必须同时满足：

- [ ] GitHub Pages 展示站可匿名访问。
- [ ] 10 秒内能理解产品定位。
- [ ] 三个静态场景可运行、重置且无真实副作用。
- [ ] 展示真实的 Policy、Approval、Guard、Secret 和 Audit 语义。
- [ ] 页面明确声明 guardrail 与 OS sandbox 的边界。
- [ ] Windows / Linux Release 和源码入口可用。
- [ ] 桌面和移动端布局通过检查。
- [ ] 类型检查、单元测试、生产构建和 CI 全绿。
- [ ] Pages 资源无 404，浏览器控制台无 error。
- [ ] 截图和 fixtures 不含敏感信息。
- [ ] README 和展示站口径一致。

## 18. 审阅决策点

已确认以下实施决策：

1. 采用独立 `website/`，不复用生产 `frontend/`。
2. 首版以中文为主体，保留一句英文定位。
3. 三个演示场景全部使用静态 fixtures，不提供公网真实后端。
4. Local Action Firewall 作为默认首屏演示。
5. 首版暂缓视频，先完成交互演示和可替换的静态素材位。
6. 采用“安全运营台 × 工程档案”的克制工业视觉方向。

## 19. 参考资料

- GitHub Pages 官方说明：<https://docs.github.com/en/pages/getting-started-with-github-pages/about-github-pages>
- GitHub Pages 自定义 Actions workflow：<https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages>
- Vite 静态站部署指南：<https://vite.dev/guide/static-deploy.html>

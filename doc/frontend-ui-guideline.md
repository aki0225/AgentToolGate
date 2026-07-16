# AgentToolGate 前端 UI 规范

> 版本：v1.0
> 日期：2026-06-04
> 目标：为 AI 协作开发提供明确的视觉和交互标准，确保所有新页面风格统一、质量稳定。

---

## 1. 技术栈迁移计划

### 当前状态
- React 18 + TypeScript + Vite
- 纯手写 CSS（单文件 styles.css）
- 无 UI 组件库

### 目标状态
- React 18 + TypeScript + Vite
- Tailwind CSS v4（utility-first）
- shadcn/ui（组件源码直接拷入项目，完全可控）
- 暗色主题为唯一主题（不需要 light mode 切换）

### 迁移原则
- 新页面（MCP、策略管理、Agent Demo）直接用 shadcn 组件 + Tailwind 编写
- 旧页面保持现有 CSS 不动，后续有空逐页迁移
- 两套样式共存期间通过 Tailwind 的 `@layer` 避免冲突

---

## 2. 颜色系统

基于现有 CSS 变量，映射到 Tailwind/shadcn 的 token 体系：

| 用途 | CSS 变量 | 色值 | Tailwind token |
|------|----------|------|----------------|
| 页面背景 | --bg | #07111f | `background` |
| 悬浮背景 | --bg-elevated | rgba(9,18,35,0.9) | `card` |
| 面板背景 | --panel | rgba(11,19,35,0.88) | `muted` |
| 面板边框 | --panel-border | rgba(148,163,184,0.16) | `border` |
| 主文字 | --text | #e2e8f0 | `foreground` |
| 次要文字 | --muted | #94a3b8 | `muted-foreground` |
| 主强调色 | --accent | #5eead4 | `primary` |
| 次强调色 | --accent-2 | #60a5fa | `accent` |
| 危险色 | --danger | #fb7185 | `destructive` |

### 颜色使用规则

- **主强调色（teal #5eead4）**：主按钮、激活状态、成功标签、关键数据
- **次强调色（blue #60a5fa）**：链接、hover 边框、次要按钮、信息标签
- **危险色（rose #fb7185）**：错误、拒绝、删除按钮、失败标签
- **白色低透明度**：hover 背景用 `rgba(255,255,255,0.04)`，不要用纯白
- **禁止**：不要引入其他颜色。没有黄色、橙色、紫色。整个 UI 只有 teal/blue/rose 三个语义色

---

## 3. 排版规范

### 字体
- 字体族：`Inter, ui-sans-serif, system-ui, sans-serif`
- 不要引入其他字体

### 字号层级

| 层级 | 大小 | 用途 |
|------|------|------|
| 页面标题 | `clamp(2rem, 3vw, 3.2rem)` / `text-3xl` | 每页顶部 hero 区域 |
| 面板标题 | `text-lg` (1.125rem) | Panel 标题、Section 标题 |
| 正文 | `text-sm` (0.875rem) | 表格内容、描述文字 |
| 标签 | `text-xs` (0.75rem) | 表头、Badge、辅助标注 |
| 大数字 | `text-3xl` (1.875rem) | 指标卡数值 |

### 字重
- 700：标题、数值、强调
- 400：正文、描述
- 不要用 300（太细看不清）、不要用 800+（太重）

### 标签文字样式
- 全部大写 `uppercase`
- 字间距 `tracking-wider`（0.08em-0.12em）
- 颜色用 muted

---

## 4. 间距与圆角

### 间距规则

| 场景 | 间距 |
|------|------|
| 页面 padding | 32px (`p-8`) |
| 面板内 padding | 22px (`p-5` 或 `p-6`) |
| 组件之间 | 24px (`gap-6`) |
| 面板内元素之间 | 14-16px (`gap-3.5` 或 `gap-4`) |
| 紧凑列表项间距 | 8px (`gap-2`) |

### 圆角规则

| 元素 | 圆角 |
|------|------|
| 页面级容器（hero） | 24px (`rounded-3xl`) |
| 面板 | 22px (`rounded-[22px]`) |
| 卡片、指标卡 | 20px (`rounded-[20px]`) |
| 表格行、列表项 | 16px (`rounded-2xl`) |
| 按钮、输入框 | 14px (`rounded-[14px]`) |
| 小标签（Badge） | 999px (`rounded-full`) |

### 禁止
- 不要用 `rounded-sm`（4px）或 `rounded-md`（6px），整体风格偏大圆角
- 不要在同一个面板里混用差距过大的圆角值

---

## 5. 布局规范

### 整体结构
```
.shell = Grid: 300px sidebar + 1fr content
```
- 侧边栏宽度固定 300px，sticky 定位
- 内容区最小宽度 0（`min-w-0` 防止溢出）
- 内容区 padding 32px

### 页面内部布局
- 每个页面是一个垂直 grid，gap 24px
- 顶部必须有 hero 区域（标题 + 描述）
- 指标卡用 4 列 grid（移动端降为 1 列）
- 内容区用 1 列或 2 列 grid

### 响应式断点
- `<= 1024px`：侧边栏折叠到顶部，内容区全宽
- 只需要这一个断点，不要做更多

---

## 6. 组件规范（shadcn 组件选用）

### 必须使用的 shadcn 组件

| 场景 | 组件 | 说明 |
|------|------|------|
| 数据表格 | `Table` | 工具列表、审计日志、审批列表 |
| 表单 | `Form` + `Input` + `Select` + `Textarea` | 工具调用、策略编辑 |
| 按钮 | `Button` | 所有可点击操作 |
| 对话框 | `Dialog` | 确认操作、详情弹窗 |
| 标签 | `Badge` | 状态标记（success/pending/denied） |
| 卡片 | `Card` | 指标卡、面板容器 |
| 标签页 | `Tabs` | 详情页多视图切换 |
| Toast | `Sonner` | 操作反馈（成功/失败） |
| 下拉菜单 | `DropdownMenu` | 更多操作 |
| 代码展示 | 自定义 `<pre>` + 等宽字体 | JSON schema、SQL、审计详情 |

### 不要使用的组件
- `Accordion`：管理台不需要折叠面板
- `Carousel`：无轮播场景
- `Calendar` / `DatePicker`：MVP 不需要日期筛选
- `Drawer`：不要侧边抽屉，用 Dialog 代替

---

## 7. 按钮层级

| 层级 | 样式 | 用途 |
|------|------|------|
| Primary | 渐变背景（teal→blue），深色文字，font-weight 700 | 主操作：提交、确认、审批通过 |
| Secondary/Ghost | 透明背景 + border | 次要操作：取消、查看详情、筛选 |
| Destructive | danger 色背景或 danger 色文字 | 拒绝审批、删除 |
| Icon | 透明背景，无 border，仅图标 | 刷新、复制、关闭 |

### 按钮规则
- 一个视图区域最多一个 Primary 按钮
- 按钮内容尽量 2-4 个字（"提交"、"审批"、"拒绝"）
- 不要用纯文字链接代替按钮做操作

---

## 8. 状态标签（Badge）规范

| 状态 | 颜色 | 对应场景 |
|------|------|----------|
| success / allow / approved | teal 背景低透明度 + teal 文字 | 成功、允许、已审批 |
| pending / approval_required | blue 背景低透明度 + blue 文字 | 等待中、需要审批 |
| failed / denied / rejected | rose 背景低透明度 + rose 文字 | 失败、拒绝、已驳回 |
| read / low | 白色低透明度 + muted 文字 | 只读、低风险 |
| write / medium / high | blue 或 rose 视风险程度 | 写操作、中高风险 |

### Badge 样式
```
背景：语义色 10-15% 透明度
文字：语义色全饱和
圆角：rounded-full
字号：text-xs
padding：px-2.5 py-0.5
```

---

## 9. 表格规范

### 表格样式
- 不要传统的水平分割线
- 每行是一个独立的"行卡片"：16px 圆角、hover 边框高亮
- 表头：muted 色、uppercase、text-xs、tracking-wider
- 行间距：8px gap

### 表格交互
- hover 时整行边框变为 `border-color: rgba(96, 165, 250, 0.18)`
- 可点击行要有 `cursor-pointer`
- 点击后跳转到详情页或弹出 Dialog

### 表格列排列建议
- 第 1 列：名称/标识（最宽，2fr）
- 中间列：属性/状态（各 1fr）
- 最后列：操作按钮（固定宽度）

---

## 10. 表单规范

### 表单布局
- 单列垂直排列，gap 14px
- label 在上，input 在下
- label 颜色用 muted

### 输入框样式
- 背景：`rgba(2, 6, 23, 0.55)` 半透明深色
- 边框：`rgba(148, 163, 184, 0.18)` 灰色低透明度
- 圆角：14px
- padding：12px 14px
- focus 状态：边框变为 accent-2 色

### 表单校验
- 错误信息用 danger 色，显示在输入框下方
- 不要用红色边框（太刺眼），只改文字颜色

---

## 11. 空状态与加载状态

### 空状态
- 居中文字："暂无数据" / "No items"
- muted 颜色
- 可选加一行操作引导（如 "点击上方按钮创建第一个工具"）
- 不要用大图标或插画（保持简洁）

### 加载状态
- 页面级加载：内容区居中显示 "Loading..."（muted 色）
- 按钮加载：文字改为 "Processing..." + 禁用状态
- 不要用 skeleton screen（实现成本高，MVP 不需要）

---

## 12. 动画与过渡

### 允许的过渡
- hover 边框/背景色变化：`transition-all duration-150 ease`（0.15s）
- 面板展开/Dialog 出现：`animate-in fade-in`（shadcn 默认）

### 禁止
- 不要加页面切换动画
- 不要加列表项入场动画（stagger）
- 不要加数字跳动动画
- 不要用 `animate-bounce` / `animate-pulse`
- 整体保持静态、稳重、企业感

---

## 13. 面板与卡片规范

### 面板（Panel）
用于包裹一组相关内容。

```
背景：var(--panel) / rgba(11, 19, 35, 0.88)
边框：1px solid var(--panel-border)
圆角：22px
padding：22px
阴影：0 24px 60px rgba(2, 6, 23, 0.45)
backdrop-filter：blur(18px)
```

### 面板结构
```
Panel
├── Panel Header（标题 + 描述 + 操作按钮）
├── Panel Body（表格 / 表单 / 列表）
└── Panel Footer（可选，分页或汇总）
```

### Hero 区域
每个页面顶部的标题区域：
```
背景：渐变叠加 + radial-gradient 点缀
圆角：24px
padding：24px
内容：eyebrow 标签 + h1 标题 + p 描述
```

---

## 14. 代码/JSON 展示规范

### 代码块样式
```
背景：rgba(1, 6, 12, 0.72)
边框：1px solid rgba(148, 163, 184, 0.14)
圆角：16px
padding：16px
字体：monospace
颜色：#dbeafe（浅蓝白色）
overflow：auto
```

### JSON 格式化
- 工具的 input_schema、审计日志的 payload 等用 `JSON.stringify(obj, null, 2)` 格式化
- 最大高度 300px，超出滚动
- 可选加"复制"按钮（icon button 在右上角）

---

## 15. 图标规范

### 图标库
使用 `lucide-react`（shadcn 默认推荐，与 shadcn 组件一致）

### 图标使用规则
- 大小：16px（表格内、Badge 旁）或 20px（按钮内、导航）
- 颜色：继承父元素文字颜色
- 不要用彩色图标
- 不要用 emoji 代替图标

### 常用图标映射

| 场景 | 图标 |
|------|------|
| 工具/扳手 | `Wrench` |
| 数据库 | `Database` |
| GitHub | `Github` |
| HTTP/API | `Globe` |
| 审批 | `ShieldCheck` |
| 审计日志 | `FileText` |
| Dashboard | `LayoutDashboard` |
| 成功 | `CheckCircle` |
| 失败 | `XCircle` |
| 等待 | `Clock` |
| 风险 | `AlertTriangle` |
| 策略 | `Lock` |

---

## 16. 新页面模板

所有新页面应遵循以下结构：

```tsx
export default function PageName() {
  return (
    <div className="page">
      {/* Hero 区域 */}
      <div className="page__hero">
        <span className="eyebrow">SECTION NAME</span>
        <h1>页面标题</h1>
        <p>一句话描述这个页面的用途。</p>
      </div>

      {/* 指标卡（可选） */}
      <div className="metrics-grid">
        {/* MetricCard x N */}
      </div>

      {/* 内容面板 */}
      <div className="panel">
        <div className="panel__header">
          <div>
            <h2>面板标题</h2>
            <p>面板描述</p>
          </div>
          <Button>操作</Button>
        </div>
        {/* 表格 / 表单 / 列表 */}
      </div>
    </div>
  );
}
```

---

## 17. 禁止事项清单

以下行为在本项目中**严格禁止**：

1. **不要用浅色/白色背景**——整个应用只有深色主题
2. **不要用彩色背景卡片**——所有面板/卡片背景是统一的深色半透明
3. **不要加 border-radius 小于 14px 的圆角**——保持大圆角风格
4. **不要用 box-shadow 模拟立体感**——用 border + backdrop-filter 营造层次
5. **不要在一个页面放超过 4 个指标卡**——信息过载
6. **不要用 grid 超过 4 列**——保持可读性
7. **不要用 toast 做关键信息提示**——审批结果等重要反馈用 Dialog
8. **不要自创配色**——只用规范中定义的 teal/blue/rose 三色
9. **不要加装饰性元素**——无 divider line、无装饰图标、无背景图案
10. **不要用 `!important`**——如果样式冲突，检查 specificity 或层级

---

## 18. shadcn/ui 接入步骤（给实现 AI 的指引）

```bash
# 1. 安装 Tailwind CSS v4
cd frontend
npm install tailwindcss @tailwindcss/vite

# 2. 配置 vite.config.ts 加入 tailwindcss plugin
# 3. 创建 app.css 引入 @import "tailwindcss"

# 4. 安装 shadcn/ui 依赖
npx shadcn@latest init

# 选项：
#   Style: Default
#   Base color: Slate
#   CSS variables: yes

# 5. 覆盖 shadcn 生成的 CSS variables 为我们的色板
# 6. 按需安装组件
npx shadcn@latest add button card table badge dialog form input select tabs toast dropdown-menu
```

### 关键配置注意
- `tailwind.config` 中 `darkMode: "class"` 并在 `<html>` 上永久加 `class="dark"`
- shadcn 的 `globals.css` 中只保留 `.dark {}` 配色，删除 `:root {}` 的浅色定义
- 保留现有 `styles.css` 不动，新 Tailwind 样式通过 `app.css` 引入

---

## 19. 质量检查清单

每次前端 PR 自查：

- [ ] 颜色是否只用了 teal/blue/rose + 灰色系？
- [ ] 圆角是否 >= 14px？
- [ ] 面板是否有统一的边框 + 半透明背景 + blur？
- [ ] 表格行是否 hover 有边框高亮？
- [ ] 空状态是否有处理？
- [ ] 按钮层级是否合理（一个区域最多一个 Primary）？
- [ ] Badge 颜色是否对应正确的语义？
- [ ] 是否有多余的动画？
- [ ] 移动端 1024px 断点是否正常？
- [ ] TypeScript 无报错？

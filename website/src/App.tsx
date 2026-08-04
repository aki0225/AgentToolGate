import { useEffect, useState, type ReactNode } from "react";

import { ArchitectureFlow } from "./components/ArchitectureFlow";
import { CapabilityGrid } from "./components/CapabilityGrid";
import { HeroPipeline } from "./components/HeroPipeline";
import { Icon, type IconName } from "./components/Icon";
import { ScenarioTabs } from "./components/ScenarioTabs";
import { SecurityBoundary } from "./components/SecurityBoundary";

const githubRoot = "https://github.com/aki0225/AgentToolGate";
const githubBlobRoot = `${githubRoot}/blob/main`;
const latestDownloadRoot = `${githubRoot}/releases/latest/download`;

const navItems = [
  { label: "产品能力", href: "#capabilities" },
  { label: "交互演示", href: "#demo" },
  { label: "架构", href: "#architecture" },
  { label: "安全边界", href: "#boundaries" },
  { label: "工程证据", href: "#evidence" },
  { label: "下载", href: "#download" }
];

const heroFacts = [
  "Windows / Linux",
  "单二进制 + SQLite",
  "MCP Inbound HTTP",
  "MIT License",
  "Go + Node CI"
];

const evidenceRows: Array<{
  code: string;
  title: string;
  detail: string;
  proof: string;
  href: string;
  linkLabel: string;
}> = [
  {
    code: "STACK",
    title: "Go + React + TypeScript",
    detail: "Go 后端、React 控制台和独立 Vite 展示站按发布边界分离。",
    proof: "backend/go.mod · frontend/package.json · website/package.json",
    href: `${githubRoot}/tree/main`,
    linkLabel: "查看源码树"
  },
  {
    code: "STORE",
    title: "SQLite / PostgreSQL / MemoryStore",
    detail: "本地单二进制优先 SQLite，同时保留 PostgreSQL 集成路径与测试内存存储。",
    proof: "internal/store · PostgreSQL CI service",
    href: `${githubBlobRoot}/docs/architecture.md`,
    linkLabel: "查看架构说明"
  },
  {
    code: "RELEASE",
    title: "Windows / Linux amd64 + SHA256",
    detail: "双平台发布产物由 GitHub Release 承载，展示站不复制二进制。",
    proof: "agenttoolgate-windows-amd64.zip · agenttoolgate-linux-amd64.tar.gz · SHA256SUMS",
    href: `${githubRoot}/releases`,
    linkLabel: "查看 Releases"
  },
  {
    code: "CI",
    title: "可复述的质量门禁",
    detail: "Go test/vet、PostgreSQL 集成测试、TypeScript check、生产构建和 E2E 分层执行。",
    proof: ".github/workflows/ci.yml",
    href: `${githubRoot}/actions/workflows/ci.yml`,
    linkLabel: "查看 CI 定义"
  },
  {
    code: "IDENTITY",
    title: "OIDC、基础 role 与 workspace 隔离",
    detail: "已有基础身份与隔离控制，但不宣传为企业级 RBAC 或完整职责分离。",
    proof: "internal/auth · internal/app/rbac_test.go",
    href: `${githubBlobRoot}/docs/security-review-notes.md`,
    linkLabel: "查看安全评审"
  },
  {
    code: "OBSERVE",
    title: "OpenTelemetry + Prometheus",
    detail: "trace id 关联治理证据，指标覆盖调用、拒绝、错误、耗时与 Guard 规则命中。",
    proof: "internal/telemetry",
    href: `${githubRoot}/tree/main/backend/internal/telemetry`,
    linkLabel: "查看可观测性实现"
  },
  {
    code: "REVIEW",
    title: "Threat Model / Security Review / Acceptance",
    detail: "公开记录已有控制、剩余风险、日常使用证据和生产化前提。",
    proof: "docs/threat-model.md · docs/security-review-notes.md · docs/daily-use-acceptance.md",
    href: `${githubBlobRoot}/docs/threat-model.md`,
    linkLabel: "查看威胁模型"
  }
];

const downloads: Array<{
  platform: string;
  filename: string;
  href: string;
  icon: IconName;
}> = [
  {
    platform: "Windows amd64",
    filename: "agenttoolgate-windows-amd64.zip",
    href: `${latestDownloadRoot}/agenttoolgate-windows-amd64.zip`,
    icon: "download"
  },
  {
    platform: "Linux amd64",
    filename: "agenttoolgate-linux-amd64.tar.gz",
    href: `${latestDownloadRoot}/agenttoolgate-linux-amd64.tar.gz`,
    icon: "download"
  },
  {
    platform: "完整性校验",
    filename: "SHA256SUMS",
    href: `${latestDownloadRoot}/SHA256SUMS`,
    icon: "lock"
  }
];

function ExternalLink({
  children,
  className,
  href
}: {
  children: ReactNode;
  className?: string;
  href: string;
}) {
  return (
    <a className={className} href={href} rel="noreferrer" target="_blank">
      {children}
    </a>
  );
}

function SectionHeading({
  index,
  eyebrow,
  title,
  description
}: {
  index: string;
  eyebrow: string;
  title: string;
  description: string;
}) {
  return (
    <div className="section-heading">
      <div className="section-index">{index}</div>
      <div>
        <span className="section-eyebrow">{eyebrow}</span>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
    </div>
  );
}

export function App() {
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  useEffect(() => {
    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setMobileMenuOpen(false);
      }
    }

    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, []);

  return (
    <>
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>

      <header className="site-header">
        <div className="site-header-inner">
          <a className="brand" href="#top" aria-label="返回 AgentToolGate 展示站首页">
            <span className="brand-mark" aria-hidden="true">
              <span />
              <span />
            </span>
            <span className="brand-copy">
              <strong>AgentToolGate</strong>
              <small>TOOL GOVERNANCE GATEWAY</small>
            </span>
          </a>

          <nav className="desktop-nav" aria-label="主导航">
            {navItems.map((item) => (
              <a href={item.href} key={item.href}>
                {item.label}
              </a>
            ))}
            <ExternalLink href={githubRoot}>
              GitHub
              <Icon name="external" />
            </ExternalLink>
          </nav>

          <button
            aria-controls="mobile-navigation"
            aria-expanded={mobileMenuOpen}
            aria-label={mobileMenuOpen ? "关闭导航菜单" : "打开导航菜单"}
            className="mobile-menu-button"
            onClick={() => setMobileMenuOpen((open) => !open)}
            type="button"
          >
            <Icon name={mobileMenuOpen ? "close" : "menu"} />
          </button>
        </div>

        {mobileMenuOpen ? (
          <nav className="mobile-nav" id="mobile-navigation" aria-label="移动端导航">
            {navItems.map((item) => (
              <a href={item.href} key={item.href} onClick={() => setMobileMenuOpen(false)}>
                {item.label}
              </a>
            ))}
            <ExternalLink href={githubRoot}>
              GitHub
              <Icon name="external" />
            </ExternalLink>
          </nav>
        ) : null}
      </header>

      <main id="main-content">
        <section className="hero section-shell" id="top">
          <div className="hero-copy">
            <div className="hero-eyebrow hero-enter hero-enter-1">
              <span>LOCAL-FIRST</span>
              <i aria-hidden="true" />
              <span>FAIL-CLOSED</span>
              <i aria-hidden="true" />
              <span>AUDITABLE</span>
            </div>
            <h1 className="hero-enter hero-enter-2">
              让 AI Agent 的高危动作，
              <span>在真正执行前先过治理闸门</span>
            </h1>
            <p className="hero-lead hero-enter hero-enter-3">
              面向 Codex、Claude Code 与 MCP 客户端的本地工具治理网关：Policy、Approval、
              Connector Secret、Audit 与 Local Action Firewall。
            </p>
            <p className="hero-english hero-enter hero-enter-3">
              Govern high-risk tool calls before execution.
            </p>
            <div className="hero-actions hero-enter hero-enter-4">
              <a className="button button-primary" href="#demo">
                查看交互演示
                <Icon name="arrow" />
              </a>
              <a className="button button-secondary" href="#download">
                下载最新 Release
                <Icon name="download" />
              </a>
              <ExternalLink className="button button-ghost" href={githubRoot}>
                查看源码
                <Icon name="github" />
              </ExternalLink>
            </div>
            <div className="hero-boundary hero-enter hero-enter-4">
              <Icon name="lock" />
              <p>
                本站是静态产品展示，不运行 AgentToolGate 后端，不连接真实 Connector，不收集
                token、Secret 或表单信息。
              </p>
            </div>
            <ul className="hero-facts hero-enter hero-enter-5" aria-label="产品摘要">
              {heroFacts.map((fact) => (
                <li key={fact}>{fact}</li>
              ))}
            </ul>
          </div>

          <div className="hero-visual hero-enter hero-enter-3">
            <HeroPipeline />
          </div>
        </section>

        <section className="section section-shell" id="capabilities">
          <SectionHeading
            index="01"
            eyebrow="PROBLEM / GOVERNANCE"
            title="危险后果发生在工具落地那一刻"
            description="AgentToolGate 不承诺消灭提示词注入；它把受支持的工具调用收敛到可解释、可审批、可审计的执行路径。"
          />
          <CapabilityGrid />
        </section>

        <section className="section section-shell section-demo" id="demo">
          <SectionHeading
            index="02"
            eyebrow="INTERACTIVE / STATIC"
            title="三条可复现的治理状态机"
            description="每个场景都有独立内存状态、合法迁移和完全重置；不使用真实 API，也不靠定时脚本伪造网络过程。"
          />
          <ScenarioTabs />
        </section>

        <section className="section section-shell" id="architecture">
          <SectionHeading
            index="03"
            eyebrow="ARCHITECTURE / TRUST BOUNDARY"
            title="两条编排轨道，同一套治理原则"
            description="Tool Registry 调用进入统一 Policy / Approval / Runtime 链路；Local Guard 走独立入口和一次性票据流程。"
          />
          <ArchitectureFlow />
        </section>

        <section className="section section-shell" id="evidence">
          <SectionHeading
            index="04"
            eyebrow="ENGINEERING / EVIDENCE"
            title="不靠夸张数字，直接给出可核对证据"
            description="技术栈、状态存储、发布产物、质量门禁、身份边界和安全文档都对应仓库中的实现或配置。"
          />

          <div className="evidence-ledger">
            <div className="evidence-ledger-header" aria-hidden="true">
              <span>索引</span>
              <span>工程能力</span>
              <span>仓库证据</span>
              <span>入口</span>
            </div>
            {evidenceRows.map((item, index) => (
              <article className="evidence-row" key={item.code}>
                <div className="evidence-code">
                  <span>{String(index + 1).padStart(2, "0")}</span>
                  <code>{item.code}</code>
                </div>
                <div className="evidence-main">
                  <h3>{item.title}</h3>
                  <p>{item.detail}</p>
                </div>
                <code className="evidence-proof">{item.proof}</code>
                <ExternalLink className="evidence-link" href={item.href}>
                  {item.linkLabel}
                  <Icon name="external" />
                </ExternalLink>
              </article>
            ))}
          </div>

          <div className="evidence-doc-strip">
            <span>继续审阅</span>
            <ExternalLink href={`${githubBlobRoot}/docs/architecture.md`}>架构说明</ExternalLink>
            <ExternalLink href={`${githubBlobRoot}/docs/threat-model.md`}>威胁模型</ExternalLink>
            <ExternalLink href={`${githubBlobRoot}/docs/security-review-notes.md`}>
              安全评审
            </ExternalLink>
            <ExternalLink href={`${githubBlobRoot}/docs/daily-use-acceptance.md`}>
              Daily Use Acceptance
            </ExternalLink>
          </div>
        </section>

        <section className="section section-shell" id="boundaries">
          <SectionHeading
            index="05"
            eyebrow="SECURITY / DISCLOSURE"
            title="Guardrail 不是操作系统沙箱"
            description="把能做、不能替代和当前限制一起摆出来，避免把本地 MVP 误解成完整企业安全平台。"
          />
          <SecurityBoundary />
        </section>

        <section className="section section-shell section-download" id="download">
          <div className="download-heading">
            <span className="section-eyebrow">RELEASE / LOCAL FIRST</span>
            <h2>下载二进制，三条命令完成本地接入</h2>
            <p>
              Release 文件和 SHA256 校验由 GitHub 托管。默认 hook mode 为 dry-run，确认结果后再显式进入真实阻断。
            </p>
          </div>

          <div className="download-layout">
            <div className="download-list">
              {downloads.map((download) => (
                <ExternalLink className="download-item" href={download.href} key={download.filename}>
                  <Icon name={download.icon} />
                  <span>
                    <strong>{download.platform}</strong>
                    <code>{download.filename}</code>
                  </span>
                  <Icon name="arrow" />
                </ExternalLink>
              ))}
              <ExternalLink className="release-index-link" href={`${githubRoot}/releases`}>
                查看全部版本与发布说明
                <Icon name="external" />
              </ExternalLink>
            </div>

            <div className="quickstart-terminal" aria-label="Windows 快速开始命令">
              <div className="terminal-bar">
                <span>
                  <i />
                  <i />
                  <i />
                </span>
                <code>PowerShell / 项目根目录</code>
              </div>
              <div className="terminal-body">
                <p>
                  <span>01</span>
                  <code>.\agenttoolgate.exe doctor</code>
                </p>
                <p>
                  <span>02</span>
                  <code>.\agenttoolgate.exe init all</code>
                </p>
                <p>
                  <span>03</span>
                  <code>.\agenttoolgate.exe up --open</code>
                </p>
                <small>Linux 使用 ./agenttoolgate，参数相同。</small>
              </div>
            </div>
          </div>

          <div className="download-docs">
            <div>
                <Icon name="terminal" />
                <span>
                  <strong>接入 Codex / Claude Code</strong>
                  <small>MCP Inbound Streamable HTTP /mcp，旧客户端按文档使用 SSE fallback。</small>
                </span>
            </div>
            <ExternalLink href={`${githubBlobRoot}/docs/ai-client-integration.md`}>
              打开接入文档
              <Icon name="external" />
            </ExternalLink>
          </div>
        </section>
      </main>

      <footer className="site-footer">
        <div>
          <a className="brand brand-footer" href="#top">
            <span className="brand-mark" aria-hidden="true">
              <span />
              <span />
            </span>
            <span className="brand-copy">
              <strong>AgentToolGate</strong>
              <small>执行前治理，执行后留痕</small>
            </span>
          </a>
          <p>本地 AI Agent 工具调用治理网关。MIT License。</p>
        </div>
        <div className="footer-links">
          <ExternalLink href={githubRoot}>GitHub</ExternalLink>
          <ExternalLink href={`${githubRoot}/releases`}>Releases</ExternalLink>
          <ExternalLink href={`${githubBlobRoot}/docs/threat-model.md`}>威胁模型</ExternalLink>
          <ExternalLink href={`${githubRoot}/blob/main/LICENSE`}>许可证</ExternalLink>
        </div>
        <span className="footer-note">静态展示 · 无真实 API · 无数据采集 · 2026</span>
      </footer>
    </>
  );
}

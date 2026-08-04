import { useEffect, useState, type ReactNode } from "react";

import { ArchitectureFlow } from "./components/ArchitectureFlow";
import { HeroPipeline } from "./components/HeroPipeline";
import { Icon, type IconName } from "./components/Icon";
import { ScenarioTabs } from "./components/ScenarioTabs";
import { SecurityBoundary } from "./components/SecurityBoundary";

const githubRoot = "https://github.com/aki0225/AgentToolGate";
const githubBlobRoot = `${githubRoot}/blob/main`;
const latestDownloadRoot = `${githubRoot}/releases/latest/download`;

const navItems = [
  { label: "工作方式", href: "#workflow" },
  { label: "交互演示", href: "#demo" },
  { label: "安全边界", href: "#boundaries" },
  { label: "下载", href: "#download" }
];

const proofLinks = [
  {
    label: "CI 与测试",
    href: `${githubRoot}/actions/workflows/ci.yml`
  },
  {
    label: "架构文档",
    href: `${githubBlobRoot}/docs/architecture.md`
  },
  {
    label: "安全评审",
    href: `${githubBlobRoot}/docs/security-review-notes.md`
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
    platform: "SHA256",
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
  title,
  description
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="section-heading">
      <h2>{title}</h2>
      <p>{description}</p>
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
              <small>本地工具治理网关</small>
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
            <p className="hero-brand">AgentToolGate</p>
            <h1>
              让高危工具调用，
              <span>
                在执行前先过
                <br />
                治理闸门
              </span>
            </h1>
            <p className="hero-lead">
              面向 Codex、Claude Code 与 MCP 客户端的本地工具治理网关，统一处理决策、审批、运行时 Secret 与脱敏审计。
            </p>
            <div className="hero-actions">
              <a className="button button-primary" href="#demo">
                查看交互演示
                <Icon name="arrow" />
              </a>
              <a className="button button-secondary" href="#download">
                下载 Release
                <Icon name="download" />
              </a>
            </div>
          </div>

          <div className="hero-visual">
            <HeroPipeline />
          </div>
        </section>

        <section className="section section-shell" id="workflow">
          <SectionHeading
            title="它如何工作"
            description="工具调用先被判定，再决定执行、审批或拒绝；本地高危动作走独立但同样保守的 Guard 入口。"
          />
          <ArchitectureFlow />

          <div className="proof-links" aria-label="工程证据">
            <span>继续核对</span>
            {proofLinks.map((item) => (
              <ExternalLink href={item.href} key={item.label}>
                {item.label}
                <Icon name="external" />
              </ExternalLink>
            ))}
          </div>
        </section>

        <section className="section section-shell section-demo" id="demo">
          <SectionHeading
            title="交互演示"
            description="三个 synthetic 场景展示真实治理状态迁移，不连接后端、上游服务或真实凭据。"
          />
          <ScenarioTabs />
        </section>

        <section className="section section-shell" id="boundaries">
          <SectionHeading
            title="安全边界"
            description="AgentToolGate 是执行前 guardrail，不是操作系统沙箱，也不能替代生产身份、网络与最小权限控制。"
          />
          <SecurityBoundary />
        </section>

        <section className="section section-shell section-download" id="download">
          <SectionHeading
            title="下载并开始使用"
            description="Release 提供 Windows 与 Linux amd64 产物和 SHA256；默认 dry-run，确认结果后再显式进入 live。"
          />

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
                查看全部版本
                <Icon name="external" />
              </ExternalLink>
            </div>

            <div className="quickstart-terminal" aria-label="Windows 快速开始命令">
              <div className="terminal-bar">
                <strong>快速开始</strong>
                <span>PowerShell · 项目根目录</span>
              </div>
              <div className="terminal-body">
                <code>.\agenttoolgate.exe doctor</code>
                <code>.\agenttoolgate.exe init codex</code>
                <code>.\agenttoolgate.exe up --open</code>
                <small>Claude 用户将第二条改为 init claude；Linux 使用 ./agenttoolgate。</small>
              </div>
            </div>
          </div>

          <ExternalLink
            className="integration-link"
            href={`${githubBlobRoot}/docs/ai-client-integration.md`}
          >
            阅读 Codex / Claude Code 接入文档
            <Icon name="external" />
          </ExternalLink>
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
      </footer>
    </>
  );
}

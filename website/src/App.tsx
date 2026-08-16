import { useEffect, useState, type ReactNode } from "react";

import { ArchitectureFlow } from "./components/ArchitectureFlow";
import { EvaluationProof } from "./components/EvaluationProof";
import { HeroPipeline } from "./components/HeroPipeline";
import { Icon, type IconName } from "./components/Icon";
import { SecurityBoundary } from "./components/SecurityBoundary";

const githubRoot = "https://github.com/aki0225/AgentToolGate";
const githubBlobRoot = `${githubRoot}/blob/main`;
const latestDownloadRoot = `${githubRoot}/releases/latest/download`;
const releaseAcceptanceUrl = `${githubBlobRoot}/docs/v0.4.1-release-acceptance.md`;

const navItems = [
  { label: "工作方式", href: "#workflow" },
  { label: "实测", href: "#evaluation" },
  { label: "安全边界", href: "#boundaries" },
  { label: "下载", href: "#download" }
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
            <div className="hero-brand">
              <span>AgentToolGate</span>
              <ExternalLink href={releaseAcceptanceUrl}>Stable v0.4.1</ExternalLink>
            </div>
            <h1>
              让高危工具调用，
              <span>
                在执行前先过
                <br />
                治理闸门
              </span>
            </h1>
            <p className="hero-lead">
              在 Agent 执行工具前，统一完成判定、审批、Secret 注入与脱敏审计。
            </p>
            <div className="hero-actions">
              <a className="button button-primary" href="#real-codex-proof">
                查看真实 Codex 验收
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
            description="一次调用，经过判定、审批、执行与审计。"
          />
          <ArchitectureFlow />
        </section>

        <EvaluationProof />

        <section className="section section-shell" id="boundaries">
          <SectionHeading
            title="安全边界"
            description="它是执行前 guardrail，不是操作系统沙箱。"
          />
          <SecurityBoundary />
        </section>

        <section className="section section-shell section-download" id="download">
          <SectionHeading
            title="下载并开始使用"
            description="Windows、Linux amd64 与 SHA256；默认 dry-run，确认后再进入 live。"
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
              <ExternalLink
                className="release-index-link"
                href={`${githubRoot}/releases/tag/v0.4.1`}
              >
                下载可复跑评估附件
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

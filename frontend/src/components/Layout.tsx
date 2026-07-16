import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { Menu, ShieldCheck, X } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { canManageConnectors, canManageSecrets, canViewApprovals, canViewAudit, canViewDashboard, canViewPolicies, canViewTools } from "../auth/permissions";
import { connectApprovalStream, listApprovals } from "../api/client";
import { useI18n, type Locale, type TranslationKey } from "../i18n";
import { cn } from "../lib/utils";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

const navLinks = [
  { to: "/", labelKey: "layout.nav.dashboard" },
  { to: "/tools", labelKey: "layout.nav.tools" },
  { to: "/connectors", labelKey: "layout.nav.connectors" },
  { to: "/policies", labelKey: "layout.nav.policies" },
  { to: "/secrets", labelKey: "layout.nav.secrets" },
  { to: "/approvals", labelKey: "layout.nav.approvals" },
  { to: "/audit", labelKey: "layout.nav.audit" },
] satisfies Array<{ to: string; labelKey: TranslationKey }>;

export function Layout() {
  const auth = useAuth();
  const { locale, setLocale, t } = useI18n();
  const location = useLocation();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const role = auth.me?.user.role;
  const canReadApprovals = canViewApprovals(role);
  const visibleNavLinks = navLinks.filter((item) => {
    switch (item.to) {
      case "/":
        return canViewDashboard(role);
      case "/tools":
        return canViewTools(role);
      case "/connectors":
        return canManageConnectors(role);
      case "/policies":
        return canViewPolicies(role);
      case "/secrets":
        return canManageSecrets(role);
      case "/approvals":
        return canReadApprovals;
      case "/audit":
        return canViewAudit(role);
      default:
        return false;
    }
  });
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [pendingApprovalCount, setPendingApprovalCount] = useState(0);
  const nextLocale: Locale = locale === "en-US" ? "zh-CN" : "en-US";
  const nextLocaleLabel = nextLocale === "zh-CN" ? "中文" : "EN";

  useEffect(() => {
    setMobileNavOpen(false);
  }, [location.pathname]);

  useEffect(() => {
    let cancelled = false;
    let pollingTimer: number | null = null;
    let safetySyncTimer: number | null = null;
    let streamConnection: { close: () => void } | null = null;

    async function loadPendingApprovals() {
      if (!workspaceOrgId || !canReadApprovals) {
        if (!cancelled) {
          setPendingApprovalCount(0);
        }
        return;
      }
      try {
        const result = await listApprovals(token, workspaceOrgId);
        if (!cancelled) {
          setPendingApprovalCount(result.items.filter((approval) => approval.status === "pending").length);
        }
      } catch {
        if (!cancelled) {
          setPendingApprovalCount(0);
        }
      }
    }

    function startPollingFallback() {
      if (pollingTimer !== null) {
        return;
      }
      void loadPendingApprovals();
      pollingTimer = window.setInterval(() => {
        void loadPendingApprovals();
      }, 15000);
    }

    void loadPendingApprovals();
    if (!workspaceOrgId || !canReadApprovals) {
      return () => {
        cancelled = true;
        if (pollingTimer !== null) {
          window.clearInterval(pollingTimer);
        }
      };
    }

    try {
      streamConnection = connectApprovalStream({
        token,
        workspaceOrgId,
        onApproval: () => {
          void loadPendingApprovals();
        },
        onOpen: () => {
          if (pollingTimer !== null) {
            window.clearInterval(pollingTimer);
            pollingTimer = null;
          }
          if (safetySyncTimer !== null) {
            window.clearTimeout(safetySyncTimer);
          }
          safetySyncTimer = window.setTimeout(() => {
            void loadPendingApprovals();
          }, 1000);
        },
        onError: () => {
          if (streamConnection) {
            streamConnection.close();
            streamConnection = null;
          }
          startPollingFallback();
        },
      });
    } catch {
      startPollingFallback();
      return () => {
        cancelled = true;
        if (pollingTimer !== null) {
          window.clearInterval(pollingTimer);
        }
      };
    }

    return () => {
      cancelled = true;
      if (streamConnection) {
        streamConnection.close();
      }
      if (pollingTimer !== null) {
        window.clearInterval(pollingTimer);
      }
      if (safetySyncTimer !== null) {
        window.clearTimeout(safetySyncTimer);
      }
    };
  }, [auth.authMode, canReadApprovals, token, workspaceOrgId]);

  return (
    <div className="min-h-screen bg-background lg:grid lg:grid-cols-[300px_minmax(0,1fr)]">
      <header className="flex items-center justify-between border-b border-border bg-[linear-gradient(180deg,rgba(7,17,31,0.92),rgba(7,17,31,0.72))] px-4 py-3 backdrop-blur-2xl lg:hidden">
        <Button type="button" variant="outline" size="icon" onClick={() => setMobileNavOpen((current) => !current)}>
          {mobileNavOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
        </Button>
        <div className="flex items-center gap-2">
          <div className="grid h-10 w-10 place-items-center rounded-[14px] bg-gradient-to-br from-primary to-accent text-xs font-extrabold tracking-[0.08em] text-primary-foreground">
            AG
          </div>
          <div>
            <div className="font-bold text-foreground">AgentToolGate</div>
            <div className="text-xs text-muted-foreground">{t("layout.tagline")}</div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label={nextLocale === "zh-CN" ? t("layout.language.switchToZh") : t("layout.language.switchToEn")}
            onClick={() => setLocale(nextLocale)}
          >
            {nextLocaleLabel}
          </Button>
          <Badge variant="pending" className="gap-1">
            <ShieldCheck className="h-3.5 w-3.5" />
            {pendingApprovalCount}
          </Badge>
        </div>
      </header>

      {mobileNavOpen ? (
        <button
          type="button"
          aria-label={t("layout.mobile.closeNavigation")}
          className="fixed inset-0 z-30 bg-slate-950/70 backdrop-blur-sm lg:hidden"
          onClick={() => setMobileNavOpen(false)}
        />
      ) : null}

      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-40 flex w-[300px] -translate-x-full flex-col gap-5 border-r border-border bg-[linear-gradient(180deg,rgba(7,17,31,0.96),rgba(7,17,31,0.82))] p-6 backdrop-blur-2xl transition-transform duration-200 lg:sticky lg:top-0 lg:min-h-screen lg:translate-x-0 lg:border-b-0",
          mobileNavOpen && "translate-x-0",
        )}
      >
        <div className="flex items-center gap-3.5">
          <div className="grid h-12 w-12 place-items-center rounded-[14px] bg-gradient-to-br from-primary to-accent text-sm font-extrabold tracking-[0.08em] text-primary-foreground shadow-[0_24px_60px_rgba(2,6,23,0.45)]">
            AG
          </div>
          <div>
            <div className="font-bold text-foreground">AgentToolGate</div>
            <div className="text-sm text-muted-foreground">{t("layout.tagline")}</div>
          </div>
        </div>

        <nav className="grid gap-2">
          {visibleNavLinks.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                cn(
                  "group relative flex items-center justify-between overflow-hidden rounded-[14px] border px-3.5 py-3 text-sm font-medium transition-all duration-200",
                  "border-transparent text-muted-foreground hover:border-accent/15 hover:bg-white/[0.025] hover:text-foreground hover:shadow-[0_8px_24px_rgba(96,165,250,0.08)]",
                  isActive && "border-accent/25 bg-[linear-gradient(90deg,rgba(94,234,212,0.11),rgba(96,165,250,0.045))] text-foreground shadow-[inset_0_0_0_1px_rgba(94,234,212,0.04),0_12px_30px_rgba(2,6,23,0.20)] hover:border-accent/30 hover:bg-[linear-gradient(90deg,rgba(94,234,212,0.14),rgba(96,165,250,0.06))]",
                )
              }
            >
              {({ isActive }) => (
                <>
                  <span
                    aria-hidden="true"
                    className={cn(
                      "absolute left-0 top-1/2 h-7 w-0.5 -translate-y-1/2 rounded-r-full bg-primary transition-all duration-200",
                      isActive ? "opacity-100 shadow-[0_0_18px_rgba(94,234,212,0.55)]" : "opacity-0 group-hover:opacity-40",
                    )}
                  />
                  <span className="relative">{t(item.labelKey)}</span>
                  {item.to === "/approvals" && pendingApprovalCount > 0 ? (
                    <Badge variant="pending" className="relative min-w-6 justify-center px-2">
                      {pendingApprovalCount}
                    </Badge>
                  ) : null}
                </>
              )}
            </NavLink>
          ))}
        </nav>

        <div className="rounded-[18px] border border-border/60 bg-white/[0.02] p-3.5 backdrop-blur-[18px]">
          <div className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("layout.workspace.label")}</div>
          <div className="mt-1.5 text-sm font-bold text-foreground">{auth.currentWorkspace?.name ?? t("layout.workspace.emptyName")}</div>
          <div className="mt-1 break-all text-sm text-muted-foreground">
            {auth.currentWorkspace?.zitadelOrganizationId ?? t("layout.workspace.emptyOrg")}
          </div>
        </div>

        <div className="grid gap-2 rounded-[18px] border border-border/60 bg-white/[0.02] p-3.5">
          <div className="text-xs uppercase tracking-[0.12em] text-muted-foreground">{t("layout.language.label")}</div>
          <div className="flex gap-2">
            <Button
              type="button"
              variant={locale === "en-US" ? "default" : "outline"}
              size="sm"
              onClick={() => setLocale("en-US")}
            >
              EN
            </Button>
            <Button
              type="button"
              variant={locale === "zh-CN" ? "default" : "outline"}
              size="sm"
              onClick={() => setLocale("zh-CN")}
            >
              中文
            </Button>
          </div>
        </div>

        <Button type="button" variant="outline" onClick={() => void auth.logout()}>
          {t("layout.signOut")}
        </Button>
      </aside>

      <main className="min-w-0 p-4 md:p-8 lg:p-8">
        <div className="mx-auto w-full max-w-7xl">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

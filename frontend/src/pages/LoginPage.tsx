import { Navigate } from "react-router-dom";
import { useMemo } from "react";
import { useAuth } from "../auth/AuthContext";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { useI18n } from "../i18n";

export function LoginPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const hasWorkspaces = auth.workspaces.length > 0;

  const intro = useMemo(() => {
    if (auth.authMode === "oidc") {
      return t("login.intro.oidc");
    }
    return t("login.intro.local");
  }, [auth.authMode, t]);

  if (auth.me) {
    return <Navigate to="/" replace />;
  }

  return (
    <div className="grid min-h-screen grid-cols-1 gap-6 p-8 lg:grid-cols-[minmax(280px,420px)_minmax(0,1fr)]">
      <Card className="h-fit rounded-3xl bg-[linear-gradient(135deg,rgba(12,20,35,0.95),rgba(12,20,35,0.72)),radial-gradient(circle_at_top_right,rgba(94,234,212,0.12),transparent_40%)] p-6">
        <div className="inline-flex items-center gap-2 text-xs font-bold uppercase tracking-[0.18em] text-primary">
          {t("login.kicker")}
        </div>
        <h1 className="mt-2 text-3xl font-bold tracking-tight text-foreground md:text-5xl">{t("login.title")}</h1>
        <p className="mt-3 max-w-3xl text-sm leading-6 text-muted-foreground">{intro}</p>
      </Card>

      <Card className="h-fit">
        <CardHeader>
          <CardTitle>{t("login.workspaces.title")}</CardTitle>
          <CardDescription>{t("login.workspaces.description")}</CardDescription>
        </CardHeader>
        <CardContent>
          {!hasWorkspaces && <p className="m-0 text-sm text-muted-foreground">{t("login.workspaces.loading")}</p>}
          <div className="grid gap-3 sm:grid-cols-[repeat(auto-fit,minmax(220px,1fr))]">
            {auth.workspaces.map((workspace) => (
              <button
                key={workspace.id}
                className="rounded-[18px] border border-border bg-card p-4 text-left shadow-[0_24px_60px_rgba(2,6,23,0.45)] transition-colors hover:border-accent/30"
                onClick={() => void auth.login(workspace)}
              >
                <div className="text-base font-bold text-foreground">{workspace.name}</div>
                <div className="mt-1 text-sm text-muted-foreground">{workspace.slug}</div>
                <div className="mt-2 break-all text-sm text-muted-foreground">{workspace.zitadelOrganizationId}</div>
                <div className="mt-3 font-bold text-primary">
                  {auth.authMode === "oidc" ? t("login.action.oidc") : t("login.action.local")}
                </div>
              </button>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

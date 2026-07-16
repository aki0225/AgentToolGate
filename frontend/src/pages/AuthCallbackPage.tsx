import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { ArrowLeft, CircleAlert, LoaderCircle } from "lucide-react";
import { useAuth } from "../auth/AuthContext";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { useI18n } from "../i18n";

export function AuthCallbackPage() {
  const auth = useAuth();
  const navigate = useNavigate();
  const { t } = useI18n();
  const [hasError, setHasError] = useState(false);

  useEffect(() => {
    let cancelled = false;

    async function run() {
      try {
        await auth.completeOidcLogin();
        if (!cancelled) {
          navigate("/", { replace: true });
        }
      } catch (error) {
        if (!cancelled) {
          console.error("OIDC login callback failed", error);
          setHasError(true);
        }
      }
    }

    void run();

    return () => {
      cancelled = true;
    };
  }, []);

  const steps = [
    t("authCallback.side.step1"),
    t("authCallback.side.step2"),
    t("authCallback.side.step3"),
  ];

  return (
    <div className="relative min-h-screen overflow-hidden bg-[radial-gradient(circle_at_top_right,rgba(94,234,212,0.12),transparent_28%),radial-gradient(circle_at_bottom_left,rgba(59,130,246,0.10),transparent_32%),linear-gradient(180deg,rgba(5,10,20,0.94),rgba(8,13,24,0.98))] p-6 sm:p-8">
      <div className="pointer-events-none absolute inset-x-0 top-0 h-80 bg-[radial-gradient(circle_at_center,rgba(94,234,212,0.08),transparent_60%)] blur-3xl" />
      <div className="relative mx-auto flex min-h-[calc(100vh-3rem)] w-full max-w-5xl items-center justify-center">
        <Card className="w-full overflow-hidden border-border/80 bg-[linear-gradient(135deg,rgba(12,20,35,0.96),rgba(12,20,35,0.78)),radial-gradient(circle_at_top_right,rgba(94,234,212,0.14),transparent_38%)] shadow-[0_30px_90px_rgba(2,6,23,0.5)]">
          <div className="grid gap-0 lg:grid-cols-[minmax(0,1.15fr)_minmax(280px,0.85fr)]">
            <section className="p-6 sm:p-8">
              <div className="inline-flex items-center gap-2 text-xs font-bold uppercase tracking-[0.18em] text-primary">
                {t("authCallback.kicker")}
              </div>
              <h1 className="mt-2 text-3xl font-bold tracking-tight text-foreground md:text-5xl">{t("authCallback.title")}</h1>
              <p className="mt-3 max-w-2xl text-sm leading-6 text-muted-foreground">{t("authCallback.description")}</p>

              <div className="mt-8 rounded-2xl border border-white/10 bg-white/[0.04] p-5">
                <div className="flex items-start gap-4">
                  <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl border border-border bg-background/60 text-primary">
                    {hasError ? <CircleAlert className="h-5 w-5" /> : <LoaderCircle className="h-5 w-5 animate-spin" />}
                  </div>
                  <div className="min-w-0 flex-1">
                    <Badge variant={hasError ? "destructive" : "pending"}>{hasError ? t("authCallback.error.badge") : t("authCallback.loading.badge")}</Badge>
                    <h2 className="mt-3 text-xl font-bold tracking-tight text-foreground">
                      {hasError ? t("authCallback.error.title") : t("authCallback.loading.title")}
                    </h2>
                    <p className="mt-2 text-sm leading-6 text-muted-foreground">
                      {hasError ? t("authCallback.error.description") : t("authCallback.loading.description")}
                    </p>

                    {hasError ? (
                      <div role="alert" className="mt-4 rounded-2xl border border-destructive/20 bg-destructive/10 p-4">
                        <p className="text-sm leading-6 text-destructive">{t("authCallback.error.fallback")}</p>
                      </div>
                    ) : (
                      <p className="mt-4 text-sm leading-6 text-muted-foreground">{t("authCallback.loading.note")}</p>
                    )}

                    {hasError ? (
                      <div className="mt-5 flex flex-wrap gap-3">
                        <Button asChild>
                          <Link to="/login">
                            <ArrowLeft className="h-4 w-4" />
                            {t("authCallback.error.retry")}
                          </Link>
                        </Button>
                      </div>
                    ) : null}
                  </div>
                </div>
              </div>
            </section>

            <aside className="border-t border-border/70 bg-black/20 p-6 sm:p-8 lg:border-l lg:border-t-0">
              <div className="text-xs font-bold uppercase tracking-[0.18em] text-muted-foreground">{t("authCallback.side.kicker")}</div>
              <h2 className="mt-2 text-2xl font-bold tracking-tight text-foreground">{t("authCallback.side.title")}</h2>
              <p className="mt-3 text-sm leading-6 text-muted-foreground">{t("authCallback.side.description")}</p>

              <ol className="mt-6 space-y-4">
                {steps.map((step, index) => (
                  <li key={step} className="flex gap-3">
                    <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-border bg-background/60 text-xs font-bold text-foreground">
                      {index + 1}
                    </span>
                    <p className="pt-1 text-sm leading-6 text-muted-foreground">{step}</p>
                  </li>
                ))}
              </ol>
            </aside>
          </div>
        </Card>
      </div>
    </div>
  );
}

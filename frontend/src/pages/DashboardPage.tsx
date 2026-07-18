import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, CheckCircle2, Clock, Gauge, TriangleAlert, Wrench, XCircle } from "lucide-react";
import { getApiErrorMessage, listDashboardSummary, listToolCalls, listTools } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Skeleton } from "../components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { useI18n } from "../i18n";
import type { DashboardSummary, Tool, ToolCall } from "../types";

export function DashboardPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const [summary, setSummary] = useState<DashboardSummary | null>(null);
  const [tools, setTools] = useState<Tool[]>([]);
  const [calls, setCalls] = useState<ToolCall[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refreshNonce, setRefreshNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        setLoading(true);
        setError(null);
        const [summaryResult, toolsResult, callsResult] = await Promise.all([
          listDashboardSummary(token, workspaceOrgId),
          listTools(token, workspaceOrgId),
          listToolCalls(token, workspaceOrgId),
        ]);
        if (cancelled) {
          return;
        }
        setSummary(summaryResult);
        setTools(toolsResult.items);
        setCalls(callsResult.items);
      } catch (loadError) {
        if (!cancelled) {
          setSummary(null);
          setTools([]);
          setCalls([]);
          setError(getApiErrorMessage(loadError, t("dashboard.loadError"), t("common.permissionDenied")));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [token, workspaceOrgId, refreshNonce, t]);

  const lastFiveCalls = useMemo(() => calls.slice(0, 5), [calls]);
  const metrics = [
    {
      label: t("dashboard.metrics.toolCalls.label"),
      value: summary?.totalCalls ?? 0,
      hint: t("dashboard.metrics.toolCalls.hint"),
      icon: Activity,
    },
    {
      label: t("dashboard.metrics.success.label"),
      value: summary?.successCalls ?? 0,
      hint: t("dashboard.metrics.success.hint"),
      icon: CheckCircle2,
    },
    {
      label: t("dashboard.metrics.failed.label"),
      value: summary?.failedCalls ?? 0,
      hint: t("dashboard.metrics.failed.hint"),
      icon: XCircle,
    },
    {
      label: t("dashboard.metrics.pending.label"),
      value: summary?.pendingApprovalCalls ?? 0,
      hint: t("dashboard.metrics.pending.hint"),
      icon: Clock,
    },
    {
      label: t("dashboard.metrics.avgDuration.label"),
      value: summary ? `${summary.averageDurationMs.toFixed(1)}ms` : "0ms",
      hint: t("dashboard.metrics.avgDuration.hint"),
      icon: Gauge,
    },
  ];

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("dashboard.kicker")} title={t("dashboard.title")} description={t("dashboard.description")} />

      {error ? (
        <Card>
          <CardContent className="grid gap-4 p-6">
            <div role="alert" className="flex items-start gap-3 rounded-2xl border border-destructive/25 bg-destructive/10 p-4">
              <TriangleAlert className="mt-0.5 h-5 w-5 shrink-0 text-destructive" />
              <div>
                <div className="font-bold text-foreground">{t("dashboard.loadErrorTitle")}</div>
                <p className="m-0 mt-1 text-sm text-muted-foreground">{error}</p>
              </div>
            </div>
            <Button type="button" variant="outline" className="w-fit" onClick={() => setRefreshNonce((value) => value + 1)}>
              {t("dashboard.retry")}
            </Button>
          </CardContent>
        </Card>
      ) : loading ? (
        <div className="grid gap-4">
          <div className="grid gap-4 lg:grid-cols-4">
            {Array.from({ length: 5 }).map((_, index) => (
              <Card key={index} className="rounded-[20px] p-[18px]">
                <div className="flex items-start justify-between gap-4">
                  <div className="min-w-0 flex-1">
                    <Skeleton className="h-4 w-28" />
                    <Skeleton className="mt-2 h-9 w-20" />
                    <Skeleton className="mt-1 h-5 w-36" />
                  </div>
                  <Skeleton className="h-10 w-10 rounded-[14px]" />
                </div>
              </Card>
            ))}
          </div>
          <div className="grid gap-5 xl:grid-cols-2">
            <Card>
              <CardHeader>
                <Skeleton className="h-5 w-40" />
                <Skeleton className="h-4 w-56" />
              </CardHeader>
              <CardContent className="grid gap-3">
                {Array.from({ length: 4 }).map((_, index) => (
                  <Skeleton key={index} className="h-[66px] w-full rounded-2xl" />
                ))}
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <Skeleton className="h-5 w-40" />
                <Skeleton className="h-4 w-56" />
              </CardHeader>
              <CardContent className="grid gap-3">
                {Array.from({ length: 4 }).map((_, index) => (
                  <Skeleton key={index} className="h-[66px] w-full rounded-2xl" />
                ))}
              </CardContent>
            </Card>
          </div>
        </div>
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-4">
            {metrics.map((metric) => {
              const Icon = metric.icon;
              return (
                <Card key={metric.label} className="rounded-[20px] p-[18px]">
                  <div className="flex items-start justify-between gap-4">
                    <div>
                      <span className="text-xs uppercase tracking-wider text-muted-foreground">{metric.label}</span>
                      <strong className="mt-2 block text-3xl font-bold text-foreground">{metric.value}</strong>
                      <span className="mt-1 block text-sm text-muted-foreground">{metric.hint}</span>
                    </div>
                    <span className="grid h-10 w-10 place-items-center rounded-[14px] border border-primary/20 bg-primary/10 text-primary">
                      <Icon className="h-5 w-5" />
                    </span>
                  </div>
                </Card>
              );
            })}
          </div>

          <div className="grid gap-5 xl:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>{t("dashboard.recent.title")}</CardTitle>
                <CardDescription>{t("dashboard.recent.description")}</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3">
                {lastFiveCalls.map((call) => (
                  <Link
                    key={call.id}
                    to={`/audit?call=${call.id}`}
                    className="flex items-center justify-between gap-4 rounded-2xl border border-transparent bg-white/[0.03] px-4 py-3 transition-colors hover:border-primary/20"
                  >
                    <div className="min-w-0">
                      <div className="truncate font-bold text-foreground">{call.toolKey}</div>
                      <div className="mt-1 text-sm text-muted-foreground">{call.durationMs}ms</div>
                    </div>
                    <div className="grid justify-items-end gap-2 text-right">
                      <Badge variant={statusBadgeVariant(call.status)}>{call.status}</Badge>
                      <span className="text-xs text-muted-foreground">{new Date(call.createdAt).toLocaleString()}</span>
                    </div>
                  </Link>
                ))}
                {lastFiveCalls.length === 0 && (
                  <EmptyState
                    icon={Activity}
                    title={t("dashboard.recent.empty.title")}
                    description={t("dashboard.recent.empty.description")}
                  />
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle>{t("dashboard.topTools.title")}</CardTitle>
                <CardDescription>{t("dashboard.topTools.description")}</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3">
                {(summary?.topTools ?? []).map((item) => (
                  <div
                    key={item.toolKey}
                    className="flex items-center justify-between gap-4 rounded-2xl border border-transparent bg-white/[0.03] px-4 py-3"
                  >
                    <div className="flex min-w-0 items-center gap-3">
                      <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[14px] border border-accent/20 bg-accent/10 text-accent">
                        <Gauge className="h-4 w-4" />
                      </span>
                      <span className="truncate font-bold text-foreground">{item.toolKey}</span>
                    </div>
                    <Badge variant="pending">{t("dashboard.topTools.count", { count: item.count })}</Badge>
                  </div>
                ))}
                {(summary?.topTools ?? []).length === 0 && (
                  <EmptyState
                    icon={Wrench}
                    title={t("dashboard.topTools.empty.title")}
                    description={t("dashboard.topTools.empty.description")}
                  />
                )}
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>{t("dashboard.errors.title")}</CardTitle>
              <CardDescription>{t("dashboard.errors.description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-3">
              {(summary?.topErrors ?? []).map((item) => (
                <div
                  key={item.message}
                  className="flex items-center justify-between gap-4 rounded-2xl border border-transparent bg-white/[0.03] px-4 py-3"
                >
                  <div className="flex min-w-0 items-center gap-3">
                    <span className="grid h-9 w-9 shrink-0 place-items-center rounded-[14px] border border-destructive/20 bg-destructive/10 text-destructive">
                      <TriangleAlert className="h-4 w-4" />
                    </span>
                    <span className="truncate font-bold text-foreground">{item.message}</span>
                  </div>
                  <Badge variant="destructive">{t("dashboard.errors.count", { count: item.count })}</Badge>
                </div>
              ))}
              {(summary?.topErrors ?? []).length === 0 && (
                <EmptyState
                  icon={TriangleAlert}
                  title={t("dashboard.errors.empty.title")}
                  description={t("dashboard.errors.empty.description")}
                />
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div>
                  <CardTitle>{t("dashboard.tools.title")}</CardTitle>
                  <CardDescription>{t("dashboard.tools.description", { count: tools.length })}</CardDescription>
                </div>
                <Badge variant="secondary" className="gap-1">
                  <Wrench className="h-3.5 w-3.5" />
                  {t("dashboard.tools.registry")}
                </Badge>
              </div>
            </CardHeader>
            <CardContent>
              {tools.length === 0 ? (
                <p className="m-0 text-sm text-muted-foreground">{t("dashboard.tools.empty")}</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow className="bg-transparent hover:border-transparent">
                      <TableHead>{t("dashboard.tools.name")}</TableHead>
                      <TableHead>{t("dashboard.tools.type")}</TableHead>
                      <TableHead>{t("dashboard.tools.risk")}</TableHead>
                      <TableHead>{t("dashboard.tools.status")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {tools.map((tool) => (
                      <TableRow key={tool.id}>
                        <TableCell className="font-bold">
                          <Link to={`/tools/${tool.id}`} className="text-foreground transition-colors hover:text-primary">
                            {tool.displayName || `${tool.namespace}.${tool.name}`}
                          </Link>
                        </TableCell>
                        <TableCell>
                          <Badge variant={operationBadgeVariant(tool.operationType)}>{tool.operationType}</Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant={riskBadgeVariant(tool.riskLevel)}>{tool.riskLevel}</Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant={tool.enabled ? "success" : "destructive"}>
                            {tool.enabled ? t("dashboard.tools.enabled") : t("dashboard.tools.disabled")}
                          </Badge>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}

function statusBadgeVariant(status: string): "success" | "pending" | "destructive" | "secondary" {
  switch (status.toLowerCase()) {
    case "success":
    case "approved":
    case "allow":
      return "success";
    case "approval_required":
    case "pending":
      return "pending";
    case "failed":
    case "denied":
    case "rejected":
      return "destructive";
    default:
      return "secondary";
  }
}

function operationBadgeVariant(operationType: string): "pending" | "destructive" | "secondary" {
  switch (operationType.toLowerCase()) {
    case "write":
    case "create":
    case "update":
    case "delete":
    case "patch":
    case "post":
      return "pending";
    default:
      return "secondary";
  }
}

function riskBadgeVariant(riskLevel: string): "pending" | "destructive" | "secondary" {
  switch (riskLevel.toLowerCase()) {
    case "high":
    case "critical":
      return "destructive";
    case "medium":
      return "pending";
    default:
      return "secondary";
  }
}

import { Fragment, useEffect, useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { ArrowLeft, ArrowRight, Eye, FileText, RefreshCw, TriangleAlert } from "lucide-react";
import { getApiErrorMessage, getToolCall, listToolCalls, listTools } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { EmptyState } from "../components/EmptyState";
import { JsonBlock } from "../components/JsonBlock";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Skeleton } from "../components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { useI18n, type TranslationKey } from "../i18n";
import { toast } from "sonner";
import type { Tool, ToolCall, ToolCallExplanation, ToolCallPage } from "../types";

const statusTabs = [
  { value: "all", labelKey: "audit.tabs.all" },
  { value: "success", labelKey: "audit.tabs.success" },
  { value: "failed", labelKey: "audit.tabs.failed" },
  { value: "approval_required", labelKey: "audit.tabs.approval" },
  { value: "denied", labelKey: "audit.tabs.denied" },
  { value: "rate_limited", labelKey: "audit.tabs.rateLimited" },
] satisfies Array<{ value: string; labelKey: TranslationKey }>;

const defaultPageSize = 10;

export function AuditLogsPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const [tools, setTools] = useState<Tool[]>([]);
  const [callsPage, setCallsPage] = useState<ToolCallPage | null>(null);
  const [focusedCall, setFocusedCall] = useState<ToolCall | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshNonce, setRefreshNonce] = useState(0);

  const toolFilter = searchParams.get("tool") ?? "all";
  const statusFilter = searchParams.get("status") ?? "all";
  const fromFilter = searchParams.get("from") ?? "";
  const toFilter = searchParams.get("to") ?? "";
  const focusedCallId = searchParams.get("call")?.trim() || null;
  const page = clampPositiveInt(searchParams.get("page"), 1);
  const pageSize = clampPositiveInt(searchParams.get("pageSize"), defaultPageSize, 200);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        setLoading(true);
        const [toolsResult, callsResult] = await Promise.all([
          listTools(token, workspaceOrgId),
          listToolCalls(token, workspaceOrgId, {
            tool: toolFilter === "all" ? undefined : toolFilter,
            status: statusFilter === "all" ? undefined : [statusFilter],
            from: fromFilter || undefined,
            to: toFilter || undefined,
            page,
            pageSize,
          }),
        ]);
        if (!cancelled) {
          setTools(toolsResult.items);
          setCallsPage(callsResult);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(getApiErrorMessage(error, t("audit.loadError"), t("common.permissionDenied")));
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
  }, [token, workspaceOrgId, toolFilter, statusFilter, fromFilter, toFilter, page, pageSize, refreshNonce, t]);

  useEffect(() => {
    let cancelled = false;
    setFocusedCall(null);
    const targetCallId = focusedCallId;
    if (!targetCallId) {
      return () => {
        cancelled = true;
      };
    }
    async function loadFocusedCall(callId: string) {
      try {
        const detail = await getToolCall(callId, token, workspaceOrgId);
        if (!cancelled) {
          setFocusedCall(detail);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(getApiErrorMessage(error, t("audit.detailError"), t("common.permissionDenied")));
        }
      }
    }
    void loadFocusedCall(targetCallId);
    return () => {
      cancelled = true;
    };
  }, [focusedCallId, token, workspaceOrgId, refreshNonce, t]);

  const totalPages = useMemo(() => {
    if (!callsPage || callsPage.total === 0) {
      return 1;
    }
    return Math.max(1, Math.ceil(callsPage.total / callsPage.pageSize));
  }, [callsPage]);

  const toolKeys = useMemo(() => tools.map((tool) => tool.namespace + "." + tool.name), [tools]);
  const visibleCalls = useMemo(() => {
    const pageItems = callsPage?.items ?? [];
    if (!focusedCall) {
      return pageItems;
    }
    const focusedIndex = pageItems.findIndex((item) => item.id === focusedCall.id);
    if (focusedIndex < 0) {
      return [focusedCall, ...pageItems];
    }
    return pageItems.map((item, index) => (index === focusedIndex ? focusedCall : item));
  }, [callsPage, focusedCall]);
  const expandedCallId = focusedCall?.id ?? null;

  function updateSearchParams(updater: (next: URLSearchParams) => void) {
    const next = new URLSearchParams(searchParams);
    updater(next);
    next.delete("call");
    setSearchParams(next, { replace: false });
  }

  function setToolFilter(value: string) {
    updateSearchParams((next) => {
      if (value === "all") {
        next.delete("tool");
      } else {
        next.set("tool", value);
      }
      next.set("page", "1");
    });
  }

  function setStatusFilter(value: string) {
    updateSearchParams((next) => {
      if (value === "all") {
        next.delete("status");
      } else {
        next.set("status", value);
      }
      next.set("page", "1");
    });
  }

  function setDateFilter(key: "from" | "to", value: string) {
    updateSearchParams((next) => {
      if (value) {
        next.set(key, value);
      } else {
        next.delete(key);
      }
      next.set("page", "1");
    });
  }

  function setPage(nextPage: number) {
    updateSearchParams((next) => {
      next.set("page", String(nextPage));
    });
  }

  function setPageSize(nextPageSize: number) {
    updateSearchParams((next) => {
      next.set("pageSize", String(nextPageSize));
      next.set("page", "1");
    });
  }

  function expandCall(call: ToolCall) {
    const next = new URLSearchParams(searchParams);
    if (focusedCallId === call.id) {
      next.delete("call");
    } else {
      next.set("call", call.id);
    }
    setSearchParams(next, { replace: true });
  }

  const traceBaseURL = import.meta.env.VITE_JAEGER_URL?.trim() ?? "";

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("audit.kicker")} title={t("audit.title")} description={t("audit.description")} icon={FileText} />

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <CardTitle>{t("audit.filters.title")}</CardTitle>
              <CardDescription>
                {callsPage ? t("audit.filters.description", { count: callsPage.total }) : t("audit.filters.placeholder")}
              </CardDescription>
            </div>
            <Button type="button" variant="outline" onClick={() => setSearchParams(new URLSearchParams(), { replace: false })}>
              {t("audit.filters.clear")}
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_220px_180px_180px_180px]">
            <div className="grid gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("audit.filters.tool")}</span>
              <Select value={toolFilter} onValueChange={setToolFilter}>
                <SelectTrigger>
                  <SelectValue placeholder={t("audit.filters.allTools")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("audit.filters.allTools")}</SelectItem>
                  {toolKeys.map((key) => (
                    <SelectItem key={key} value={key}>
                      {key}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("audit.filters.pageSize")}</span>
              <Select value={String(pageSize)} onValueChange={(value) => setPageSize(Number(value))}>
                <SelectTrigger>
                  <SelectValue placeholder={t("audit.filters.pageSize")} />
                </SelectTrigger>
                <SelectContent>
                  {[10, 25, 50, 100, 200].map((value) => (
                    <SelectItem key={value} value={String(value)}>
                      {value}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("audit.filters.from")}</span>
              <Input type="date" value={fromFilter} onChange={(event) => setDateFilter("from", event.target.value)} />
            </div>

            <div className="grid gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("audit.filters.to")}</span>
              <Input type="date" value={toFilter} onChange={(event) => setDateFilter("to", event.target.value)} />
            </div>

            <div className="grid gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("audit.filters.refresh")}</span>
              <Button type="button" variant="outline" onClick={() => setRefreshNonce((value) => value + 1)}>
                <RefreshCw className="h-4 w-4" />
                {t("audit.filters.reload")}
              </Button>
            </div>
          </div>

          <Tabs value={statusFilter} onValueChange={setStatusFilter}>
            <TabsList className="flex w-fit flex-wrap">
              {statusTabs.map((tab) => (
                <TabsTrigger key={tab.value} value={tab.value} className="gap-2">
                  {t(tab.labelKey)}
                  {callsPage ? <Badge variant={tab.value === "all" ? "secondary" : statusBadgeVariant(tab.value)}>{statusCount(callsPage.items, tab.value)}</Badge> : null}
                </TabsTrigger>
              ))}
            </TabsList>

            <TabsContent value={statusFilter} className="mt-4">
              {loading ? (
                <div className="grid gap-3">
                  <Skeleton className="h-10 w-52" />
                  <Skeleton className="h-72 w-full" />
                </div>
              ) : !callsPage || visibleCalls.length === 0 ? (
                <EmptyState
                  icon={TriangleAlert}
                  title={t("audit.empty.title")}
                  description={t("audit.empty.description")}
                  action={
                    <Button type="button" variant="outline" onClick={() => setSearchParams(new URLSearchParams(), { replace: false })}>
                      {t("audit.empty.clear")}
                    </Button>
                  }
                />
              ) : (
                <>
                  <Table>
                    <TableHeader>
                      <TableRow className="bg-transparent hover:border-transparent">
                        <TableHead>{t("audit.table.tool")}</TableHead>
                        <TableHead>{t("audit.table.status")}</TableHead>
                        <TableHead>{t("audit.table.policy")}</TableHead>
                        <TableHead>{t("audit.table.approval")}</TableHead>
                        <TableHead>{t("audit.table.trace")}</TableHead>
                        <TableHead>{t("audit.table.created")}</TableHead>
                        <TableHead className="text-right">{t("audit.table.action")}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {visibleCalls.map((call) => (
                        <Fragment key={call.id}>
                          <TableRow
                            className={expandedCallId === call.id ? "border-accent/30 bg-white/[0.05]" : undefined}
                          >
                            <TableCell>
                              <div className="font-bold text-foreground">{call.toolKey}</div>
                              <div className="mt-1 text-sm text-muted-foreground">{call.durationMs}ms</div>
                            </TableCell>
                            <TableCell>
                              <Badge variant={statusBadgeVariant(call.status)}>{call.status}</Badge>
                            </TableCell>
                            <TableCell>
                              <Badge variant={policyBadgeVariant(call.policyDecision)}>{call.policyDecision}</Badge>
                            </TableCell>
                            <TableCell>
                              <Badge variant={approvalBadgeVariant(call.approvalStatus || (call.approvalId ? "pending" : "none"))}>
                                {call.approvalStatus || (call.approvalId ? "pending" : "none")}
                              </Badge>
                            </TableCell>
                            <TableCell className="max-w-[16rem] truncate text-muted-foreground">
                              {buildTraceLink(traceBaseURL, call.traceId) ? (
                                <a
                                  href={buildTraceLink(traceBaseURL, call.traceId) ?? undefined}
                                  target="_blank"
                                  rel="noreferrer"
                                  className="text-primary transition-colors hover:text-accent"
                                >
                                  {call.traceId}
                                </a>
                              ) : (
                                call.traceId
                              )}
                            </TableCell>
                            <TableCell className="text-muted-foreground">{new Date(call.createdAt).toLocaleString()}</TableCell>
                            <TableCell>
                              <div className="flex justify-end">
                                <Button type="button" size="sm" variant="outline" onClick={() => expandCall(call)}>
                                  <Eye className="h-4 w-4" />
                                  {expandedCallId === call.id ? t("audit.action.hide") : t("audit.action.view")}
                                </Button>
                              </div>
                            </TableCell>
                          </TableRow>

                          {expandedCallId === call.id ? (
                            <TableRow className="bg-transparent hover:border-transparent">
                              <TableCell colSpan={7} className="px-0 pb-4 pt-0">
                                <div className="grid gap-4 rounded-[20px] border border-border bg-white/[0.03] p-4">
                                  <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
                                    <Meta label={t("audit.meta.approvalId")} value={call.approvalId || "-"} />
                                    <Meta
                                      label={t("audit.meta.trace")}
                                      value={
                                        buildTraceLink(traceBaseURL, call.traceId) ? (
                                          <a
                                            href={buildTraceLink(traceBaseURL, call.traceId) ?? undefined}
                                            target="_blank"
                                            rel="noreferrer"
                                            className="break-all text-primary transition-colors hover:text-accent"
                                          >
                                            {call.traceId}
                                          </a>
                                        ) : (
                                          call.traceId
                                        )
                                      }
                                    />
                                    <Meta label={t("audit.meta.actor")} value={call.actorName || call.actorEmail || call.actorSubject || "-"} />
                                    <Meta label={t("audit.meta.duration")} value={`${call.durationMs}ms`} />
                                  </div>

                                  {call.errorMessage ? (
                                    <p className="m-0 rounded-2xl border border-destructive/25 bg-destructive/10 px-4 py-3 text-sm text-destructive">
                                      {call.errorMessage}
                                    </p>
                                  ) : null}

                                  <section className="grid gap-3 rounded-[20px] border border-border bg-background/30 p-4">
                                    <div className="grid gap-1">
                                      <h4 className="text-sm font-bold text-foreground">{t("audit.risk.title")}</h4>
                                      <p className="m-0 text-sm text-muted-foreground">{t("audit.risk.description")}</p>
                                    </div>
                                    {call.explanation ? (
                                      <div className="grid gap-4">
                                        <div className="grid gap-3 md:grid-cols-3 xl:grid-cols-5">
                                          <Meta
                                            label={t("audit.risk.level")}
                                            value={
                                              <Badge variant={riskLevelBadgeVariant(call.explanation.riskLevel)}>
                                                {displayExplanationRiskLevel(call.explanation, t("audit.risk.unknown"))}
                                              </Badge>
                                            }
                                          />
                                          <Meta
                                            label={t("audit.risk.target")}
                                            value={call.explanation.targetCategory?.trim() || t("audit.risk.noTarget")}
                                          />
                                          <Meta
                                            label={t("audit.risk.rule")}
                                            value={call.explanation.matchedRule?.trim() || t("audit.risk.noRule")}
                                          />
                                          <Meta
                                            label={t("audit.risk.policyRuleId")}
                                            value={explanationSignalValue(call.explanation, "matchedPolicyRuleId") || "-"}
                                          />
                                          <Meta
                                            label={t("audit.risk.policyExplanation")}
                                            value={explanationSignalValue(call.explanation, "policyExplanation") || "-"}
                                          />
                                        </div>
                                        <div className="grid gap-2">
                                          <span className="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">{t("audit.risk.signals")}</span>
                                          {call.explanation.signals && call.explanation.signals.length > 0 ? (
                                            <div className="flex flex-wrap gap-2">
                                              {call.explanation.signals.map((signal) => (
                                                <Badge key={signal} variant="secondary" className="max-w-full whitespace-normal text-left">
                                                  {signal}
                                                </Badge>
                                              ))}
                                            </div>
                                          ) : (
                                            <p className="m-0 text-sm text-muted-foreground">{t("audit.risk.noSignals")}</p>
                                          )}
                                        </div>
                                      </div>
                                    ) : (
                                      <p className="m-0 text-sm text-muted-foreground">{t("audit.risk.noExplanation")}</p>
                                    )}
                                  </section>

                                  <div className="grid gap-4 lg:grid-cols-2">
                                    <section className="grid gap-2">
                                      <h4 className="text-sm font-bold text-foreground">{t("audit.input")}</h4>
                                      <JsonBlock value={call.inputRedactedJson} />
                                    </section>
                                    <section className="grid gap-2">
                                      <h4 className="text-sm font-bold text-foreground">{t("audit.output")}</h4>
                                      <JsonBlock value={call.outputRedactedJson} />
                                    </section>
                                  </div>
                                </div>
                              </TableCell>
                            </TableRow>
                          ) : null}
                        </Fragment>
                      ))}
                    </TableBody>
                  </Table>

                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <p className="m-0 text-sm text-muted-foreground">
                      {t("audit.pagination", { page: callsPage.page, totalPages, total: callsPage.total })}
                    </p>
                    <div className="flex items-center gap-2">
                      <Button type="button" variant="outline" size="sm" disabled={callsPage.page <= 1} onClick={() => setPage(callsPage.page - 1)}>
                        <ArrowLeft className="h-4 w-4" />
                        {t("audit.pagination.previous")}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        disabled={callsPage.page >= totalPages}
                        onClick={() => setPage(callsPage.page + 1)}
                      >
                        {t("audit.pagination.next")}
                        <ArrowRight className="h-4 w-4" />
                      </Button>
                    </div>
                  </div>
                </>
              )}
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>
    </div>
  );
}

function Meta({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 rounded-2xl border border-border bg-background/40 px-4 py-3">
      <span className="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">{label}</span>
      <div className="text-sm font-bold text-foreground">{value}</div>
    </div>
  );
}

function buildTraceLink(baseURL: string, traceId: string): string | null {
  const trimmed = baseURL.trim();
  if (!trimmed) {
    return null;
  }
  return `${trimmed.replace(/\/$/, "")}/trace/${encodeURIComponent(traceId)}`;
}

function clampPositiveInt(raw: string | null, fallback: number, maxValue = Number.POSITIVE_INFINITY): number {
  const parsed = Number(raw ?? "");
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return fallback;
  }
  return Math.min(Math.floor(parsed), maxValue);
}

function statusCount(items: ToolCall[], status: string): number {
  if (status === "all") {
    return items.length;
  }
  return items.filter((item) => item.status === status).length;
}

function statusBadgeVariant(status: string): "success" | "pending" | "destructive" | "secondary" {
  switch (status.toLowerCase()) {
    case "success":
    case "approved":
    case "allow":
      return "success";
    case "pending":
    case "approval_required":
      return "pending";
    case "failed":
    case "denied":
    case "rejected":
    case "rate_limited":
      return "destructive";
    default:
      return "secondary";
  }
}

function policyBadgeVariant(policyDecision: string): "success" | "pending" | "destructive" | "secondary" {
  switch (policyDecision.toLowerCase()) {
    case "allow":
      return "success";
    case "require_approval":
    case "approval_required":
      return "pending";
    case "deny":
    case "denied":
      return "destructive";
    default:
      return "secondary";
  }
}

function approvalBadgeVariant(approvalStatus: string): "success" | "pending" | "destructive" | "secondary" {
  switch (approvalStatus.toLowerCase()) {
    case "approved":
    case "allow":
    case "success":
      return "success";
    case "pending":
    case "approval_required":
      return "pending";
    case "rejected":
    case "denied":
    case "failed":
      return "destructive";
    default:
      return "secondary";
  }
}

function riskLevelBadgeVariant(riskLevel: string | undefined): "success" | "pending" | "destructive" | "secondary" {
  switch ((riskLevel ?? "").trim().toLowerCase()) {
    case "critical":
    case "high":
      return "destructive";
    case "medium":
      return "pending";
    case "low":
      return "success";
    default:
      return "secondary";
  }
}

function displayExplanationRiskLevel(explanation: ToolCallExplanation, fallback: string): string {
  const value = explanation.riskLevel?.trim().toLowerCase();
  if (!value) {
    return fallback;
  }
  return value;
}

function explanationSignalValue(explanation: ToolCallExplanation, key: string): string {
  const prefix = `${key}:`;
  const match = explanation.signals?.find((signal) => signal.startsWith(prefix));
  return match ? match.slice(prefix.length).trim() : "";
}

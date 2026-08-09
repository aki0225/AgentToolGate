import { type FormEvent, type ReactNode, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, CircleAlert, Clock, ExternalLink, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import {
  ApiError,
  approveApproval,
  connectApprovalStream,
  getApiErrorMessage,
  listApprovals,
  listToolCalls,
  rejectApproval,
} from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canReviewApprovals } from "../auth/permissions";
import { JsonBlock } from "../components/JsonBlock";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { useI18n, type TranslationKey } from "../i18n";
import { governanceActionLabel, governanceRiskLabel, governanceStatusLabel } from "../lib/governanceLabels";
import type { ApprovalRequest } from "../types";

type Feedback = {
  kind: "success" | "error" | "warning";
  text: string;
  callId?: string;
} | null;

const approvalTabs = [
  { value: "pending", labelKey: "approvals.tab.pending" },
  { value: "approved", labelKey: "approvals.tab.approved" },
  { value: "rejected", labelKey: "approvals.tab.rejected" },
  { value: "expired", labelKey: "approvals.tab.expired" },
] satisfies Array<{ value: ApprovalRequest["status"]; labelKey: TranslationKey }>;

const textareaClassName =
  "min-h-28 w-full rounded-[14px] border border-input bg-background/55 px-3 py-2 text-sm text-foreground ring-offset-background transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

type ReviewDialogState = {
  approval: ApprovalRequest;
  decision: "approve" | "reject";
} | null;

export function ApprovalsPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  // AuthContext 会先加载 workspace，再异步加载 me；me 未就绪时不能误判为无审批权限。
  const authReady = auth.me !== null || auth.error !== null;
  const canReview = canReviewApprovals(auth.me?.user.role);
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([]);
  const [loading, setLoading] = useState(true);
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [savingId, setSavingId] = useState<string | null>(null);
  const [refreshTick, setRefreshTick] = useState(0);
  const [reviewDialog, setReviewDialog] = useState<ReviewDialogState>(null);
  const [reviewReason, setReviewReason] = useState("");
  const [localReviewerToken, setLocalReviewerToken] = useState("");
  const [approvalCallIds, setApprovalCallIds] = useState<Record<string, string>>({});

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!authReady) {
        setLoading(true);
        return;
      }
      if (!canReview) {
        setApprovals([]);
        setApprovalCallIds({});
        setFeedback({ kind: "error", text: t("common.permissionDenied") });
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const [result, callsPage] = await Promise.all([
          listApprovals(token, workspaceOrgId),
          listToolCalls(token, workspaceOrgId, { page: 1, pageSize: 200 }).catch(() => null),
        ]);
        if (!cancelled) {
          setApprovals(result.items);
          setApprovalCallIds(
            Object.fromEntries(
              (callsPage?.items ?? [])
                .filter((call) => call.approvalId)
                .map((call) => [call.approvalId as string, call.id]),
            ),
          );
        }
      } catch (error) {
        if (!cancelled) {
          setFeedback({
            kind: "error",
            text: getApiErrorMessage(error, t("approvals.loadError"), t("common.permissionDenied")),
          });
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
  }, [authReady, canReview, token, workspaceOrgId, refreshTick, t]);

  useEffect(() => {
    if (!workspaceOrgId || !canReview) {
      return;
    }

    let cancelled = false;
    let reconnectTimer: number | null = null;
    let reconnectDelayMs = 1000;
    let closeConnection = () => {};

    const clearReconnectTimer = () => {
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
    };

    const scheduleReconnect = () => {
      if (cancelled || reconnectTimer !== null) {
        return;
      }
      reconnectTimer = window.setTimeout(() => {
        reconnectTimer = null;
        if (cancelled) {
          return;
        }
        connect();
      }, reconnectDelayMs);
      reconnectDelayMs = Math.min(reconnectDelayMs * 2, 30000);
    };

    const connect = () => {
      if (cancelled) {
        return;
      }
      closeConnection();
      closeConnection = connectApprovalStream({
        token,
        workspaceOrgId,
        onApproval: () => setRefreshTick((tick) => tick + 1),
        onOpen: () => {
          reconnectDelayMs = 1000;
          clearReconnectTimer();
        },
        onError: () => {
          scheduleReconnect();
        },
      }).close;
    };

    connect();

    return () => {
      cancelled = true;
      clearReconnectTimer();
      closeConnection();
    };
  }, [canReview, token, workspaceOrgId]);

  function openReviewDialog(approval: ApprovalRequest, decision: "approve" | "reject") {
    setReviewDialog({ approval, decision });
    setReviewReason("");
    setLocalReviewerToken("");
  }

  async function handleReviewSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!reviewDialog) {
      return;
    }
    await handleReview(reviewDialog.approval.id, reviewDialog.decision, reviewReason);
  }

  async function handleReview(approvalId: string, decision: "approve" | "reject", reason: string) {
    if (!canReview) {
      setFeedback({ kind: "error", text: t("common.permissionDenied") });
      return;
    }
    setSavingId(approvalId);
    setFeedback(null);
    try {
      const reviewerToken =
        auth.authMode === "local" && localReviewerToken.trim() ? localReviewerToken.trim() : token;
      const response =
        decision === "approve"
          ? await approveApproval(approvalId, { reason }, reviewerToken, workspaceOrgId)
          : await rejectApproval(approvalId, { reason }, reviewerToken, workspaceOrgId);

      setApprovals((current) =>
        current.map((approval) => (approval.id === response.approval.id ? response.approval : approval))
      );
      setApprovalCallIds((current) => ({ ...current, [response.approval.id]: response.toolCall.id }));
      if (decision === "reject") {
        setFeedback({
          kind: "success",
          text: t("approvals.rejectedFeedback", { tool: response.approval.toolDisplayName }),
          callId: response.toolCall.id,
        });
      } else {
        const executionStatus = response.toolCall.status.trim().toLowerCase();
        if (executionStatus === "success") {
          setFeedback({
            kind: "success",
            text: t("approvals.approvedFeedback", { tool: response.approval.toolDisplayName }),
            callId: response.toolCall.id,
          });
        } else if (executionStatus === "failed") {
          setFeedback({
            kind: "error",
            text: t("approvals.approvedExecutionFailed", {
              tool: response.approval.toolDisplayName,
              error: "",
            }).replace(/[:：]\s*$/, ""),
            callId: response.toolCall.id,
          });
        } else if (executionStatus === "approval_required") {
          setFeedback({
            kind: "warning",
            text: t("approvals.approvedPendingRetry", { tool: response.approval.toolDisplayName }),
            callId: response.toolCall.id,
          });
        } else {
          setFeedback({
            kind: "warning",
            text: t("approvals.approvedUnknownStatus", {
              tool: response.approval.toolDisplayName,
              status: governanceStatusLabel(t, executionStatus),
            }),
            callId: response.toolCall.id,
          });
        }
      }
      setReviewDialog(null);
      setReviewReason("");
      setLocalReviewerToken("");
    } catch (error) {
      setFeedback({
        kind: "error",
        text: error instanceof ApiError && error.status === 403
          ? t("common.permissionDenied")
          : t("approvals.actionError"),
      });
    } finally {
      setSavingId(null);
    }
  }

  function renderApprovalTable(items: ApprovalRequest[], emptyText: string) {
    if (items.length === 0) {
      return <p className="m-0 text-sm text-muted-foreground">{emptyText}</p>;
    }

    return (
      <Table>
        <TableHeader>
          <TableRow className="bg-transparent hover:border-transparent">
            <TableHead>{t("approvals.table.tool")}</TableHead>
            <TableHead>{t("approvals.table.status")}</TableHead>
            <TableHead>{t("approvals.table.requester")}</TableHead>
            <TableHead>{t("approvals.table.created")}</TableHead>
            <TableHead>{t("approvals.table.reason")}</TableHead>
            <TableHead className="text-right">{t("approvals.table.action")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((approval) => (
            <TableRow key={approval.id}>
              <TableCell>
                <div className="font-bold text-foreground">{approval.toolDisplayName}</div>
                <div className="mt-1 text-sm text-muted-foreground">{approval.toolKey}</div>
              </TableCell>
              <TableCell>
                <Badge variant={statusBadgeVariant(approval.status)} className="gap-1">
                  {approval.status === "pending" && <Clock className="h-3.5 w-3.5" />}
                  {governanceStatusLabel(t, approval.status)}
                </Badge>
              </TableCell>
              <TableCell>
                <div className="font-bold text-foreground">{approval.requestedBy}</div>
                <div className="mt-1 text-sm text-muted-foreground">
                  {t("approvals.reviewedBy", { name: approval.reviewedBy || "-" })}
                </div>
              </TableCell>
              <TableCell className="text-muted-foreground">
                <div>{new Date(approval.createdAt).toLocaleString()}</div>
                {approval.status !== "pending" && (
                  <div className="mt-1 text-xs">{t("approvals.updatedAt", { time: new Date(approval.updatedAt).toLocaleString() })}</div>
                )}
              </TableCell>
              <TableCell className="max-w-[22rem] text-muted-foreground">
                <span className="line-clamp-2">{approval.reason || t("approvals.noReason")}</span>
              </TableCell>
              <TableCell>
                <div className="flex justify-end gap-2">
                  {approvalCallIds[approval.id] ? (
                    <Button asChild type="button" size="sm" variant="outline">
                      <Link to={`/audit?call=${encodeURIComponent(approvalCallIds[approval.id])}`}>
                        <ExternalLink className="h-4 w-4" />
                        {t("approvals.reviewDialog.openAudit")}
                      </Link>
                    </Button>
                  ) : null}
                  {approval.status === "pending" && canReview ? (
                    <>
                    <Button
                      type="button"
                      size="sm"
                      disabled={savingId === approval.id}
                      onClick={() => openReviewDialog(approval, "approve")}
                    >
                      {t("approvals.approve")}
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      variant="destructive"
                      disabled={savingId === approval.id}
                      onClick={() => openReviewDialog(approval, "reject")}
                    >
                      {t("approvals.reject")}
                    </Button>
                    </>
                  ) : (
                    <span className="self-center text-sm text-muted-foreground">{t("approvals.reviewComplete")}</span>
                  )}
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    );
  }

  const selectedApproval = reviewDialog?.approval ?? null;
  const selectedCallId = selectedApproval ? approvalCallIds[selectedApproval.id] : undefined;
  const selectedDecisionSummary = selectedApproval ? buildDecisionSummary(selectedApproval) : null;
  const selectedRiskLevel = selectedApproval ? approvalRiskLevel(selectedApproval) : "";

  return (
    <div className="grid gap-6">
      <PageHeader
        kicker={t("approvals.kicker")}
        title={t("approvals.title")}
        icon={ShieldCheck}
        description={
          <>
            {t("approvals.descriptionPrefix")}{" "}
            <code className="rounded-[14px] border border-border bg-white/[0.04] px-2 py-0.5 text-foreground">requiresApproval=true</code>,
            {" "}{t("approvals.descriptionSuffix")}
          </>
        }
      />

      <Dialog open={feedback !== null} onOpenChange={(open) => !open && setFeedback(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              {feedback?.kind === "success" ? (
                <CheckCircle2 className="h-5 w-5 text-primary" />
              ) : feedback?.kind === "warning" ? (
                <CircleAlert className="h-5 w-5 text-accent" />
              ) : (
                <XCircle className="h-5 w-5 text-destructive" />
              )}
              {feedback?.kind === "success"
                ? t("approvals.dialog.success")
                : feedback?.kind === "warning"
                  ? t("approvals.dialog.warning")
                  : t("approvals.dialog.error")}
            </DialogTitle>
            <DialogDescription>{feedback?.text}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            {feedback?.callId ? (
              <Button asChild type="button">
                <Link to={`/audit?call=${encodeURIComponent(feedback.callId)}`} onClick={() => setFeedback(null)}>
                  <ExternalLink className="h-4 w-4" />
                  {t("approvals.dialog.openAudit")}
                </Link>
              </Button>
            ) : null}
            <Button type="button" variant="outline" onClick={() => setFeedback(null)}>
              {t("approvals.dialog.close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={reviewDialog !== null} onOpenChange={(open) => {
        if (!open) {
          setReviewDialog(null);
          setReviewReason("");
          setLocalReviewerToken("");
        }
      }}>
        <DialogContent className="max-h-[90vh] max-w-3xl overflow-y-auto">
          <form className="grid gap-4" onSubmit={(event) => void handleReviewSubmit(event)}>
            <DialogHeader>
              <DialogTitle>
                {reviewDialog?.decision === "approve" ? t("approvals.reviewDialog.approveTitle") : t("approvals.reviewDialog.rejectTitle")}
              </DialogTitle>
              <DialogDescription>
                {t("approvals.reviewDialog.description", {
                  tool: reviewDialog?.approval.toolDisplayName ?? "-",
                  requester: reviewDialog?.approval.requestedBy ?? "-",
                })}
              </DialogDescription>
            </DialogHeader>
            {selectedApproval ? (
              <section className="grid gap-3 border-y border-border py-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <h3 className="m-0 text-sm font-bold text-foreground">{t("approvals.reviewDialog.detailsTitle")}</h3>
                  {selectedCallId ? (
                    <Button asChild type="button" size="sm" variant="outline">
                      <Link to={`/audit?call=${encodeURIComponent(selectedCallId)}`}>
                        <ExternalLink className="h-4 w-4" />
                        {t("approvals.reviewDialog.openAudit")}
                      </Link>
                    </Button>
                  ) : (
                    <span className="text-xs text-muted-foreground">{t("approvals.reviewDialog.auditPending")}</span>
                  )}
                </div>

                <div className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.risk")}
                    value={
                      selectedRiskLevel ? (
                        <MachineValue
                          label={governanceRiskLabel(t, selectedRiskLevel)}
                          raw={selectedRiskLevel}
                        />
                      ) : (
                        t("approvals.reviewDialog.noValue")
                      )
                    }
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.actionType")}
                    value={
                      selectedApproval.actionType ? (
                        <MachineValue
                          label={governanceActionLabel(t, selectedApproval.actionType)}
                          raw={selectedApproval.actionType}
                        />
                      ) : (
                        t("approvals.reviewDialog.noValue")
                      )
                    }
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.adapter")}
                    value={selectedApproval.adapter || t("approvals.reviewDialog.noValue")}
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.expires")}
                    value={formatDateTime(selectedApproval.expiresAt, t("approvals.reviewDialog.noValue"))}
                  />
                </div>

                <ApprovalMeta
                  label={t("approvals.reviewDialog.target")}
                  value={redactApprovalTarget(selectedApproval.target) || t("approvals.reviewDialog.noValue")}
                  code
                />
                <ApprovalMeta
                  label={t("approvals.reviewDialog.canonicalTarget")}
                  value={redactApprovalTarget(selectedApproval.canonicalTarget) || t("approvals.reviewDialog.noValue")}
                  code
                />

                <div className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.fingerprint")}
                    value={selectedApproval.fingerprint || t("approvals.reviewDialog.noValue")}
                    code
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.approvalId")}
                    value={selectedApproval.id}
                    code
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.contentHash")}
                    value={selectedApproval.contentHash || t("approvals.reviewDialog.noValue")}
                    code
                  />
                  <ApprovalMeta
                    label={t("approvals.reviewDialog.scriptHash")}
                    value={selectedApproval.scriptHash || t("approvals.reviewDialog.noValue")}
                    code
                  />
                </div>

                {selectedDecisionSummary ? (
                  <div className="grid gap-2">
                    <span className="text-xs font-bold uppercase tracking-[0.18em] text-muted-foreground">
                      {t("approvals.reviewDialog.decisionSummary")}
                    </span>
                    <JsonBlock value={selectedDecisionSummary} className="max-h-52" />
                  </div>
                ) : null}
              </section>
            ) : null}
            <label className="grid gap-2 text-sm font-medium text-foreground">
              {t("approvals.reviewDialog.reasonLabel")}
              <textarea
                className={textareaClassName}
                maxLength={500}
                value={reviewReason}
                placeholder={t("approvals.reviewDialog.reasonPlaceholder")}
                onChange={(event) => setReviewReason(event.target.value)}
              />
            </label>
            {auth.authMode === "local" ? (
              <label className="grid gap-2 text-sm font-medium text-foreground">
                {t("approvals.reviewDialog.localReviewerTokenLabel")}
                <Input
                  type="password"
                  name="agenttoolgate-reviewer-token"
                  autoComplete="off"
                  data-1p-ignore
                  data-lpignore="true"
                  value={localReviewerToken}
                  placeholder={t("approvals.reviewDialog.localReviewerTokenPlaceholder")}
                  onChange={(event) => setLocalReviewerToken(event.target.value)}
                />
                <span className="text-xs font-normal text-muted-foreground">
                  {t("approvals.reviewDialog.localReviewerTokenHint")}
                </span>
              </label>
            ) : null}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setReviewDialog(null);
                  setReviewReason("");
                  setLocalReviewerToken("");
                }}
              >
                {t("approvals.reviewDialog.cancel")}
              </Button>
              <Button type="submit" variant={reviewDialog?.decision === "reject" ? "destructive" : "default"} disabled={savingId === reviewDialog?.approval.id}>
                {reviewDialog?.decision === "approve"
                  ? t(
                      approvalRequiresClientRetry(reviewDialog.approval)
                        ? "approvals.reviewDialog.confirmApproveRetry"
                        : "approvals.reviewDialog.confirmApprove",
                    )
                  : t("approvals.reviewDialog.confirmReject")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <CardTitle>{t("approvals.queue.title")}</CardTitle>
              <CardDescription>{t("approvals.queue.count", { count: approvals.length })}</CardDescription>
            </div>
            <Button type="button" variant="outline" onClick={() => setRefreshTick((tick) => tick + 1)}>
              <RefreshCw className="h-4 w-4" />
              {t("approvals.refresh")}
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {loading ? (
            <p className="m-0 text-sm text-muted-foreground">{t("approvals.loading")}</p>
          ) : approvals.length === 0 ? (
            <div className="grid gap-3 rounded-[20px] border border-border bg-white/[0.03] p-5 text-sm text-muted-foreground">
              <p className="m-0">{t("approvals.empty.title")}</p>
              <Button asChild variant="outline" className="w-fit">
                <Link to="/tools">{t("approvals.empty.openTools")}</Link>
              </Button>
            </div>
          ) : (
            <Tabs defaultValue="pending">
              <TabsList className="mb-4 flex w-fit">
                {approvalTabs.map((tab) => {
                  const count = approvals.filter((approval) => approval.status === tab.value).length;
                  return (
                    <TabsTrigger key={tab.value} value={tab.value} className="gap-2">
                      {t(tab.labelKey)}
                      <Badge variant={statusBadgeVariant(tab.value)}>{count}</Badge>
                    </TabsTrigger>
                  );
                })}
              </TabsList>
              {approvalTabs.map((tab) => (
                <TabsContent key={tab.value} value={tab.value}>
                  {renderApprovalTable(
                    approvals.filter((approval) => approval.status === tab.value),
                    t("approvals.emptyTab", { status: t(tab.labelKey).toLowerCase() }),
                  )}
                </TabsContent>
              ))}
            </Tabs>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function statusBadgeVariant(status: string): "success" | "pending" | "destructive" | "secondary" {
  switch (status.toLowerCase()) {
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

function ApprovalMeta({
  label,
  value,
  code = false,
}: {
  label: string;
  value: ReactNode;
  code?: boolean;
}) {
  return (
    <div className="grid gap-1">
      <span className="text-[11px] font-bold uppercase tracking-[0.18em] text-muted-foreground">{label}</span>
      <div className={code ? "break-all font-mono text-xs text-foreground" : "text-sm text-foreground"}>{value}</div>
    </div>
  );
}

function MachineValue({ label, raw }: { label: string; raw: string }) {
  return (
    <span className="inline-flex flex-wrap items-baseline gap-2">
      <span className="font-medium text-foreground">{label}</span>
      {label.toLowerCase() !== raw.trim().toLowerCase() ? (
        <span className="font-mono text-[11px] text-muted-foreground">{raw}</span>
      ) : null}
    </span>
  );
}

function approvalRiskLevel(approval: ApprovalRequest): string {
  const payload = decisionPayloadRecord(approval.decisionPayloadJson);
  return typeof payload?.riskLevel === "string" ? payload.riskLevel.trim() : "";
}

function buildDecisionSummary(approval: ApprovalRequest): Record<string, unknown> | null {
  const payload = decisionPayloadRecord(approval.decisionPayloadJson);
  if (!payload) {
    return null;
  }

  const summary: Record<string, unknown> = {};
  for (const key of [
    "tool",
    "adapter",
    "actionType",
    "targetCategory",
    "riskLevel",
    "isScript",
    "contentEncoding",
    "contentSensitive",
  ]) {
    const value = payload[key];
    if (value !== undefined && value !== null && value !== "") {
      summary[key] = value;
    }
  }
  return Object.keys(summary).length > 0 ? summary : null;
}

function decisionPayloadRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  return value as Record<string, unknown>;
}

function formatDateTime(value: string, fallback: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? fallback : date.toLocaleString();
}

function redactApprovalTarget(value: string | undefined): string {
  const target = value?.trim();
  if (!target) {
    return "";
  }

  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(target)) {
    try {
      const parsed = new URL(target);
      parsed.username = "";
      parsed.password = "";
      parsed.hash = "";
      for (const key of parsed.searchParams.keys()) {
        if (/token|api[_-]?key|secret|password|passwd|auth|signature|credential|cookie|session|(?:^|[-_])code$/i.test(key)) {
          parsed.searchParams.set(key, "[REDACTED]");
        }
      }
      return parsed.toString();
    } catch {
      // 非标准 URL 继续使用文本级脱敏。
    }
  }

  return target
    .replace(/(bearer\s+)[^\s"'`]+/gi, "$1[REDACTED]")
    .replace(
      /((?:access[_-]?token|api[_-]?key|token|secret|password|authorization|signature|cookie)\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,&]+)/gi,
      "$1[REDACTED]",
    );
}

function approvalRequiresClientRetry(approval: ApprovalRequest): boolean {
  return Boolean(approval.adapter?.trim());
}

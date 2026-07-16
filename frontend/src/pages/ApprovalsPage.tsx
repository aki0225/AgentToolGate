import { type FormEvent, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, Clock, RefreshCw, ShieldCheck, XCircle } from "lucide-react";
import { approveApproval, connectApprovalStream, listApprovals, rejectApproval } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canReviewApprovals } from "../auth/permissions";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { useI18n, type TranslationKey } from "../i18n";
import type { ApprovalRequest } from "../types";

type Feedback = {
  kind: "success" | "error";
  text: string;
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

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!authReady) {
        setLoading(true);
        return;
      }
      if (!canReview) {
        setApprovals([]);
        setFeedback({ kind: "error", text: t("common.permissionDenied") });
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const result = await listApprovals(token, workspaceOrgId);
        if (!cancelled) {
          setApprovals(result.items);
        }
      } catch (error) {
        if (!cancelled) {
          setFeedback({
            kind: "error",
            text: error instanceof Error ? error.message : t("approvals.loadError"),
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
      const response =
        decision === "approve"
          ? await approveApproval(approvalId, { reason }, token, workspaceOrgId)
          : await rejectApproval(approvalId, { reason }, token, workspaceOrgId);

      setApprovals((current) =>
        current.map((approval) => (approval.id === response.approval.id ? response.approval : approval))
      );
      setFeedback({
        kind: "success",
        text:
          decision === "approve"
            ? t("approvals.approvedFeedback", { tool: response.approval.toolDisplayName, callId: response.toolCall.id })
            : t("approvals.rejectedFeedback", { tool: response.approval.toolDisplayName }),
      });
      setReviewDialog(null);
      setReviewReason("");
    } catch (error) {
      setFeedback({
        kind: "error",
        text: error instanceof Error ? error.message : t("approvals.actionError"),
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
                  {approval.status}
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
                {approval.status === "pending" && canReview ? (
                  <div className="flex justify-end gap-2">
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
                  </div>
                ) : (
                  <span className="block text-right text-sm text-muted-foreground">{t("approvals.reviewComplete")}</span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    );
  }

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
              ) : (
                <XCircle className="h-5 w-5 text-destructive" />
              )}
              {feedback?.kind === "success" ? t("approvals.dialog.success") : t("approvals.dialog.error")}
            </DialogTitle>
            <DialogDescription>{feedback?.text}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
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
        }
      }}>
        <DialogContent>
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
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setReviewDialog(null);
                  setReviewReason("");
                }}
              >
                {t("approvals.reviewDialog.cancel")}
              </Button>
              <Button type="submit" variant={reviewDialog?.decision === "reject" ? "destructive" : "default"} disabled={savingId === reviewDialog?.approval.id}>
                {reviewDialog?.decision === "approve" ? t("approvals.reviewDialog.confirmApprove") : t("approvals.reviewDialog.confirmReject")}
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

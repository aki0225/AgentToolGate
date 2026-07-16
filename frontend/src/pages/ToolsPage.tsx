import { FormEvent, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { PlusCircle, ShieldCheck, Wrench } from "lucide-react";
import { createTool, listTools, updateTool } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canManageTools } from "../auth/permissions";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Skeleton } from "../components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { useI18n } from "../i18n";
import { toast } from "sonner";
import type { Tool } from "../types";

const defaultForm = {
  namespace: "mock",
  name: "write",
  displayName: "Mock Write",
  description: "Requires approval and echoes arguments back for demo flows.",
  operationType: "write",
  riskLevel: "low",
  requiresApproval: true,
  inputSchemaJson: `{"type":"object"}`,
  outputSchemaJson: `{"type":"object"}`,
  enabled: true,
};

const textareaClassName =
  "min-h-28 w-full rounded-[14px] border border-input bg-background/55 px-3 py-2 text-sm text-foreground ring-offset-background transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

export function ToolsPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const canManage = canManageTools(auth.me?.user.role);
  const [tools, setTools] = useState<Tool[]>([]);
  const [form, setForm] = useState(defaultForm);
  const [loading, setLoading] = useState(true);
  const [savingToolId, setSavingToolId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        setLoading(true);
        const result = await listTools(token, workspaceOrgId);
        if (!cancelled) {
          setTools(result.items);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(error instanceof Error ? error.message : t("tools.loadError"));
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
  }, [token, workspaceOrgId]);

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    try {
      const created = await createTool(
        {
          ...form,
          inputSchemaJson: parseSchemaJsonObject(form.inputSchemaJson, t("tools.form.inputSchema"), t),
          outputSchemaJson: parseSchemaJsonObject(form.outputSchemaJson, t("tools.form.outputSchema"), t),
        },
        token,
        workspaceOrgId,
      );
      setTools((current) => [created, ...current.filter((tool) => tool.id !== created.id)]);
      setForm(defaultForm);
      toast.success(t("tools.createSuccess", { name: created.displayName }));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("tools.createError"));
    }
  }

  async function handleToggleEnabled(tool: Tool) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setSavingToolId(tool.id);
    try {
      const updated = await updateTool(
        tool.id,
        {
          enabled: !tool.enabled,
        },
        token,
        workspaceOrgId,
      );
      setTools((current) => current.map((item) => (item.id === updated.id ? updated : item)));
      toast.success(t("tools.toggleSuccess", { name: updated.displayName, state: updated.enabled ? t("tools.state.enabled") : t("tools.state.disabled") }));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("tools.updateError"));
    } finally {
      setSavingToolId(null);
    }
  }

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("tools.kicker")} title={t("tools.title")} description={t("tools.description")} icon={Wrench} />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,420px)_1fr]">
        {canManage ? (
          <Card>
            <CardHeader>
              <div className="flex items-start justify-between gap-4">
                <div>
                  <CardTitle>{t("tools.create.title")}</CardTitle>
                  <CardDescription>{t("tools.create.description")}</CardDescription>
                </div>
                <span className="grid h-10 w-10 shrink-0 place-items-center rounded-[14px] border border-primary/20 bg-primary/10 text-primary">
                  <PlusCircle className="h-5 w-5" />
                </span>
              </div>
            </CardHeader>
            <CardContent>
              <form className="grid gap-4" onSubmit={handleSubmit}>
              <div className="grid gap-2">
                <Label htmlFor="tool-namespace">{t("tools.form.namespace")}</Label>
                <Input
                  id="tool-namespace"
                  value={form.namespace}
                  onChange={(event) => setForm({ ...form, namespace: event.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-name">{t("tools.form.name")}</Label>
                <Input id="tool-name" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-display-name">{t("tools.form.displayName")}</Label>
                <Input
                  id="tool-display-name"
                  value={form.displayName}
                  onChange={(event) => setForm({ ...form, displayName: event.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-description">{t("tools.form.description")}</Label>
                <textarea
                  id="tool-description"
                  className={textareaClassName}
                  value={form.description}
                  onChange={(event) => setForm({ ...form, description: event.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-operation-type">{t("tools.form.operationType")}</Label>
                <Input
                  id="tool-operation-type"
                  value={form.operationType}
                  onChange={(event) => setForm({ ...form, operationType: event.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-risk-level">{t("tools.form.riskLevel")}</Label>
                <Select value={form.riskLevel} onValueChange={(value) => setForm({ ...form, riskLevel: value })}>
                  <SelectTrigger id="tool-risk-level">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="low">low</SelectItem>
                    <SelectItem value="medium">medium</SelectItem>
                    <SelectItem value="high">high</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <label className="flex items-center gap-3 rounded-2xl border border-border bg-white/[0.03] px-4 py-3 text-sm text-foreground">
                <input
                  type="checkbox"
                  checked={form.requiresApproval}
                  onChange={(event) => setForm({ ...form, requiresApproval: event.target.checked })}
                />
                {t("tools.form.requiresApproval")}
              </label>
              <div className="grid gap-2">
                <Label htmlFor="tool-input-schema">{t("tools.form.inputSchema")}</Label>
                <textarea
                  id="tool-input-schema"
                  className={textareaClassName}
                  rows={4}
                  value={form.inputSchemaJson}
                  onChange={(event) => setForm({ ...form, inputSchemaJson: event.target.value })}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tool-output-schema">{t("tools.form.outputSchema")}</Label>
                <textarea
                  id="tool-output-schema"
                  className={textareaClassName}
                  rows={4}
                  value={form.outputSchemaJson}
                  onChange={(event) => setForm({ ...form, outputSchemaJson: event.target.value })}
                />
              </div>
              <label className="flex items-center gap-3 rounded-2xl border border-border bg-white/[0.03] px-4 py-3 text-sm text-foreground">
                <input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />
                {t("tools.form.enabled")}
              </label>
              <Button type="submit" className="w-full">
                {t("tools.form.create")}
              </Button>
              </form>
            </CardContent>
          </Card>
        ) : (
          <Card>
            <CardHeader>
              <CardTitle>{t("tools.readOnly.title")}</CardTitle>
              <CardDescription>{t("tools.readOnly.description")}</CardDescription>
            </CardHeader>
          </Card>
        )}

        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <CardTitle>{t("tools.registry.title")}</CardTitle>
                <CardDescription>{t("tools.registry.description", { count: tools.length })}</CardDescription>
              </div>
              <Badge variant="secondary" className="gap-1">
                <Wrench className="h-3.5 w-3.5" />
                {t("tools.registry.badge")}
              </Badge>
            </div>
          </CardHeader>
          <CardContent>
            {tools.length === 0 ? (
              loading ? (
                <div className="grid gap-3">
                  <Skeleton className="h-8 w-48" />
                  <Skeleton className="h-48 w-full" />
                </div>
              ) : (
                <EmptyState
                  icon={Wrench}
                  title={t("tools.empty.title")}
                  description={t("tools.empty.description")}
                  action={
                    canManage ? (
                      <Button type="button" onClick={() => window.scrollTo({ top: 0, behavior: "smooth" })}>
                        {t("tools.form.create")}
                      </Button>
                    ) : undefined
                  }
                />
              )
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="bg-transparent hover:border-transparent">
                    <TableHead>{t("tools.table.name")}</TableHead>
                    <TableHead>{t("tools.table.operation")}</TableHead>
                    <TableHead>{t("tools.table.risk")}</TableHead>
                    <TableHead>{t("tools.table.approval")}</TableHead>
                    <TableHead>{t("tools.table.status")}</TableHead>
                    <TableHead className="text-right">{t("tools.table.action")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {tools.map((tool) => (
                    <TableRow key={tool.id}>
                      <TableCell>
                        <div className="min-w-0">
                          <div className="truncate font-bold text-foreground">{tool.displayName}</div>
                          <div className="mt-1 truncate text-xs text-muted-foreground">
                            {tool.namespace}.{tool.name}
                          </div>
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={operationBadgeVariant(tool.operationType)}>{tool.operationType}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={riskBadgeVariant(tool.riskLevel)}>{tool.riskLevel}</Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={tool.requiresApproval ? "pending" : "secondary"} className="gap-1">
                          {tool.requiresApproval && <ShieldCheck className="h-3.5 w-3.5" />}
                          {tool.requiresApproval ? t("tools.approval.required") : t("tools.approval.notRequired")}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant={tool.enabled ? "success" : "destructive"}>{tool.enabled ? t("tools.state.enabled") : t("tools.state.disabled")}</Badge>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-2">
                          {canManage ? (
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              disabled={savingToolId === tool.id}
                              onClick={() => void handleToggleEnabled(tool)}
                            >
                              {tool.enabled ? t("tools.action.disable") : t("tools.action.enable")}
                            </Button>
                          ) : null}
                          <Button asChild variant="outline" size="sm">
                            <Link to={`/tools/${tool.id}`}>{t("tools.action.open")}</Link>
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function parseSchemaJsonObject(raw: string, label: string, t: ReturnType<typeof useI18n>["t"]): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error(t("tools.form.invalidJson", { label }));
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(t("tools.form.invalidObject", { label }));
  }
  return parsed as Record<string, unknown>;
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

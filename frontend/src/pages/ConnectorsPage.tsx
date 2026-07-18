import { FormEvent, useEffect, useMemo, useState } from "react";
import { Plus, RefreshCw, ShieldCheck, Wrench } from "lucide-react";
import { createConnector, getApiErrorMessage, listConnectors, syncConnector, updateConnector } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canManageConnectors } from "../auth/permissions";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Skeleton } from "../components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { useI18n } from "../i18n";
import { toast } from "sonner";
import type { Connector } from "../types";

type ConnectorDraft = {
  type: string;
  name: string;
  displayName: string;
  configJson: string;
  enabled: boolean;
};

const defaultDraft: ConnectorDraft = {
  type: "database",
  name: "",
  displayName: "",
  configJson: `{\n  "mode": "reference"\n}`,
  enabled: true,
};

const connectorConfigTemplates: Record<string, string> = {
  database: `{\n  "mode": "reference"\n}`,
  github: `{\n  "mode": "reference"\n}`,
  http: `{\n  "mode": "reference"\n}`,
  mcp: `{\n  "transport": "sse",\n  "url": "http://localhost:8081/mcp/sse",\n  "headers": {\n    "X-Demo": "hello"\n  },\n  "headerSecretRefs": {\n    "Authorization": "MCP_WEATHER_AUTH"\n  },\n  "timeoutMs": 3000\n}`,
};

const textareaClassName =
  "min-h-36 w-full rounded-[14px] border border-input bg-background/55 px-3 py-2 text-sm text-foreground ring-offset-background transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

export function ConnectorsPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const canManage = canManageConnectors(auth.me?.user.role);
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [syncingConnectorId, setSyncingConnectorId] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Connector | null>(null);
  const [draft, setDraft] = useState<ConnectorDraft>(defaultDraft);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!canManage) {
        setConnectors([]);
        setLoading(false);
        return;
      }
      try {
        setLoading(true);
        const result = await listConnectors(token, workspaceOrgId);
        if (!cancelled) {
          setConnectors(result.items);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(getApiErrorMessage(error, t("connectors.loadError"), t("common.permissionDenied")));
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
  }, [canManage, token, workspaceOrgId]);

  const connectorCountByType = useMemo(() => {
    return connectors.reduce<Record<string, number>>((acc, connector) => {
      acc[connector.type] = (acc[connector.type] ?? 0) + 1;
      return acc;
    }, {});
  }, [connectors]);

  async function handleSync(connector: Connector) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setSyncingConnectorId(connector.id);
    try {
      const result = await syncConnector(connector.id, token, workspaceOrgId);
      toast.success(
        t("connectors.syncSuccess", {
          created: result.createdTools.length,
          updated: result.updatedTools.length,
          skipped: result.skippedTools.length,
          stale: result.staleTools.length,
        }),
      );
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("connectors.syncError"), t("common.permissionDenied")));
    } finally {
      setSyncingConnectorId(null);
    }
  }

  function openCreateDialog() {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setEditing(null);
    setDraft(defaultDraft);
    setOpen(true);
  }

  function openEditDialog(connector: Connector) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setEditing(connector);
    setDraft({
      type: connector.type,
      name: connector.name,
      displayName: connector.displayName,
      configJson: stringifyConfig(connector.configJson),
      enabled: connector.enabled,
    });
    setOpen(true);
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setSaving(true);
    try {
      const configJson = parseConnectorConfigJson(draft.configJson, t);
      const displayName = draft.displayName.trim() || `${draft.type}.${draft.name.trim()}`;
      const payload = {
        type: draft.type,
        name: draft.name.trim(),
        displayName,
        configJson,
        enabled: draft.enabled,
      };
      const saved = editing
        ? await updateConnector(
            editing.id,
            {
              displayName,
              configJson,
              enabled: draft.enabled,
            },
            token,
            workspaceOrgId,
          )
        : await createConnector(payload, token, workspaceOrgId);

      setConnectors((current) => [saved, ...current.filter((item) => item.id !== saved.id)]);
      toast.success(editing ? t("connectors.saveUpdated") : t("connectors.saveCreated"));
      setOpen(false);
      setEditing(null);
      setDraft(defaultDraft);
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("connectors.saveError"), t("common.permissionDenied")));
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("connectors.kicker")} title={t("connectors.title")} description={t("connectors.description")} icon={Wrench} />

      <div className="grid gap-4 md:grid-cols-3">
        {Object.entries(connectorCountByType).map(([type, count]) => (
          <Card key={type}>
            <CardContent className="flex items-center justify-between p-5">
              <div>
                <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{type}</div>
                <div className="mt-2 text-2xl font-bold text-foreground">{count}</div>
              </div>
              <Badge variant="secondary">{t("connectors.metrics.count", { count })}</Badge>
            </CardContent>
          </Card>
        ))}
        <Card>
          <CardContent className="flex items-center justify-between p-5">
            <div>
              <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t("connectors.metrics.total")}</div>
              <div className="mt-2 text-2xl font-bold text-foreground">{connectors.length}</div>
            </div>
            <Badge variant="success" className="gap-1">
              <ShieldCheck className="h-3.5 w-3.5" />
              {t("connectors.metrics.references")}
            </Badge>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <CardTitle>{t("connectors.list.title")}</CardTitle>
              <CardDescription>{t("connectors.list.description")}</CardDescription>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button type="button" variant="outline" onClick={() => void window.location.reload()}>
                <RefreshCw className="h-4 w-4" />
                {t("connectors.action.refresh")}
              </Button>
              {canManage ? (
                <Button type="button" onClick={openCreateDialog}>
                  <Plus className="h-4 w-4" />
                  {t("connectors.action.create")}
                </Button>
              ) : null}
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="grid gap-3">
              <Skeleton className="h-8 w-48" />
              <Skeleton className="h-40 w-full" />
            </div>
          ) : connectors.length === 0 ? (
            <EmptyState
              icon={Wrench}
              title={t("connectors.empty.title")}
              description={t("connectors.empty.description")}
              action={
                canManage ? (
                  <Button type="button" onClick={openCreateDialog}>
                    {t("connectors.form.create")}
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="bg-transparent hover:border-transparent">
                  <TableHead>{t("connectors.table.type")}</TableHead>
                  <TableHead>{t("connectors.table.name")}</TableHead>
                  <TableHead>{t("connectors.table.displayName")}</TableHead>
                  <TableHead>{t("connectors.table.status")}</TableHead>
                  <TableHead>{t("connectors.table.secretRefs")}</TableHead>
                  <TableHead>{t("connectors.table.updated")}</TableHead>
                  <TableHead className="text-right">{t("connectors.table.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {connectors.map((connector) => (
                  <TableRow key={connector.id}>
                    <TableCell>
                      <Badge variant={connectorTypeBadgeVariant(connector.type)}>{connector.type}</Badge>
                    </TableCell>
                    <TableCell className="font-mono text-sm text-foreground">{connector.name}</TableCell>
                    <TableCell>
                      <div className="font-bold text-foreground">{connector.displayName}</div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={connector.enabled ? "success" : "destructive"}>
                        {connector.enabled ? t("connectors.state.enabled") : t("connectors.state.disabled")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <ConnectorSecretRefs connector={connector} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">{new Date(connector.updatedAt).toLocaleString()}</TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-2">
                        {connector.type === "mcp" && canManage ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={syncingConnectorId === connector.id}
                            onClick={() => void handleSync(connector)}
                          >
                            {syncingConnectorId === connector.id ? t("connectors.action.syncing") : t("connectors.action.sync")}
                          </Button>
                        ) : null}
                        {canManage ? (
                          <Button type="button" variant="outline" size="sm" onClick={() => openEditDialog(connector)}>
                            {t("connectors.action.edit")}
                          </Button>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{editing ? t("connectors.dialog.editTitle") : t("connectors.dialog.createTitle")}</DialogTitle>
            <DialogDescription>{t("connectors.dialog.description")}</DialogDescription>
          </DialogHeader>

          <form className="grid gap-4" onSubmit={handleSubmit}>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="connector-type">{t("connectors.form.type")}</Label>
                <Select
                  value={draft.type}
                  onValueChange={(value) =>
                    setDraft((current) => ({
                      ...current,
                      type: value,
                      configJson: connectorConfigTemplates[value] ?? connectorConfigTemplates.database,
                    }))
                  }
                  disabled={editing !== null}
                >
                  <SelectTrigger id="connector-type">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="database">database</SelectItem>
                    <SelectItem value="github">github</SelectItem>
                    <SelectItem value="http">http</SelectItem>
                    <SelectItem value="mcp">mcp</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="connector-name">{t("connectors.form.name")}</Label>
                <Input
                  id="connector-name"
                  value={draft.name}
                  onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                  placeholder="weather"
                  disabled={editing !== null}
                />
              </div>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="connector-display-name">{t("connectors.form.displayName")}</Label>
              <Input
                id="connector-display-name"
                value={draft.displayName}
                onChange={(event) => setDraft((current) => ({ ...current, displayName: event.target.value }))}
                placeholder="Weather MCP"
              />
            </div>

            <div className="grid gap-2">
              <Label htmlFor="connector-config">{t("connectors.form.config")}</Label>
              <textarea
                id="connector-config"
                className={textareaClassName}
                rows={9}
                value={draft.configJson}
                onChange={(event) => setDraft((current) => ({ ...current, configJson: event.target.value }))}
              />
            </div>

            <label className="flex items-center gap-3 rounded-2xl border border-border bg-white/[0.03] px-4 py-3 text-sm text-foreground">
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))}
              />
              {t("connectors.form.enabled")}
            </label>

            <Button type="submit" disabled={saving}>
              {saving ? t("connectors.form.saving") : editing ? t("connectors.form.update") : t("connectors.form.create")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function connectorTypeBadgeVariant(type: string): "success" | "pending" | "secondary" {
  switch (type.toLowerCase()) {
    case "database":
      return "success";
    case "github":
    case "http":
      return "pending";
    default:
      return "secondary";
  }
}

function parseConnectorConfigJson(raw: string, t: ReturnType<typeof useI18n>["t"]): unknown {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new Error(t("connectors.form.invalidJson"));
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(t("connectors.form.invalidObject"));
  }
  return parsed;
}

function stringifyConfig(value: unknown): string {
  try {
    return JSON.stringify(value ?? {}, null, 2);
  } catch {
    return "{}";
  }
}

function ConnectorSecretRefs({ connector }: { connector: Connector }) {
  const { t } = useI18n();
  const refs = extractSecretRefs(connector.configJson);
  if (refs.length === 0) {
    return <span className="text-sm text-muted-foreground">{t("connectors.secretRefs.empty")}</span>;
  }
  return (
    <div className="flex max-w-[28rem] flex-wrap gap-2">
      {refs.map((ref) => (
        <Badge key={`${ref.path}:${ref.value}`} variant="secondary" className="whitespace-normal text-left">
          {ref.path}: {ref.value}
        </Badge>
      ))}
    </div>
  );
}

function extractSecretRefs(value: unknown): Array<{ path: string; value: string }> {
  const refs: Array<{ path: string; value: string }> = [];

  function visit(current: unknown, path: string[]) {
    if (current === null || current === undefined) {
      return;
    }
    if (Array.isArray(current)) {
      current.forEach((item, index) => visit(item, [...path, `[${index}]`]));
      return;
    }
    if (typeof current !== "object") {
      return;
    }
    Object.entries(current as Record<string, unknown>).forEach(([key, item]) => {
      const nextPath = [...path, key];
      if (isSecretReferenceKey(key)) {
        collectSecretRefValue(item, nextPath.join("."), refs);
        return;
      }
      visit(item, nextPath);
    });
  }

  visit(value, []);
  return refs;
}

function collectSecretRefValue(value: unknown, path: string, refs: Array<{ path: string; value: string }>) {
  if (typeof value === "string" && value.trim() !== "") {
    refs.push({ path, value: value.trim() });
    return;
  }
  if (value && typeof value === "object" && !Array.isArray(value)) {
    Object.entries(value as Record<string, unknown>).forEach(([key, item]) => {
      if (typeof item === "string" && item.trim() !== "") {
        refs.push({ path: `${path}.${key}`, value: item.trim() });
      }
    });
  }
}

function isSecretReferenceKey(key: string): boolean {
  const normalized = key.trim().toLowerCase();
  return normalized === "headersecretrefs" || normalized.endsWith("secretref") || normalized.endsWith("secretrefs");
}

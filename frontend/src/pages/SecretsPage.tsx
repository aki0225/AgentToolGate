import { FormEvent, useEffect, useMemo, useState } from "react";
import { KeyRound, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { createSecret, deleteSecret, getSecretUsage, listSecrets, updateSecret } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canManageSecrets } from "../auth/permissions";
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
import type { Secret, SecretInput, SecretUsageResponse } from "../types";

type SecretDraft = {
  name: string;
  description: string;
  enabled: boolean;
  secretType: string;
  valueSource: string;
  valueRef: string;
  metadataJson: string;
};

const defaultDraft: SecretDraft = {
  name: "",
  description: "",
  enabled: true,
  secretType: "token",
  valueSource: "env",
  valueRef: "",
  metadataJson: "{\n  \"scope\": \"demo\"\n}",
};

const textareaClassName =
  "min-h-28 w-full rounded-[14px] border border-input bg-background/55 px-3 py-2 text-sm text-foreground ring-offset-background transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

export function SecretsPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const canManage = canManageSecrets(auth.me?.user.role);
  const [secrets, setSecrets] = useState<Secret[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<Secret | null>(null);
  const [draft, setDraft] = useState<SecretDraft>(defaultDraft);
  const [deleteUsage, setDeleteUsage] = useState<SecretUsageResponse | null>(null);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  async function loadSecrets() {
    if (!canManage) {
      setSecrets([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const result = await listSecrets(token, workspaceOrgId);
      setSecrets(result.items);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("secrets.loadError"));
      setSecrets([]);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    let cancelled = false;
    async function load() {
      if (!canManage) {
        setSecrets([]);
        setLoading(false);
        return;
      }
      setLoading(true);
      try {
        const result = await listSecrets(token, workspaceOrgId);
        if (!cancelled) {
          setSecrets(result.items);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(error instanceof Error ? error.message : t("secrets.loadError"));
          setSecrets([]);
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

  const metrics = useMemo(
    () => [
      { label: t("secrets.metrics.total.label"), value: secrets.length, hint: t("secrets.metrics.total.hint") },
      { label: t("secrets.metrics.enabled.label"), value: secrets.filter((secret) => secret.enabled).length, hint: t("secrets.metrics.enabled.hint") },
      { label: t("secrets.metrics.env.label"), value: secrets.filter((secret) => secret.valueSource === "env").length, hint: t("secrets.metrics.env.hint") },
      { label: t("secrets.metrics.bound.label"), value: secrets.filter((secret) => (secret.bindings ?? []).length > 0).length, hint: t("secrets.metrics.bound.hint") },
    ],
    [secrets, t],
  );

  function openCreateDialog() {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setEditing(null);
    setDraft(defaultDraft);
    setOpen(true);
  }

  function openEditDialog(secret: Secret) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setEditing(secret);
    setDraft({
      name: secret.name,
      description: secret.description,
      enabled: secret.enabled,
      secretType: secret.secretType,
      valueSource: secret.valueSource,
      valueRef: secret.valueRef,
      metadataJson: stringifyMetadata(secret.metadata),
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
      const payload = secretDraftToInput(draft, t);
      if (editing) {
        await updateSecret(editing.id, payload, token, workspaceOrgId);
        toast.success(t("secrets.updateSuccess"));
      } else {
        await createSecret(payload, token, workspaceOrgId);
        toast.success(t("secrets.createSuccess"));
      }
      setOpen(false);
      setEditing(null);
      setDraft(defaultDraft);
      await loadSecrets();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("secrets.saveError"));
    } finally {
      setSaving(false);
    }
  }

  async function removeSecret(secret: Secret) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    setDeleting(true);
    try {
      const usage = await getSecretUsage(secret.id, token, workspaceOrgId);
      if (!usage.canDelete) {
        setDeleteUsage(usage);
        setDeleteDialogOpen(true);
        return;
      }
      if (!window.confirm(t("secrets.deleteConfirm", { name: secret.name }))) {
        return;
      }
      await deleteSecret(secret.id, token, workspaceOrgId);
      toast.success(t("secrets.deleteSuccess"));
      await loadSecrets();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("secrets.deleteError"));
    } finally {
      setDeleting(false);
    }
  }

  async function forceRemoveSecret() {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    if (!deleteUsage) {
      return;
    }
    setDeleting(true);
    try {
      await deleteSecret(deleteUsage.secret.id, token, workspaceOrgId, { force: true });
      toast.success(t("secrets.forceDeleteSuccess"));
      setDeleteDialogOpen(false);
      setDeleteUsage(null);
      await loadSecrets();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("secrets.forceDeleteError"));
    } finally {
      setDeleting(false);
    }
  }

  async function toggleSecret(secret: Secret) {
    if (!canManage) {
      toast.error(t("common.permissionDenied"));
      return;
    }
    try {
      await updateSecret(secret.id, { ...secretToInput(secret), enabled: !secret.enabled }, token, workspaceOrgId);
      await loadSecrets();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("secrets.toggleError"));
    }
  }

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("secrets.kicker")} title={t("secrets.title")} description={t("secrets.description")} icon={KeyRound} />

      <div className="grid gap-4 lg:grid-cols-4">
        {metrics.map((metric) => (
          <Card key={metric.label} className="rounded-[20px] p-[18px]">
            <div className="flex items-start justify-between gap-4">
              <div>
                <span className="text-xs uppercase tracking-wider text-muted-foreground">{metric.label}</span>
                <strong className="mt-2 block text-3xl font-bold text-foreground">{metric.value}</strong>
                <span className="mt-1 block text-sm text-muted-foreground">{metric.hint}</span>
              </div>
              <span className="grid h-10 w-10 place-items-center rounded-[14px] border border-primary/20 bg-primary/10 text-primary">
                <ShieldCheck className="h-5 w-5" />
              </span>
            </div>
          </Card>
        ))}
      </div>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <CardTitle>{t("secrets.list.title")}</CardTitle>
              <CardDescription>{t("secrets.list.description")}</CardDescription>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button type="button" variant="outline" onClick={() => void loadSecrets()}>
                <RefreshCw className="h-4 w-4" />
                {t("secrets.action.refresh")}
              </Button>
              {canManage ? (
                <Button type="button" onClick={openCreateDialog}>
                  <Plus className="h-4 w-4" />
                  {t("secrets.action.create")}
                </Button>
              ) : null}
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="grid gap-3">
              <Skeleton className="h-8 w-48" />
              <Skeleton className="h-48 w-full" />
            </div>
          ) : secrets.length === 0 ? (
            <EmptyState
              icon={KeyRound}
              title={t("secrets.empty.title")}
              description={t("secrets.empty.description")}
              action={
                canManage ? (
                  <Button type="button" onClick={openCreateDialog}>
                    {t("secrets.form.create")}
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="bg-transparent hover:border-transparent">
                  <TableHead>{t("secrets.table.name")}</TableHead>
                  <TableHead>{t("secrets.table.type")}</TableHead>
                  <TableHead>{t("secrets.table.source")}</TableHead>
                  <TableHead>{t("secrets.table.status")}</TableHead>
                  <TableHead>{t("secrets.table.bindings")}</TableHead>
                  <TableHead>{t("secrets.table.updated")}</TableHead>
                  <TableHead className="text-right">{t("secrets.table.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {secrets.map((secret) => (
                  <TableRow key={secret.id}>
                    <TableCell>
                      <div className="font-bold text-foreground">{secret.name}</div>
                      <div className="mt-1 max-w-[26rem] text-sm text-muted-foreground">{secret.description || "-"}</div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">{secret.secretType}</Badge>
                    </TableCell>
                    <TableCell>
                      <div className="font-mono text-sm text-foreground">{secret.valueSource}</div>
                      <div className="mt-1 font-mono text-xs text-muted-foreground">{secret.valueRef}</div>
                    </TableCell>
                    <TableCell>
                      {canManage ? (
                        <Button type="button" variant="outline" size="sm" onClick={() => void toggleSecret(secret)}>
                          {secret.enabled ? t("secrets.state.enabled") : t("secrets.state.disabled")}
                        </Button>
                      ) : (
                        <Badge variant={secret.enabled ? "success" : "destructive"}>
                          {secret.enabled ? t("secrets.state.enabled") : t("secrets.state.disabled")}
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <SecretBindings secret={secret} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">{new Date(secret.updatedAt).toLocaleString()}</TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-2">
                        {canManage ? (
                          <>
                            <Button type="button" variant="outline" size="sm" onClick={() => openEditDialog(secret)}>
                              {t("secrets.action.edit")}
                            </Button>
                            <Button type="button" variant="outline" size="sm" onClick={() => void removeSecret(secret)}>
                              <Trash2 className="h-4 w-4" />
                              {deleting ? t("secrets.action.processing") : t("secrets.action.delete")}
                            </Button>
                          </>
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
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editing ? t("secrets.dialog.editTitle") : t("secrets.dialog.createTitle")}</DialogTitle>
            <DialogDescription>{t("secrets.dialog.description")}</DialogDescription>
          </DialogHeader>

          <form className="grid gap-4" onSubmit={handleSubmit}>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="secret-name">{t("secrets.form.name")}</Label>
                <Input
                  id="secret-name"
                  value={draft.name}
                  onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                  placeholder="github_token"
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="secret-type">{t("secrets.form.type")}</Label>
                <Select value={draft.secretType} onValueChange={(value) => setDraft((current) => ({ ...current, secretType: value }))}>
                  <SelectTrigger id="secret-type">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="token">token</SelectItem>
                    <SelectItem value="api_key">api_key</SelectItem>
                    <SelectItem value="generic">generic</SelectItem>
                    <SelectItem value="oauth_like">oauth_like</SelectItem>
                    <SelectItem value="env">env</SelectItem>
                    <SelectItem value="text">text</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="secret-description">{t("secrets.form.description")}</Label>
              <Input
                id="secret-description"
                value={draft.description}
                onChange={(event) => setDraft((current) => ({ ...current, description: event.target.value }))}
                placeholder="GitHub token from backend environment"
              />
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="secret-source">{t("secrets.form.valueSource")}</Label>
                <Select value={draft.valueSource} onValueChange={(value) => setDraft((current) => ({ ...current, valueSource: value }))}>
                  <SelectTrigger id="secret-source">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="env">env</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="secret-ref">{t("secrets.form.valueRef")}</Label>
                <Input
                  id="secret-ref"
                  value={draft.valueRef}
                  onChange={(event) => setDraft((current) => ({ ...current, valueRef: event.target.value }))}
                  placeholder="GITHUB_TOKEN"
                />
              </div>
            </div>

            <div className="grid gap-2">
              <Label htmlFor="secret-metadata">{t("secrets.form.metadata")}</Label>
              <textarea
                id="secret-metadata"
                className={textareaClassName}
                rows={5}
                value={draft.metadataJson}
                onChange={(event) => setDraft((current) => ({ ...current, metadataJson: event.target.value }))}
              />
            </div>

            <label className="flex items-center gap-3 rounded-2xl border border-border bg-white/[0.03] px-4 py-3 text-sm text-foreground">
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))}
              />
              {t("secrets.form.enabled")}
            </label>

            <Button type="submit" disabled={saving}>
              {saving ? t("secrets.form.saving") : editing ? t("secrets.form.update") : t("secrets.form.create")}
            </Button>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t("secrets.deleteDialog.title")}</DialogTitle>
            <DialogDescription>{t("secrets.deleteDialog.description")}</DialogDescription>
          </DialogHeader>
          {deleteUsage ? (
            <div className="grid gap-4">
              <div className="rounded-2xl border border-border bg-white/[0.03] p-4">
                <div className="text-sm font-bold text-foreground">{deleteUsage.secret.name}</div>
                <div className="mt-1 text-sm text-muted-foreground">
                  {deleteUsage.deleteBlockedReason || t("secrets.deleteDialog.blockedFallback", { count: deleteUsage.usages.length })}
                </div>
              </div>
              <div className="grid gap-2">
                {deleteUsage.usages.map((usage) => (
                  <div
                    key={`${usage.kind}:${usage.connectorId}:${usage.field}:${usage.target}`}
                    className="rounded-2xl border border-border bg-background/50 p-3"
                  >
                    <div className="font-bold text-foreground">{usage.connectorDisplayName || usage.target}</div>
                    <div className="mt-1 text-sm text-muted-foreground">
                      {usage.connectorType}.{usage.connectorName} · {usage.field}
                    </div>
                    <div className="mt-1 font-mono text-xs text-muted-foreground">{usage.target}</div>
                  </div>
                ))}
              </div>
              <div className="flex flex-wrap justify-end gap-2">
                <Button type="button" variant="outline" onClick={() => setDeleteDialogOpen(false)}>
                  {t("secrets.deleteDialog.cancel")}
                </Button>
                <Button type="button" variant="destructive" disabled={deleting} onClick={() => void forceRemoveSecret()}>
                  {deleting ? t("secrets.deleteDialog.forcing") : t("secrets.deleteDialog.force")}
                </Button>
              </div>
            </div>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function SecretBindings({ secret }: { secret: Secret }) {
  const { t } = useI18n();
  const bindings = secret.bindings ?? [];
  if (bindings.length === 0) {
    return <span className="text-sm text-muted-foreground">{t("secrets.bindings.empty")}</span>;
  }
  return (
    <div className="flex max-w-[28rem] flex-wrap gap-2">
      {bindings.map((binding) => (
        <Badge key={`${binding.kind}:${binding.target}:${binding.field}`} variant="secondary" className="whitespace-normal text-left">
          {binding.target} · {binding.field}
        </Badge>
      ))}
    </div>
  );
}

function secretDraftToInput(draft: SecretDraft, t: ReturnType<typeof useI18n>["t"]): SecretInput {
  let metadata: unknown;
  try {
    metadata = draft.metadataJson.trim() === "" ? {} : JSON.parse(draft.metadataJson);
  } catch {
    throw new Error(t("secrets.form.invalidJson"));
  }
  if (metadata === null || typeof metadata !== "object" || Array.isArray(metadata)) {
    throw new Error(t("secrets.form.invalidObject"));
  }
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    enabled: draft.enabled,
    secretType: draft.secretType.trim(),
    valueSource: draft.valueSource.trim(),
    valueRef: draft.valueRef.trim(),
    metadata: metadata as Record<string, unknown>,
  };
}

function secretToInput(secret: Secret): SecretInput {
  const metadata = secret.metadata;
  return {
    name: secret.name,
    description: secret.description,
    enabled: secret.enabled,
    secretType: secret.secretType,
    valueSource: secret.valueSource,
    valueRef: secret.valueRef,
    metadata: metadata !== null && typeof metadata === "object" && !Array.isArray(metadata) ? (metadata as Record<string, unknown>) : {},
  };
}

function stringifyMetadata(value: unknown): string {
  try {
    if (value === null || typeof value !== "object" || Array.isArray(value)) {
      return "{}";
    }
    return JSON.stringify(value, null, 2);
  } catch {
    return "{}";
  }
}

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { CheckCircle2, Edit, Lock, PlayCircle, ShieldCheck, TriangleAlert, Trash2 } from "lucide-react";
import { createPolicy, deletePolicy, getApiErrorMessage, listPolicies, simulatePolicy, updatePolicy } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canManagePolicies as canManagePoliciesForRole } from "../auth/permissions";
import { PageHeader } from "../components/PageHeader";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import { Input } from "../components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { useI18n } from "../i18n";
import {
  governanceActionLabel,
  governanceEffectLabel,
  governanceMatchLabel,
  governanceRiskLabel,
  isGovernanceToolWildcardPattern,
  isGovernanceWildcardPattern,
} from "../lib/governanceLabels";
import { toast } from "sonner";
import type { PolicyRule, PolicyRuleInput, PolicySimulationRequest, PolicySimulationResponse } from "../types";

const emptyPolicyForm: PolicyRuleInput = {
  name: "",
  description: "",
  enabled: false,
  priority: 100,
  effect: "deny",
  connectorType: "*",
  toolNamePattern: "*",
  operationType: "*",
  riskLevel: "*",
  resourcePattern: "*",
  reason: "",
};

const emptySimulationForm: PolicySimulationRequest = {
  connectorType: "mock",
  toolName: "mock.echo",
  operationType: "mock",
  riskLevel: "low",
  resource: "",
};

export function PoliciesPage() {
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const userRole = auth.me?.user.role ?? "";
  const canManagePolicies = canManagePoliciesForRole(userRole);
  const [policies, setPolicies] = useState<PolicyRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<PolicyRuleInput>(emptyPolicyForm);
  const [simulationForm, setSimulationForm] = useState<PolicySimulationRequest>(emptySimulationForm);
  const [simulation, setSimulation] = useState<PolicySimulationResponse | null>(null);
  const [broadDenyAcknowledged, setBroadDenyAcknowledged] = useState(false);
  const effectLabels = {
    allow: governanceEffectLabel(t, "allow"),
    require_approval: governanceEffectLabel(t, "require_approval"),
    deny: governanceEffectLabel(t, "deny"),
  } satisfies Record<PolicyRule["effect"], string>;
  const broadDeny = isWildcardDenyPolicy(form);
  const requiresBroadDenyAcknowledgement = broadDeny && form.enabled;
  const draftMatches = draftPolicyMatches(form, simulationForm);

  useEffect(() => {
    setBroadDenyAcknowledged(false);
  }, [
    form.enabled,
    form.effect,
    form.connectorType,
    form.toolNamePattern,
    form.operationType,
    form.riskLevel,
    form.resourcePattern,
  ]);

  async function loadPolicies() {
    setLoading(true);
    try {
      const result = await listPolicies(token, workspaceOrgId);
      setPolicies(result.items);
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("policies.loadError"), t("common.permissionDenied")));
      setPolicies([]);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    let cancelled = false;
    async function load() {
      setLoading(true);
      try {
        const result = await listPolicies(token, workspaceOrgId);
        if (!cancelled) {
          setPolicies(result.items);
        }
      } catch (error) {
        if (!cancelled) {
          toast.error(getApiErrorMessage(error, t("policies.loadError"), t("common.permissionDenied")));
          setPolicies([]);
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
  }, [token, workspaceOrgId, t]);

  const metrics = useMemo(
    () => [
      { label: t("policies.metrics.rules.label"), value: policies.length, hint: t("policies.metrics.rules.hint") },
      { label: t("policies.metrics.enabled.label"), value: policies.filter((policy) => policy.enabled).length, hint: t("policies.metrics.enabled.hint") },
      { label: t("policies.metrics.approval.label"), value: policies.filter((policy) => policy.effect === "require_approval").length, hint: t("policies.metrics.approval.hint") },
      { label: t("policies.metrics.deny.label"), value: policies.filter((policy) => policy.effect === "deny").length, hint: t("policies.metrics.deny.hint") },
    ],
    [policies, t]
  );

  function resetForm() {
    setEditingId(null);
    setForm(emptyPolicyForm);
    setBroadDenyAcknowledged(false);
  }

  function editRule(rule: PolicyRule) {
    if (!canManagePolicies) {
      toast.error(t("policies.guardToast"));
      return;
    }
    setEditingId(rule.id);
    setForm(policyRuleToInput(rule));
    setBroadDenyAcknowledged(false);
  }

  async function saveRule() {
    if (!canManagePolicies) {
      toast.error(t("policies.guardToast"));
      return;
    }
    if (requiresBroadDenyAcknowledgement && !broadDenyAcknowledged) {
      return;
    }
    setSaving(true);
    try {
      if (editingId) {
        await updatePolicy(editingId, form, token, workspaceOrgId);
        toast.success(t("policies.updateSuccess"));
      } else {
        await createPolicy(form, token, workspaceOrgId);
        toast.success(t("policies.createSuccess"));
      }
      resetForm();
      await loadPolicies();
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("policies.saveError"), t("common.permissionDenied")));
    } finally {
      setSaving(false);
    }
  }

  async function removeRule(rule: PolicyRule) {
    if (!canManagePolicies) {
      toast.error(t("policies.guardToast"));
      return;
    }
    if (!window.confirm(t("policies.deleteConfirm", { name: rule.name }))) {
      return;
    }
    try {
      await deletePolicy(rule.id, token, workspaceOrgId);
      toast.success(t("policies.deleteSuccess"));
      await loadPolicies();
      if (editingId === rule.id) {
        resetForm();
      }
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("policies.deleteError"), t("common.permissionDenied")));
    }
  }

  async function toggleRule(rule: PolicyRule) {
    if (!canManagePolicies) {
      toast.error(t("policies.guardToast"));
      return;
    }
    if (!rule.enabled && isWildcardDenyPolicy(policyRuleToInput(rule)) && !window.confirm(t("policies.form.broadDeny.confirm"))) {
      return;
    }
    try {
      await updatePolicy(rule.id, { ...policyRuleToInput(rule), enabled: !rule.enabled }, token, workspaceOrgId);
      await loadPolicies();
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("policies.toggleError"), t("common.permissionDenied")));
    }
  }

  async function runSimulation() {
    try {
      const result = await simulatePolicy(simulationForm, token, workspaceOrgId);
      setSimulation(result);
    } catch (error) {
      toast.error(getApiErrorMessage(error, t("policies.simulateError"), t("common.permissionDenied")));
      setSimulation(null);
    }
  }

  return (
    <div className="grid gap-6">
      <PageHeader kicker={t("policies.kicker")} title={t("policies.title")} description={t("policies.description")} icon={Lock} />

      {!canManagePolicies ? (
        <div className="rounded-[20px] border border-accent/20 bg-accent/10 px-5 py-4 text-sm text-foreground">
          <div className="flex flex-wrap items-center gap-2 font-bold">
            <Lock className="h-4 w-4 text-accent" />
            <span>{t("policies.readOnly.title")}</span>
            <Badge variant="pending">{t("policies.readOnly.role")}: {userRole.trim() || "unknown"}</Badge>
          </div>
          <p className="m-0 mt-2 text-muted-foreground">{t("policies.readOnly.description")}</p>
        </div>
      ) : null}

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

      <div className={canManagePolicies ? "grid gap-6 xl:grid-cols-[minmax(0,1.2fr)_minmax(360px,0.8fr)]" : "grid gap-6"}>
        <Card>
          <CardHeader>
            <CardTitle>{t("policies.rules.title")}</CardTitle>
            <CardDescription>{t("policies.rules.description")}</CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <p className="m-0 text-sm text-muted-foreground">{t("policies.rules.loading")}</p>
            ) : policies.length === 0 ? (
              <p className="m-0 text-sm text-muted-foreground">{t("policies.rules.empty")}</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow className="bg-transparent hover:border-transparent">
                    <TableHead>{t("policies.table.enabled")}</TableHead>
                    <TableHead>{t("policies.table.priority")}</TableHead>
                    <TableHead>{t("policies.table.name")}</TableHead>
                    <TableHead>{t("policies.table.effect")}</TableHead>
                    <TableHead>{t("policies.table.match")}</TableHead>
                    <TableHead>{t("policies.table.reason")}</TableHead>
                    {canManagePolicies ? <TableHead className="text-right">{t("policies.table.actions")}</TableHead> : null}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {policies.map((policyRule) => (
                    <TableRow key={policyRule.id}>
                      <TableCell>
                        {canManagePolicies ? (
                          <Button type="button" variant="outline" size="sm" onClick={() => void toggleRule(policyRule)}>
                            {policyRule.enabled ? t("policies.state.enabled") : t("policies.state.disabled")}
                          </Button>
                        ) : (
                          <Badge variant={policyRule.enabled ? "success" : "secondary"}>
                            {policyRule.enabled ? t("policies.state.enabled") : t("policies.state.disabled")}
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="font-bold text-foreground">{policyRule.priority}</TableCell>
                      <TableCell>
                        <div className="font-bold text-foreground">{policyRule.name}</div>
                        <div className="mt-1 max-w-[22rem] text-sm text-muted-foreground">{policyRule.description || "-"}</div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={effectBadgeVariant(policyRule.effect)}>{effectLabels[policyRule.effect]}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-2">
                          <Badge variant="secondary" title={policyRule.connectorType}>
                            {t("policies.table.connector")}: {governanceMatchLabel(t, policyRule.connectorType)}
                          </Badge>
                          <Badge variant="secondary" title={policyRule.toolNamePattern}>
                            {t("policies.table.tool")}: {governanceMatchLabel(t, policyRule.toolNamePattern)}
                          </Badge>
                          <Badge variant="secondary" title={policyRule.operationType}>
                            {t("policies.table.operation")}: {isGovernanceWildcardPattern(policyRule.operationType) ? governanceMatchLabel(t, policyRule.operationType) : governanceActionLabel(t, policyRule.operationType)}
                          </Badge>
                          <Badge variant="secondary" title={policyRule.riskLevel}>
                            {t("policies.table.risk")}: {isGovernanceWildcardPattern(policyRule.riskLevel) ? governanceMatchLabel(t, policyRule.riskLevel) : governanceRiskLabel(t, policyRule.riskLevel)}
                          </Badge>
                          <Badge variant="secondary" title={policyRule.resourcePattern}>
                            {t("policies.table.resource")}: {governanceMatchLabel(t, policyRule.resourcePattern)}
                          </Badge>
                        </div>
                      </TableCell>
                      <TableCell className="max-w-[20rem] text-sm text-muted-foreground">{policyRule.reason || "-"}</TableCell>
                      {canManagePolicies ? (
                        <TableCell>
                          <div className="flex justify-end gap-2">
                            <Button type="button" variant="outline" size="sm" onClick={() => editRule(policyRule)}>
                              <Edit className="h-4 w-4" />
                              {t("policies.action.edit")}
                            </Button>
                            <Button type="button" variant="outline" size="sm" onClick={() => void removeRule(policyRule)}>
                              <Trash2 className="h-4 w-4" />
                              {t("policies.action.delete")}
                            </Button>
                          </div>
                        </TableCell>
                      ) : null}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        {canManagePolicies ? (
          <Card>
            <CardHeader>
              <CardTitle>{editingId ? t("policies.form.editTitle") : t("policies.form.createTitle")}</CardTitle>
              <CardDescription>{t("policies.form.description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              <FormField label={t("policies.form.name")}>
                <Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
              </FormField>
              <FormField label={t("policies.form.summary")}>
                <Input value={form.description} onChange={(event) => setForm({ ...form, description: event.target.value })} />
              </FormField>
              <div className="grid gap-4 md:grid-cols-2">
                <FormField label={t("policies.form.enabled")}>
                  <select
                    className="h-10 rounded-md border border-input bg-background px-3 text-sm"
                    value={String(form.enabled)}
                    onChange={(event) => setForm({ ...form, enabled: event.target.value === "true" })}
                  >
                    <option value="true">{t("policies.form.enabledOption")}</option>
                    <option value="false">{t("policies.form.disabledOption")}</option>
                  </select>
                </FormField>
                <FormField label={t("policies.form.priority")}>
                  <Input type="number" value={form.priority} onChange={(event) => setForm({ ...form, priority: Number(event.target.value) })} />
                </FormField>
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <FormField label={t("policies.form.effect")}>
                  <select
                    className="h-10 rounded-md border border-input bg-background px-3 text-sm"
                    value={form.effect}
                    onChange={(event) => setForm({ ...form, effect: event.target.value as PolicyRule["effect"] })}
                  >
                    <option value="allow">{effectLabels.allow}</option>
                    <option value="require_approval">{effectLabels.require_approval}</option>
                    <option value="deny">{effectLabels.deny}</option>
                  </select>
                </FormField>
                <FormField label={t("policies.form.connector")}>
                  <Input value={form.connectorType} onChange={(event) => setForm({ ...form, connectorType: event.target.value })} />
                </FormField>
              </div>
              <FormField label={t("policies.form.toolPattern")}>
                <Input value={form.toolNamePattern} onChange={(event) => setForm({ ...form, toolNamePattern: event.target.value })} />
              </FormField>
              <div className="grid gap-4 md:grid-cols-2">
                <FormField label={t("policies.form.operation")}>
                  <Input value={form.operationType} onChange={(event) => setForm({ ...form, operationType: event.target.value })} />
                </FormField>
                <FormField label={t("policies.form.risk")}>
                  <Input value={form.riskLevel} onChange={(event) => setForm({ ...form, riskLevel: event.target.value })} />
                </FormField>
              </div>
              <FormField label={t("policies.form.resource")}>
                <Input value={form.resourcePattern} onChange={(event) => setForm({ ...form, resourcePattern: event.target.value })} />
              </FormField>
              <FormField label={t("policies.form.reason")}>
                <textarea
                  className="min-h-24 rounded-md border border-input bg-background px-3 py-2 text-sm outline-none transition-colors focus:border-ring"
                  value={form.reason}
                  onChange={(event) => setForm({ ...form, reason: event.target.value })}
                />
              </FormField>
              {broadDeny ? (
                <div className="grid gap-3 border-l-2 border-destructive px-4 py-3 text-sm">
                  <div className="flex items-center gap-2 font-bold text-foreground">
                    <TriangleAlert className="h-4 w-4 text-destructive" />
                    {t("policies.form.broadDeny.title")}
                  </div>
                  <p className="m-0 text-muted-foreground">
                    {form.enabled ? t("policies.form.broadDeny.enabled") : t("policies.form.broadDeny.disabled")}
                  </p>
                  {form.enabled ? (
                    <label className="flex items-start gap-3 text-foreground">
                      <input
                        type="checkbox"
                        className="mt-0.5 h-4 w-4 accent-[hsl(var(--destructive))]"
                        checked={broadDenyAcknowledged}
                        onChange={(event) => setBroadDenyAcknowledged(event.target.checked)}
                      />
                      <span>{t("policies.form.broadDeny.confirm")}</span>
                    </label>
                  ) : null}
                </div>
              ) : null}
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  disabled={saving || (requiresBroadDenyAcknowledgement && !broadDenyAcknowledged)}
                  onClick={() => void saveRule()}
                >
                  {editingId ? t("policies.form.update") : t("policies.form.create")}
                </Button>
                <Button type="button" variant="outline" onClick={resetForm}>
                  {t("policies.form.reset")}
                </Button>
              </div>
            </CardContent>
          </Card>
        ) : null}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("policies.simulator.title")}</CardTitle>
          <CardDescription>{t("policies.simulator.description")}</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          {canManagePolicies ? (
            <div className="grid gap-2 border-l-2 border-accent px-4 py-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-bold text-foreground">{t("policies.simulator.draft.title")}</span>
                <Badge variant={draftMatches ? "pending" : "secondary"}>
                  {draftMatches ? t("policies.simulator.hit") : t("policies.simulator.skip")}
                </Badge>
              </div>
              <p className="m-0 text-sm text-muted-foreground">{t("policies.simulator.draft.description")}</p>
              <p className="m-0 text-sm text-foreground">
                {draftMatches
                  ? t("policies.simulator.draft.matches", { effect: effectLabels[form.effect] })
                  : t("policies.simulator.draft.skips")}
                {!form.enabled ? ` ${t("policies.simulator.draft.disabled")}` : ""}
              </p>
            </div>
          ) : null}
          <div className="grid gap-4 md:grid-cols-5">
            <FormField label={t("policies.simulator.connector")}>
              <Input value={simulationForm.connectorType} onChange={(event) => setSimulationForm({ ...simulationForm, connectorType: event.target.value })} />
            </FormField>
            <FormField label={t("policies.simulator.tool")}>
              <Input value={simulationForm.toolName} onChange={(event) => setSimulationForm({ ...simulationForm, toolName: event.target.value })} />
            </FormField>
            <FormField label={t("policies.simulator.operation")}>
              <Input value={simulationForm.operationType} onChange={(event) => setSimulationForm({ ...simulationForm, operationType: event.target.value })} />
            </FormField>
            <FormField label={t("policies.simulator.risk")}>
              <Input value={simulationForm.riskLevel} onChange={(event) => setSimulationForm({ ...simulationForm, riskLevel: event.target.value })} />
            </FormField>
            <FormField label={t("policies.simulator.resource")}>
              <Input value={simulationForm.resource} onChange={(event) => setSimulationForm({ ...simulationForm, resource: event.target.value })} />
            </FormField>
          </div>
          <div>
            <Button type="button" onClick={() => void runSimulation()}>
              <PlayCircle className="h-4 w-4" />
              {t("policies.simulator.run")}
            </Button>
          </div>

          {simulation ? (
            <div className="grid gap-4 rounded-[20px] border border-border bg-white/[0.03] p-4">
              <div className="flex flex-wrap items-center gap-3">
                <Badge variant={effectBadgeVariant(simulation.decision)}>{effectLabels[simulation.decision]}</Badge>
                <span className="text-sm text-muted-foreground">
                  {matchedSimulationRule(simulation)
                    ? t("policies.simulator.matched", { rule: matchedSimulationRule(simulation) })
                    : t("policies.simulator.defaultPolicy")}
                </span>
              </div>
              <p className="m-0 text-sm text-muted-foreground">{simulation.explanation}</p>
              <div className="grid gap-2">
                <div className="text-xs font-bold uppercase tracking-[0.18em] text-muted-foreground">{t("policies.simulator.trace")}</div>
                <div className="relative grid gap-0">
                  {simulation.evaluationTrace.map((item, index) => {
                    const isLast = index === simulation.evaluationTrace.length - 1;
                    return (
                      <div key={`${item.ruleId ?? item.ruleName ?? "default"}-${index}`} className="relative grid grid-cols-[2rem_minmax(0,1fr)] gap-3 pb-4 last:pb-0">
                        {!isLast ? (
                          <span
                            aria-hidden="true"
                            className="absolute bottom-0 left-4 top-8 border-l border-dashed border-border/80"
                          />
                        ) : null}
                        <span
                          className={
                            item.matched
                              ? "relative z-10 grid h-8 w-8 place-items-center rounded-full border border-primary/30 bg-primary/[0.14] text-primary shadow-[0_0_20px_rgba(94,234,212,0.18)]"
                              : "relative z-10 grid h-8 w-8 place-items-center rounded-full border border-border bg-white/[0.035] text-muted-foreground"
                          }
                        >
                          {item.matched ? <CheckCircle2 className="h-4 w-4" /> : <span className="h-1.5 w-1.5 rounded-full bg-current" />}
                        </span>
                        <div
                          className={
                            item.matched
                              ? "rounded-2xl border border-primary/20 bg-[linear-gradient(90deg,rgba(94,234,212,0.08),rgba(96,165,250,0.035))] px-4 py-3 text-sm shadow-[0_10px_28px_rgba(2,6,23,0.16)]"
                              : "rounded-2xl border border-border bg-background/30 px-4 py-3 text-sm"
                          }
                        >
                          <div className="flex flex-wrap items-center gap-2 font-semibold text-foreground">
                            <span>{item.ruleName || item.ruleId || t("policies.simulator.defaultPolicy")}</span>
                            <Badge variant={item.matched ? "success" : "secondary"}>
                              {item.matched ? t("policies.simulator.hit") : t("policies.simulator.skip")}
                            </Badge>
                            {item.decision ? (
                              <Badge variant={decisionBadgeVariant(item.decision)} title={item.decision}>
                                {governanceEffectLabel(t, item.decision)}
                              </Badge>
                            ) : null}
                          </div>
                          <div className="mt-2 text-muted-foreground">
                            {item.decision ? `${governanceEffectLabel(t, item.decision)} · ` : ""}
                            {item.reason}
                          </div>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

function FormField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-2">
      <span className="text-xs font-bold uppercase tracking-[0.18em] text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function policyRuleToInput(rule: PolicyRule): PolicyRuleInput {
  return {
    name: rule.name,
    description: rule.description,
    enabled: rule.enabled,
    priority: rule.priority,
    effect: rule.effect,
    connectorType: rule.connectorType,
    toolNamePattern: rule.toolNamePattern,
    operationType: rule.operationType,
    riskLevel: rule.riskLevel,
    resourcePattern: rule.resourcePattern,
    reason: rule.reason,
  };
}

function matchedSimulationRule(simulation: PolicySimulationResponse): string {
  return simulation.matchedRuleName?.trim() || simulation.matchedRuleId?.trim() || "";
}

function effectBadgeVariant(effect: PolicyRule["effect"]): "success" | "pending" | "destructive" {
  switch (effect) {
    case "allow":
      return "success";
    case "require_approval":
      return "pending";
    case "deny":
      return "destructive";
  }
}

function decisionBadgeVariant(decision: string): "success" | "pending" | "destructive" | "secondary" {
  switch (decision) {
    case "allow":
      return "success";
    case "require_approval":
      return "pending";
    case "deny":
      return "destructive";
    default:
      return "secondary";
  }
}

function isWildcardDenyPolicy(policy: PolicyRuleInput): boolean {
  return (
    policy.effect === "deny" &&
    isGovernanceWildcardPattern(policy.connectorType) &&
    isGovernanceToolWildcardPattern(policy.toolNamePattern) &&
    isGovernanceWildcardPattern(policy.operationType) &&
    isGovernanceWildcardPattern(policy.riskLevel) &&
    isGovernanceWildcardPattern(policy.resourcePattern)
  );
}

function draftPolicyMatches(policy: PolicyRuleInput, simulation: PolicySimulationRequest): boolean {
  const normalizedSimulation = normalizeDraftSimulation(simulation);
  if (!normalizedSimulation) {
    return false;
  }
  return (
    globMatch(policy.connectorType, normalizedSimulation.connectorType) &&
    globMatch(policy.toolNamePattern, normalizedSimulation.toolName) &&
    globMatch(policy.operationType, normalizedSimulation.operationType) &&
    globMatch(policy.riskLevel, normalizedSimulation.riskLevel) &&
    globMatch(policy.resourcePattern, normalizedSimulation.resource)
  );
}

function normalizeDraftSimulation(simulation: PolicySimulationRequest): PolicySimulationRequest | null {
  const toolName = simulation.toolName.trim().toLowerCase();
  const separator = toolName.indexOf(".");
  if (separator <= 0 || separator === toolName.length - 1) {
    return null;
  }

  const namespace = toolName.slice(0, separator);
  const connectorInput = simulation.connectorType.trim().toLowerCase();
  return {
    connectorType: connectorInput === "" || connectorInput === "*" ? policyConnectorTypeForNamespace(namespace) : connectorInput,
    toolName,
    operationType: simulation.operationType.trim().toLowerCase() || "*",
    riskLevel: normalizeDraftRiskLevel(simulation.riskLevel),
    resource: simulation.resource.trim(),
  };
}

function policyConnectorTypeForNamespace(namespace: string): string {
  const normalized = namespace.trim().toLowerCase();
  return normalized.startsWith("mcp_") ? "mcp" : normalized;
}

function normalizeDraftRiskLevel(value: string): string {
  switch (value.trim().toLowerCase()) {
    case "critical":
      return "critical";
    case "high":
      return "high";
    case "medium":
      return "medium";
    default:
      return "low";
  }
}

function globMatch(patternValue: string, candidateValue: string): boolean {
  const pattern = patternValue.trim().toLowerCase();
  const candidate = candidateValue.trim().toLowerCase();
  if (isGovernanceWildcardPattern(pattern)) {
    return true;
  }
  if (!pattern.includes("*")) {
    return pattern === candidate;
  }

  const parts = pattern.split("*");
  let position = 0;
  for (const [index, part] of parts.entries()) {
    if (!part) {
      continue;
    }
    const found = candidate.indexOf(part, position);
    if (found < 0 || (index === 0 && !pattern.startsWith("*") && found !== 0)) {
      return false;
    }
    position = found + part.length;
  }

  const last = parts.at(-1) ?? "";
  return last === "" || pattern.endsWith("*") || candidate.endsWith(last);
}

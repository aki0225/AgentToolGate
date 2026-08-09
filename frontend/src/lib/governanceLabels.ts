import type { TranslationKey } from "../i18n";

type Translator = (key: TranslationKey, values?: Record<string, string | number>) => string;

const statusKeys: Record<string, TranslationKey> = {
  success: "governance.status.success",
  failed: "governance.status.failed",
  approval_required: "governance.status.approvalRequired",
  denied: "governance.status.denied",
  rate_limited: "governance.status.rateLimited",
  pending: "governance.status.pending",
  approved: "governance.status.approved",
  rejected: "governance.status.rejected",
  expired: "governance.status.expired",
  consumed: "governance.status.consumed",
  none: "governance.status.none",
};

const effectKeys: Record<string, TranslationKey> = {
  allow: "governance.effect.allow",
  require_approval: "governance.effect.requireApproval",
  approval_required: "governance.effect.requireApproval",
  deny: "governance.effect.deny",
  denied: "governance.effect.deny",
};

const riskKeys: Record<string, TranslationKey> = {
  low: "governance.risk.low",
  medium: "governance.risk.medium",
  high: "governance.risk.high",
  critical: "governance.risk.critical",
};

const actionKeys: Record<string, TranslationKey> = {
  read: "governance.action.read",
  write: "governance.action.write",
  create: "governance.action.create",
  update: "governance.action.update",
  delete: "governance.action.delete",
  execute: "governance.action.execute",
  search: "governance.action.search",
  network: "governance.action.network",
};

export function governanceStatusLabel(t: Translator, value: string): string {
  return translatedValue(t, value, statusKeys);
}

export function governanceEffectLabel(t: Translator, value: string): string {
  return translatedValue(t, value, effectKeys);
}

export function governanceRiskLabel(t: Translator, value: string): string {
  return translatedValue(t, value, riskKeys);
}

export function governanceActionLabel(t: Translator, value: string): string {
  return translatedValue(t, value, actionKeys);
}

export function governanceMatchLabel(t: Translator, value: string): string {
  const normalized = value.trim();
  return isGovernanceWildcardPattern(normalized) ? t("governance.match.all") : normalized;
}

export function isGovernanceWildcardPattern(value: string): boolean {
  const normalized = value.trim();
  return normalized === "" || /^\*+$/.test(normalized);
}

export function isGovernanceToolWildcardPattern(value: string): boolean {
  const normalized = value.trim();
  return isGovernanceWildcardPattern(normalized) || normalized === "*.*";
}

function translatedValue(t: Translator, value: string, keys: Record<string, TranslationKey>): string {
  const normalized = value.trim().toLowerCase();
  const key = keys[normalized];
  return key ? t(key) : value.trim() || t("governance.value.unknown");
}

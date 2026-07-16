export function normalizeRole(role?: string | null): string {
  return role?.trim().toLowerCase().replaceAll("_", "-") ?? "";
}

export function canViewDashboard(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver", "viewer"]);
}

export function canViewTools(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver", "viewer", "agent", "service-account"]);
}

export function canManageTools(role?: string | null): boolean {
  return isWorkspaceAdmin(role);
}

export function canExecuteTools(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver", "agent", "service-account"]);
}

export function canViewApprovals(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver"]);
}

export function canReviewApprovals(role?: string | null): boolean {
  return canViewApprovals(role);
}

export function canViewPolicies(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver", "viewer"]);
}

export function canManagePolicies(role?: string | null): boolean {
  return isWorkspaceAdmin(role);
}

export function canManageSecrets(role?: string | null): boolean {
  return isWorkspaceAdmin(role);
}

export function canManageConnectors(role?: string | null): boolean {
  return isWorkspaceAdmin(role);
}

export function canViewAudit(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin", "approver", "viewer"]);
}

export function isWorkspaceAdmin(role?: string | null): boolean {
  return roleIn(role, ["owner", "admin"]);
}

function roleIn(role: string | null | undefined, allowedRoles: string[]): boolean {
  const normalized = normalizeRole(role);
  return allowedRoles.includes(normalized);
}

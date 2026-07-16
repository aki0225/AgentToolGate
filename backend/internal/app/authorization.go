package app

import (
	"fmt"
	"strings"
)

const (
	roleOwner          = "owner"
	roleAdmin          = "admin"
	roleApprover       = "approver"
	roleViewer         = "viewer"
	roleAgent          = "agent"
	roleServiceAccount = "service-account"
)

func requireRole(reqCtx RequestContext, allowedRoles ...string) error {
	if roleAllowed(reqCtx.User.Role, allowedRoles...) {
		return nil
	}
	return forbidden("role %q is not allowed", reqCtx.User.Role)
}

func requireViewDashboard(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleViewer)
}

func requireViewTools(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleViewer, roleAgent, roleServiceAccount)
}

func requireManageTools(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin)
}

func requireExecuteTools(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleAgent, roleServiceAccount)
}

func requireViewApprovals(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover)
}

func requireReviewApprovals(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover)
}

func requireViewPolicies(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleViewer)
}

func requireManagePolicies(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin)
}

func requireManageSecrets(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin)
}

func requireManageConnectors(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin)
}

func requireViewAudit(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleViewer)
}

func requireViewDatabaseSchema(reqCtx RequestContext) error {
	return requireRole(reqCtx, roleOwner, roleAdmin, roleApprover, roleViewer)
}

func canReviewApprovals(role string) bool {
	return roleAllowed(role, roleOwner, roleAdmin, roleApprover)
}

func canManagePolicies(role string) bool {
	return roleAllowed(role, roleOwner, roleAdmin)
}

func roleAllowed(role string, allowedRoles ...string) bool {
	normalized := normalizeRole(role)
	if normalized == "" {
		return false
	}
	for _, allowed := range allowedRoles {
		if normalized == normalizeRole(allowed) {
			return true
		}
	}
	return false
}

func normalizeRole(role string) string {
	normalized := strings.ToLower(strings.TrimSpace(role))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return normalized
}

func forbidden(format string, args ...any) error {
	// 具体原因只进入日志；API 响应由 respondError 统一稳定为 forbidden，避免前端依赖细碎文案。
	return fmt.Errorf("%w: %s", errForbidden, fmt.Sprintf(format, args...))
}

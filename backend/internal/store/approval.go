package store

import (
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
)

const defaultApprovalTTL = 24 * time.Hour

func approvalExpiresAt(now time.Time, ttl time.Duration) time.Time {
	if ttl == 0 {
		ttl = defaultApprovalTTL
	}
	return now.UTC().Add(ttl)
}

func approvalIsExpired(now time.Time, approval model.ApprovalRequest) bool {
	if !strings.EqualFold(strings.TrimSpace(approval.Status), "pending") {
		return false
	}
	if approval.ExpiresAt.IsZero() {
		return false
	}
	return !approval.ExpiresAt.After(now.UTC())
}

func expireApprovalIfNeeded(approval *model.ApprovalRequest, now time.Time) bool {
	if approval == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(approval.Status), "pending") {
		return false
	}
	if approval.ExpiresAt.IsZero() || approval.ExpiresAt.After(now.UTC()) {
		return false
	}
	approval.Status = "expired"
	approval.UpdatedAt = now.UTC()
	return true
}

func approvalFingerprintIsActive(approval model.ApprovalRequest) bool {
	switch strings.ToLower(strings.TrimSpace(approval.Status)) {
	case "pending", "approved":
		return true
	default:
		return false
	}
}

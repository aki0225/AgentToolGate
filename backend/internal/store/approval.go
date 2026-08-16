package store

import (
	"fmt"
	"strings"
	"time"

	"agenttoolgate/backend/internal/model"
)

const defaultApprovalTTL = 24 * time.Hour

var approvalStatuses = [...]string{"pending", "approved", "rejected", "expired", "consumed"}

func NormalizeApprovalStatus(status string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		return "", true
	}
	for _, allowed := range approvalStatuses {
		if normalized == allowed {
			return normalized, true
		}
	}
	return "", false
}

func normalizeApprovalPage(page, pageSize int) (int, int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return 0, 0, 0, fmt.Errorf("approval page exceeds supported range")
	}
	return page, pageSize, (page - 1) * pageSize, nil
}

func newApprovalStatusCounts() map[string]int64 {
	counts := make(map[string]int64, len(approvalStatuses))
	for _, status := range approvalStatuses {
		counts[status] = 0
	}
	return counts
}

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

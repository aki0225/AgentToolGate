package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const boundedApprovalTestRows = 1000

func TestMemoryListApprovalRequestsPageContract(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertApprovalRequestsPageContract(t, st, func(ctx context.Context, workspaceID string, createdAt time.Time) {
		st.mu.Lock()
		defer st.mu.Unlock()
		for id, approval := range st.approvals {
			if approval.WorkspaceID != workspaceID {
				continue
			}
			approval.CreatedAt = createdAt
			st.approvals[id] = approval
		}
	})
}

func TestSQLiteListApprovalRequestsPageContract(t *testing.T) {
	t.Parallel()

	st := newTestSQLiteStore(t).(*SQLiteStore)
	assertApprovalRequestsPageContract(t, st, func(ctx context.Context, workspaceID string, createdAt time.Time) {
		if _, err := st.db.ExecContext(ctx, `
			UPDATE approval_requests
			SET created_at = ?
			WHERE workspace_id = ?
		`, sqliteTimestamp(createdAt), workspaceID); err != nil {
			t.Fatalf("set sqlite approval timestamps: %v", err)
		}
	})
}

func TestPostgresListApprovalRequestsPageContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	assertApprovalRequestsPageContract(t, st, func(ctx context.Context, workspaceID string, createdAt time.Time) {
		if _, err := st.pool.Exec(ctx, `
			UPDATE approval_requests
			SET created_at = $2
			WHERE workspace_id = $1
		`, workspaceID, createdAt); err != nil {
			t.Fatalf("set postgres approval timestamps: %v", err)
		}
	})
}

func TestMemoryListApprovalRequestsPageBoundsThousandRows(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertApprovalRequestsPageBoundsThousandRows(t, st, func(ctx context.Context, workspaceID string, count int) error {
		for index := 0; index < count; index++ {
			if _, err := st.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
				WorkspaceID:     workspaceID,
				ToolKey:         "mock.bulk",
				ToolDisplayName: "Bulk Approval",
				RequestedBy:     "bulk-requester",
				TTL:             time.Hour,
			}); err != nil {
				return fmt.Errorf("create memory approval %d: %w", index, err)
			}
		}
		return nil
	})
}

func TestSQLiteListApprovalRequestsPageBoundsThousandRows(t *testing.T) {
	t.Parallel()

	st := newTestSQLiteStore(t).(*SQLiteStore)
	assertApprovalRequestsPageBoundsThousandRows(t, st, func(ctx context.Context, workspaceID string, count int) error {
		return seedSQLiteApprovalRequests(ctx, st, workspaceID, count)
	})
}

func TestPostgresListApprovalRequestsPageBoundsThousandRows(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	assertApprovalRequestsPageBoundsThousandRows(t, st, func(ctx context.Context, workspaceID string, count int) error {
		return seedPostgresApprovalRequests(ctx, st, workspaceID, count)
	})
}

func TestMemoryApprovalTTLMutationContract(t *testing.T) {
	t.Parallel()

	st := NewMemoryStore()
	assertApprovalTTLMutationContract(t, st, func(_ context.Context, workspaceID, approvalID string) error {
		st.mu.Lock()
		defer st.mu.Unlock()
		approval, ok := st.approvals[approvalID]
		if !ok || approval.WorkspaceID != workspaceID {
			return ErrNotFound
		}
		approval.Status = "pending"
		approval.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		st.approvals[approvalID] = approval
		return nil
	})
}

func TestSQLiteApprovalTTLMutationContract(t *testing.T) {
	t.Parallel()

	st := newTestSQLiteStore(t).(*SQLiteStore)
	assertApprovalTTLMutationContract(t, st, func(ctx context.Context, workspaceID, approvalID string) error {
		_, err := st.db.ExecContext(ctx, `
			UPDATE approval_requests
			SET status = 'pending', expires_at = ?
			WHERE workspace_id = ? AND id = ?
		`, sqliteTimestamp(time.Now().UTC().Add(-time.Minute)), workspaceID, approvalID)
		return err
	})
}

func TestPostgresApprovalTTLMutationContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("new postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	assertApprovalTTLMutationContract(t, st, func(ctx context.Context, workspaceID, approvalID string) error {
		_, err := st.pool.Exec(ctx, `
			UPDATE approval_requests
			SET status = 'pending', expires_at = NOW() - INTERVAL '1 minute'
			WHERE workspace_id = $1 AND id = $2
		`, workspaceID, approvalID)
		return err
	})
}

func assertApprovalRequestsPageContract(
	t *testing.T,
	st Store,
	setCreatedAt func(context.Context, string, time.Time),
) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval Page " + suffix,
		Slug:                  "approval-page-" + suffix,
		ZitadelOrganizationID: "org-approval-page-" + suffix,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	tool, err := st.CreateTool(ctx, model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mock",
		Name:             "approval_page",
		DisplayName:      "Approval Page",
		OperationType:    "write",
		RiskLevel:        "medium",
		RequiresApproval: true,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}

	type seededApproval struct {
		approval model.ApprovalRequest
		call     model.ToolCall
	}
	seed := func(status string, ttl time.Duration) seededApproval {
		t.Helper()
		approval, createErr := st.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
			WorkspaceID:     workspace.ID,
			ToolKey:         tool.Key(),
			ToolDisplayName: tool.DisplayName,
			RequestedBy:     "requester",
			Reason:          "pagination contract",
			TTL:             ttl,
		})
		if createErr != nil {
			t.Fatalf("create %s approval: %v", status, createErr)
		}
		call, createErr := st.CreateToolCall(ctx, model.CreateToolCallInput{
			WorkspaceID:        workspace.ID,
			RequestID:          "req-approval-page-" + uuid.NewString(),
			ToolID:             tool.ID,
			ToolKey:            tool.Key(),
			Status:             "approval_required",
			RiskLevel:          "medium",
			PolicyDecision:     "require_approval",
			ApprovalID:         approval.ID,
			InputRedactedJSON:  json.RawMessage(`{}`),
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{}`),
		})
		if createErr != nil {
			t.Fatalf("create %s tool call: %v", status, createErr)
		}
		if status != "pending" && status != "expired" {
			approval, createErr = st.UpdateApprovalRequest(ctx, workspace.ID, approval.ID, model.UpdateApprovalRequestInput{
				Status:     status,
				ReviewedBy: "reviewer",
			})
			if createErr != nil {
				t.Fatalf("set approval status %s: %v", status, createErr)
			}
		}
		return seededApproval{approval: approval, call: call}
	}

	seeded := []seededApproval{
		seed("pending", time.Hour),
		seed("pending", time.Hour),
		seed("approved", time.Hour),
		seed("rejected", time.Hour),
		seed("expired", -time.Minute),
		seed("consumed", time.Hour),
	}
	originalCallID := seeded[0].call.ID
	if _, err := st.UpdateToolCall(ctx, workspace.ID, seeded[0].call.ID, model.UpdateToolCallInput{
		Status:             "success",
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("complete original approval call: %v", err)
	}
	replacementCall, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-approval-page-replacement-" + uuid.NewString(),
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "medium",
		PolicyDecision:     "require_approval",
		ApprovalID:         seeded[0].approval.ID,
		InputRedactedJSON:  json.RawMessage(`{}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("create replacement approval call: %v", err)
	}
	if replacementCall.ID == originalCallID {
		t.Fatalf("replacement call reused original id %s", originalCallID)
	}
	stableCall, err := st.GetToolCallByApprovalID(ctx, workspace.ID, seeded[0].approval.ID)
	if err != nil {
		t.Fatalf("get stable approval call: %v", err)
	}
	if stableCall.ID != originalCallID {
		t.Fatalf("approval callId drifted to %s, want first linked call %s", stableCall.ID, originalCallID)
	}
	setCreatedAt(ctx, workspace.ID, time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC))

	page, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("list approval page: %v", err)
	}
	if page.Total != 6 || page.Page != 1 || page.PageSize != 2 || len(page.Items) != 2 {
		t.Fatalf("unexpected approval page: %+v", page)
	}
	wantCounts := map[string]int64{
		"pending":  2,
		"approved": 1,
		"rejected": 1,
		"expired":  1,
		"consumed": 1,
	}
	for status, want := range wantCounts {
		if page.StatusCounts[status] != want {
			t.Fatalf("status count %s = %d, want %d; all=%+v", status, page.StatusCounts[status], want, page.StatusCounts)
		}
	}

	wantIDs := make([]string, 0, len(seeded))
	callIDs := make(map[string]string, len(seeded))
	for _, item := range seeded {
		wantIDs = append(wantIDs, item.approval.ID)
		callIDs[item.approval.ID] = item.call.ID
	}
	sort.Sort(sort.Reverse(sort.StringSlice(wantIDs)))
	for index, approval := range page.Items {
		if approval.ID != wantIDs[index] {
			t.Fatalf("approval order[%d] = %s, want %s", index, approval.ID, wantIDs[index])
		}
		if approval.CallID != callIDs[approval.ID] || approval.CallID == "" {
			t.Fatalf("approval %s callId = %q, want %q", approval.ID, approval.CallID, callIDs[approval.ID])
		}
	}

	approved, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{
		Status:   "approved",
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list approved approvals: %v", err)
	}
	if approved.Total != 1 || len(approved.Items) != 1 || approved.Items[0].Status != "approved" {
		t.Fatalf("unexpected approved page: %+v", approved)
	}
	if approved.StatusCounts["pending"] != 2 {
		t.Fatalf("status counts must describe the full workspace, got %+v", approved.StatusCounts)
	}

	defaults, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{})
	if err != nil {
		t.Fatalf("list approvals with defaults: %v", err)
	}
	if defaults.Page != 1 || defaults.PageSize != 50 {
		t.Fatalf("unexpected approval defaults: %+v", defaults)
	}

	bounded, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{PageSize: 1000})
	if err != nil {
		t.Fatalf("list approvals with oversized page: %v", err)
	}
	if bounded.PageSize != 100 {
		t.Fatalf("approval page size must be capped at 100, got %+v", bounded)
	}

	maxInt := int(^uint(0) >> 1)
	if _, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{
		Page:     maxInt,
		PageSize: 100,
	}); err == nil {
		t.Fatal("approval page offset overflow must return an error")
	}
}

func assertApprovalRequestsPageBoundsThousandRows(
	t *testing.T,
	st Store,
	seed func(context.Context, string, int) error,
) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval Bound " + suffix,
		Slug:                  "approval-bound-" + suffix,
		ZitadelOrganizationID: "org-approval-bound-" + suffix,
	})
	if err != nil {
		t.Fatalf("create bounded approval workspace: %v", err)
	}
	if err := seed(ctx, workspace.ID, boundedApprovalTestRows); err != nil {
		t.Fatalf("seed bounded approvals: %v", err)
	}

	page, err := st.ListApprovalRequestsPage(ctx, workspace.ID, model.ApprovalRequestQuery{
		Page:     1,
		PageSize: boundedApprovalTestRows,
	})
	if err != nil {
		t.Fatalf("list bounded approval page: %v", err)
	}
	if page.Total != boundedApprovalTestRows || page.Page != 1 || page.PageSize != 100 || len(page.Items) != 100 {
		t.Fatalf("approval query must remain bounded at 100 items: %+v", page)
	}
	if page.StatusCounts["pending"] != boundedApprovalTestRows {
		t.Fatalf("pending status count = %d, want %d", page.StatusCounts["pending"], boundedApprovalTestRows)
	}
}

func seedSQLiteApprovalRequests(ctx context.Context, st *SQLiteStore, workspaceID string, count int) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO approval_requests (
			id, workspace_id, tool_key, tool_display_name, status, requested_by, expires_at, created_at, updated_at
		)
		VALUES (?, ?, 'mock.bulk', 'Bulk Approval', 'pending', 'bulk-requester', ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	suffix := uuid.NewString()
	base := time.Date(2026, time.August, 15, 9, 0, 0, 0, time.UTC)
	expiresAt := time.Now().UTC().Add(time.Hour)
	for index := 0; index < count; index++ {
		createdAt := base.Add(time.Duration(index) * time.Nanosecond)
		if _, err := statement.ExecContext(
			ctx,
			fmt.Sprintf("approval_bulk_%s_%04d", suffix, index),
			workspaceID,
			sqliteTimestamp(expiresAt),
			sqliteTimestamp(createdAt),
			sqliteTimestamp(createdAt),
		); err != nil {
			_ = statement.Close()
			return err
		}
	}
	if err := statement.Close(); err != nil {
		return err
	}
	return tx.Commit()
}

func seedPostgresApprovalRequests(ctx context.Context, st *PostgresStore, workspaceID string, count int) error {
	suffix := uuid.NewString()
	base := time.Date(2026, time.August, 15, 9, 0, 0, 0, time.UTC)
	expiresAt := time.Now().UTC().Add(time.Hour)
	rows := make([][]any, 0, count)
	for index := 0; index < count; index++ {
		createdAt := base.Add(time.Duration(index) * time.Nanosecond)
		rows = append(rows, []any{
			fmt.Sprintf("approval_bulk_%s_%04d", suffix, index),
			workspaceID,
			"mock.bulk",
			"Bulk Approval",
			"pending",
			"bulk-requester",
			expiresAt,
			createdAt,
			createdAt,
		})
	}
	_, err := st.pool.CopyFrom(
		ctx,
		pgx.Identifier{"approval_requests"},
		[]string{"id", "workspace_id", "tool_key", "tool_display_name", "status", "requested_by", "expires_at", "created_at", "updated_at"},
		pgx.CopyFromRows(rows),
	)
	return err
}

func assertApprovalTTLMutationContract(
	t *testing.T,
	st Store,
	forceExpired func(context.Context, string, string) error,
) {
	t.Helper()

	ctx := context.Background()
	suffix := uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval TTL " + suffix,
		Slug:                  "approval-ttl-" + suffix,
		ZitadelOrganizationID: "org-approval-ttl-" + suffix,
	})
	if err != nil {
		t.Fatalf("create ttl workspace: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(model.ApprovalRequest) error
	}{
		{
			name: "transition",
			mutate: func(approval model.ApprovalRequest) error {
				_, err := st.TransitionApprovalRequest(ctx, workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
					Status:     "approved",
					ReviewedBy: "reviewer",
				})
				return err
			},
		},
		{
			name: "update",
			mutate: func(approval model.ApprovalRequest) error {
				_, err := st.UpdateApprovalRequest(ctx, workspace.ID, approval.ID, model.UpdateApprovalRequestInput{
					Status:     "approved",
					ReviewedBy: "reviewer",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			approval, err := st.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
				WorkspaceID:     workspace.ID,
				ToolKey:         "mock.ttl",
				ToolDisplayName: "TTL Approval",
				RequestedBy:     "requester",
				TTL:             time.Hour,
			})
			if err != nil {
				t.Fatalf("create approval: %v", err)
			}
			if err := forceExpired(ctx, workspace.ID, approval.ID); err != nil {
				t.Fatalf("force approval expiry: %v", err)
			}
			if err := test.mutate(approval); !errors.Is(err, ErrExpired) {
				t.Fatalf("expired %s error = %v, want ErrExpired", test.name, err)
			}
			current, err := st.GetApprovalRequestByID(ctx, workspace.ID, approval.ID)
			if err != nil {
				t.Fatalf("get expired approval: %v", err)
			}
			if current.Status != "expired" {
				t.Fatalf("expired %s persisted status = %q, want expired", test.name, current.Status)
			}
		})
	}
}

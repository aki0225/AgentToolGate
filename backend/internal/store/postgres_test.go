package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"

	"github.com/google/uuid"
)

func TestPostgresStoreConcurrentSchemaInitialization(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const workers = 8
	start := make(chan struct{})
	errCh := make(chan error, workers)
	storeCh := make(chan *PostgresStore, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			st, err := NewPostgresStore(ctx, dsn)
			if err != nil {
				errCh <- fmt.Errorf("worker %d: %w", worker, err)
				return
			}
			storeCh <- st
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)
	close(storeCh)

	for st := range storeCh {
		st.Close()
	}
	for err := range errCh {
		if err != nil {
			t.Errorf("new postgres store: %v", err)
		}
	}
}

func TestPostgresStoreCreateToolCallKeepsEmptyApprovalID(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Test Workspace " + uuid.NewString(),
		Slug:                  "test-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	tool, err := st.CreateTool(ctx, model.CreateToolInput{
		WorkspaceID:      workspace.ID,
		Namespace:        "mock",
		Name:             "echo",
		DisplayName:      "Mock Echo",
		OperationType:    "mock",
		RiskLevel:        "low",
		RequiresApproval: false,
		InputSchemaJSON:  json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON: json.RawMessage(`{"type":"object"}`),
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("create tool: %v", err)
	}

	call, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "",
		ActorID:            "actor_1",
		ActorSubject:       "subject_1",
		ActorEmail:         "dev@example.com",
		ActorName:          "Dev User",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "success",
		RiskLevel:          "low",
		PolicyDecision:     "allow",
		ApprovalID:         "",
		DurationMs:         1,
		InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
		OutputRedactedJSON: json.RawMessage(`{"echo":{"message":"hello"}}`),
		ErrorMessage:       "",
		TraceID:            "trace_test",
	})
	if err != nil {
		t.Fatalf("create tool call: %v", err)
	}

	if call.ApprovalID != "" {
		t.Fatalf("expected empty approval id, got %q", call.ApprovalID)
	}
}

func TestPostgresUpdateToolCallClearsExecutionInput(t *testing.T) {
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

	assertUpdateToolCallClearsExecutionInput(t, st)
}

func TestPostgresToolCallExplanationPersists(t *testing.T) {
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

	assertToolCallExplanationPersists(t, st)
}

func TestPostgresStoreBootstrapRegistersDatabaseQuery(t *testing.T) {
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

	if err := st.Bootstrap(ctx, model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	workspaces, err := st.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) == 0 {
		t.Fatalf("expected bootstrap workspace")
	}

	tool, err := st.GetToolByKey(ctx, workspaces[0].ID, "database.query")
	if err != nil {
		t.Fatalf("get database.query: %v", err)
	}
	if tool.OperationType != "read" || tool.RiskLevel != "medium" || tool.RequiresApproval {
		t.Fatalf("unexpected database.query metadata: %+v", tool)
	}
}

func TestPostgresStoreBootstrapRegistersGitHubTools(t *testing.T) {
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

	orgID := "org-" + uuid.NewString()
	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "GitHub Workspace " + uuid.NewString(),
		Slug:                  "github-" + uuid.NewString(),
		ZitadelOrganizationID: orgID,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	expected := map[string]struct {
		operationType    string
		riskLevel        string
		requiresApproval bool
	}{
		"github.list_repos":       {"read", "low", false},
		"github.get_pull_request": {"read", "medium", false},
		"github.create_issue":     {"create", "medium", true},
	}
	for key, want := range expected {
		tool, err := st.GetToolByKey(ctx, workspace.ID, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if tool.OperationType != want.operationType || tool.RiskLevel != want.riskLevel || tool.RequiresApproval != want.requiresApproval {
			t.Fatalf("unexpected %s metadata: %+v", key, tool)
		}
	}
}

func TestPostgresStoreBootstrapRegistersHTTPRequest(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "HTTP Workspace " + uuid.NewString(),
		Slug:                  "http-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tool, err := st.GetToolByKey(ctx, workspace.ID, "http.request")
	if err != nil {
		t.Fatalf("get http.request: %v", err)
	}
	if tool.OperationType != "read" || tool.RiskLevel != "medium" || tool.RequiresApproval {
		t.Fatalf("unexpected http.request metadata: %+v", tool)
	}
}

func TestPostgresStoreBootstrapRegistersConnectors(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Connector Workspace " + uuid.NewString(),
		Slug:                  "connector-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{
		Connectors: []model.BootstrapConnectorInput{
			{
				Type:        "database",
				Name:        "local_postgres",
				DisplayName: "Local PostgreSQL",
				ConfigJSON:  json.RawMessage(`{"datasource":"local_postgres"}`),
				Enabled:     true,
			},
			{
				Type:        "github",
				Name:        "default",
				DisplayName: "GitHub Default",
				ConfigJSON:  json.RawMessage(`{"apiBaseURL":"https://api.github.com","allowedRepos":["acme/demo"]}`),
				Enabled:     true,
			},
			{
				Type:        "http",
				Name:        "default",
				DisplayName: "HTTP Default",
				ConfigJSON:  json.RawMessage(`{"allowedHosts":["localhost:18080"],"allowedMethods":["GET"]}`),
				Enabled:     true,
			},
		},
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	connectors, err := st.ListConnectors(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	if len(connectors) != 3 {
		t.Fatalf("expected 3 connectors, got %d", len(connectors))
	}
}

func TestPostgresSecretCRUDAndValidation(t *testing.T) {
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

	assertSecretCRUDAndValidation(t, st)
}

func TestPostgresStoreConnectorCRUDLifecycle(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Connector CRUD Workspace " + uuid.NewString(),
		Slug:                  "connector-crud-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	connector, err := st.CreateConnector(ctx, model.CreateConnectorInput{
		WorkspaceID: workspace.ID,
		Type:        "http",
		Name:        "demo",
		DisplayName: "HTTP Demo",
		ConfigJSON:  json.RawMessage(`{"allowedHosts":["localhost:18080"],"allowedMethods":["GET"]}`),
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("create connector: %v", err)
	}

	fetched, err := st.GetConnectorByID(ctx, workspace.ID, connector.ID)
	if err != nil {
		t.Fatalf("get connector: %v", err)
	}
	if fetched.Type != "http" || fetched.Name != "demo" || fetched.DisplayName != "HTTP Demo" || !fetched.Enabled {
		t.Fatalf("unexpected fetched connector: %+v", fetched)
	}

	updated, err := st.UpdateConnector(ctx, workspace.ID, connector.ID, model.UpdateConnectorInput{
		DisplayName: "HTTP Demo Updated",
		ConfigJSON:  json.RawMessage(`{"allowedHosts":["localhost:18080"],"allowedMethods":["GET","POST"]}`),
		Enabled:     boolPtr(false),
	})
	if err != nil {
		t.Fatalf("update connector: %v", err)
	}
	if updated.DisplayName != "HTTP Demo Updated" || updated.Enabled {
		t.Fatalf("unexpected updated connector: %+v", updated)
	}

	connectors, err := st.ListConnectors(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list connectors: %v", err)
	}
	if len(connectors) != 1 {
		t.Fatalf("expected 1 connector, got %d", len(connectors))
	}
	if connectors[0].DisplayName != "HTTP Demo Updated" {
		t.Fatalf("unexpected listed connector: %+v", connectors[0])
	}
}

func TestPostgresStoreApprovalTransitionIsCompareAndSwap(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Approval Workspace " + uuid.NewString(),
		Slug:                  "approval-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	approval, err := st.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
		WorkspaceID:     workspace.ID,
		ToolKey:         "mock.write",
		ToolDisplayName: "Mock Write",
		RequestedBy:     "requester",
		Reason:          "write operation requires approval",
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	updated, err := st.TransitionApprovalRequest(ctx, workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	})
	if err != nil {
		t.Fatalf("first transition should succeed: %v", err)
	}
	if updated.Status != "approved" || updated.ReviewedBy != "owner" {
		t.Fatalf("unexpected updated approval: %+v", updated)
	}

	_, err = st.TransitionApprovalRequest(ctx, workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner-2",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second transition should conflict, got %v", err)
	}
}

func TestPostgresStoreApprovalRequestMetadataPersists(t *testing.T) {
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

	assertApprovalRequestMetadataPersists(t, st)
}

func TestPostgresStoreApprovalRequestFingerprintAllowsNewTicketAfterInactive(t *testing.T) {
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

	assertApprovalRequestFingerprintAllowsNewTicketAfterInactive(t, st)
}

func TestPostgresStoreApprovalRequestExpiresAndBlocksTransition(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Expired Approval Workspace " + uuid.NewString(),
		Slug:                  "expired-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	approval, err := st.CreateApprovalRequest(ctx, model.CreateApprovalRequestInput{
		WorkspaceID:     workspace.ID,
		ToolKey:         "mock.write",
		ToolDisplayName: "Mock Write",
		RequestedBy:     "requester",
		Reason:          "write operation requires approval",
		TTL:             -time.Minute,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}
	if !approval.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatalf("expected expired approval timestamp, got %+v", approval)
	}

	items, err := st.ListApprovalRequests(ctx, workspace.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(items) != 1 || items[0].Status != "expired" {
		t.Fatalf("expected expired approval in list, got %+v", items)
	}

	got, err := st.GetApprovalRequestByID(ctx, workspace.ID, approval.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != "expired" {
		t.Fatalf("expected expired approval status, got %+v", got)
	}

	_, err = st.TransitionApprovalRequest(ctx, workspace.ID, approval.ID, "pending", model.UpdateApprovalRequestInput{
		Status:     "approved",
		ReviewedBy: "owner",
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected expired transition error, got %v", err)
	}
}

func TestPostgresStoreListToolCallsPageFiltersByTimeRange(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Range Workspace " + uuid.NewString(),
		Slug:                  "range-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tool, err := st.GetToolByKey(ctx, workspace.ID, "mock.echo")
	if err != nil {
		t.Fatalf("get mock.echo: %v", err)
	}

	call1, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-range-1-" + uuid.NewString(),
		ActorSubject:       "subject",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "success",
		RiskLevel:          "low",
		PolicyDecision:     "allow",
		DurationMs:         1,
		InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
		TraceID:            "trace-range-1",
	})
	if err != nil {
		t.Fatalf("create tool call 1: %v", err)
	}
	call2, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "req-range-2-" + uuid.NewString(),
		ActorSubject:       "subject",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "success",
		RiskLevel:          "low",
		PolicyDecision:     "allow",
		DurationMs:         1,
		InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
		InputExecutionJSON: json.RawMessage(`{}`),
		OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
		TraceID:            "trace-range-2",
	})
	if err != nil {
		t.Fatalf("create tool call 2: %v", err)
	}

	oldTime := time.Date(2026, time.January, 2, 8, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	if _, err := st.pool.Exec(ctx, `UPDATE tool_calls SET created_at = $2 WHERE id = $1`, call1.ID, oldTime); err != nil {
		t.Fatalf("update tool call 1 time: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE tool_calls SET created_at = $2 WHERE id = $1`, call2.ID, newTime); err != nil {
		t.Fatalf("update tool call 2 time: %v", err)
	}

	from := time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.January, 3, 23, 59, 59, 0, time.UTC)
	page, err := st.ListToolCallsPage(ctx, workspace.ID, model.ToolCallQuery{
		Tool:     "mock.echo",
		From:     &from,
		To:       &to,
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list tool calls page: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected 1 filtered call, got page=%+v", page)
	}
	if page.Items[0].ID != call2.ID {
		t.Fatalf("expected the newer call to match time range, got %+v", page.Items[0])
	}
}

func TestPostgresStoreCreateToolCallIsIdempotentByRequestID(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Idempotent Workspace " + uuid.NewString(),
		Slug:                  "idem-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tool, err := st.GetToolByKey(ctx, workspace.ID, "mock.echo")
	if err != nil {
		t.Fatalf("get mock.echo: %v", err)
	}

	requestID := "req-idem-" + uuid.NewString()
	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
				WorkspaceID:        workspace.ID,
				RequestID:          requestID,
				ActorSubject:       "subject",
				ToolID:             tool.ID,
				ToolKey:            tool.Key(),
				Status:             "success",
				RiskLevel:          "low",
				PolicyDecision:     "allow",
				DurationMs:         1,
				InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
				InputExecutionJSON: json.RawMessage(`{}`),
				OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
				TraceID:            "trace-idem",
			})
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)

	successCount := 0
	conflictCount := 0
	for err := range errCh {
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, ErrConflict):
			conflictCount++
		default:
			t.Fatalf("unexpected create tool call error: %v", err)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("expected one success and one conflict, got success=%d conflict=%d", successCount, conflictCount)
	}

	var count int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM tool_calls WHERE request_id = $1`, requestID).Scan(&count); err != nil {
		t.Fatalf("count tool calls: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one tool call row, got %d", count)
	}
}

func TestPostgresUpdateToolCanToggleEnabled(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Tool Workspace " + uuid.NewString(),
		Slug:                  "tool-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tool, err := st.GetToolByKey(ctx, workspace.ID, "mock.echo")
	if err != nil {
		t.Fatalf("get tool: %v", err)
	}

	updated, err := st.UpdateTool(ctx, workspace.ID, tool.ID, model.UpdateToolInput{Enabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("update tool: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("expected tool to be disabled, got %+v", updated)
	}
}

func TestPostgresUpdateToolMetadataPreservesDisabledState(t *testing.T) {
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

	assertUpdateToolMetadataPreservesDisabledState(t, st)
}

func TestPostgresPolicyRuleCRUDAndOrdering(t *testing.T) {
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

	assertPolicyRuleCRUDAndOrdering(t, st)
}

func TestPostgresStoreListToolCallsPageAppliesPaginationAndFilters(t *testing.T) {
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

	workspace, err := st.CreateWorkspace(ctx, model.CreateWorkspaceInput{
		Name:                  "Pagination Workspace " + uuid.NewString(),
		Slug:                  "pagination-" + uuid.NewString(),
		ZitadelOrganizationID: "org-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := st.Bootstrap(ctx, model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	tool, err := st.GetToolByKey(ctx, workspace.ID, "mock.echo")
	if err != nil {
		t.Fatalf("get mock.echo: %v", err)
	}
	for i := 0; i < 15; i++ {
		if _, err := st.CreateToolCall(ctx, model.CreateToolCallInput{
			WorkspaceID:        workspace.ID,
			RequestID:          "req-page-" + uuid.NewString(),
			ActorSubject:       "subject",
			ToolID:             tool.ID,
			ToolKey:            tool.Key(),
			Status:             "success",
			RiskLevel:          "low",
			PolicyDecision:     "allow",
			DurationMs:         1,
			InputRedactedJSON:  json.RawMessage(`{"message":"hello"}`),
			InputExecutionJSON: json.RawMessage(`{}`),
			OutputRedactedJSON: json.RawMessage(`{"ok":true}`),
			TraceID:            "trace-page",
		}); err != nil {
			t.Fatalf("create tool call: %v", err)
		}
	}

	page, err := st.ListToolCallsPage(ctx, workspace.ID, model.ToolCallQuery{
		Tool:     "mock.echo",
		Statuses: []string{"success"},
		Page:     2,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("list tool calls page: %v", err)
	}
	if page.Total != 15 || page.Page != 2 || page.PageSize != 10 {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	if len(page.Items) != 5 {
		t.Fatalf("expected 5 calls on page 2, got %d", len(page.Items))
	}
}

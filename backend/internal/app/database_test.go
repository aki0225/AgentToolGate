package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGuardDatabaseSQLAllowsSelectAndForcesLimit(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	guarded, err := guardDatabaseSQL("SELECT generate_series(1, 1000) AS n", 7, allowed)
	if err != nil {
		t.Fatalf("guard select: %v", err)
	}
	if !strings.HasPrefix(guarded.ExecutedSQL, "SELECT * FROM (SELECT generate_series") {
		t.Fatalf("unexpected guarded SQL: %s", guarded.ExecutedSQL)
	}
	if !strings.HasSuffix(guarded.ExecutedSQL, "LIMIT 7") {
		t.Fatalf("expected forced outer limit, got %s", guarded.ExecutedSQL)
	}

	limited, err := guardDatabaseSQL("SELECT generate_series(1, 1000) AS n LIMIT 9999", 5, allowed)
	if err != nil {
		t.Fatalf("guard limited select: %v", err)
	}
	if !strings.HasSuffix(limited.ExecutedSQL, "LIMIT 5") {
		t.Fatalf("expected high inner limit to be capped by outer limit, got %s", limited.ExecutedSQL)
	}
}

func TestGuardDatabaseSQLAllowsWhitelistedTables(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	guarded, err := guardDatabaseSQL("SELECT id FROM public.orders", 10, allowed)
	if err != nil {
		t.Fatalf("expected whitelisted table to pass: %v", err)
	}
	if len(guarded.Tables) != 1 || guarded.Tables[0] != "public.orders" {
		t.Fatalf("expected extracted public.orders table, got %+v", guarded.Tables)
	}
}

func TestGuardDatabaseSQLAllowsDirectSensitiveColumnsForOutputMasking(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT id, email, token AS token FROM public.orders", 10, allowed); err != nil {
		t.Fatalf("expected direct sensitive columns to pass so output masking can apply: %v", err)
	}
}

func TestGuardDatabaseSQLRejectsNonWhitelistedTables(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT id FROM public.users", 10, allowed); err == nil {
		t.Fatalf("expected non-whitelisted table to be rejected")
	}
}

func TestGuardDatabaseSQLRejectsJoinNonWhitelistedTables(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT * FROM public.orders o JOIN public.users u ON u.id = o.user_id", 10, allowed); err == nil {
		t.Fatalf("expected join with non-whitelisted table to be rejected")
	}
}

func TestGuardDatabaseSQLRejectsNestedNonWhitelistedTables(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT * FROM public.orders WHERE id IN (SELECT order_id FROM public.payments)", 10, allowed); err == nil {
		t.Fatalf("expected nested non-whitelisted table to be rejected")
	}
}

func TestGuardDatabaseSQLRejectsSubqueryFrom(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT * FROM (SELECT * FROM public.orders) AS nested", 10, allowed); err == nil {
		t.Fatalf("expected FROM subquery to be rejected")
	}
}

func TestGuardDatabaseSQLRejectsSensitiveColumnAliasBypass(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	cases := []string{
		"SELECT email AS contact FROM public.orders",
		"SELECT email contact FROM public.orders",
		"SELECT email || '' AS contact FROM public.orders",
	}
	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := guardDatabaseSQL(input, 10, allowed); err == nil {
				t.Fatalf("expected sensitive alias bypass to be rejected for %q", input)
			}
		})
	}
}

func TestGuardDatabaseSQLRejectsEmptyWhitelist(t *testing.T) {
	t.Parallel()

	if _, err := guardDatabaseSQL("SELECT 1", 10, databaseAllowedTables{}); err == nil {
		t.Fatalf("expected empty whitelist to reject database.query")
	}
}

func TestParseDatabaseAllowedTablesDefaultsPublicSchema(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "Orders", "public.orders")
	if !allowed.contains("public.orders") {
		t.Fatalf("expected unqualified table to normalize to public.orders")
	}
	if len(allowed.keys()) != 1 {
		t.Fatalf("expected duplicate normalized tables to be collapsed, got %+v", allowed.keys())
	}
}

func TestGuardDatabaseSQLRejectsDMLAndDDL(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.users")
	cases := []string{
		"INSERT INTO users(id) VALUES (1)",
		"UPDATE users SET name = 'x'",
		"DELETE FROM users",
		"DROP TABLE users",
		"ALTER TABLE users ADD COLUMN x text",
		"CREATE TABLE demo(id int)",
		"TRUNCATE TABLE users",
		"SELECT * INTO new_table FROM users",
	}
	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := guardDatabaseSQL(input, 10, allowed); err == nil {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestGuardDatabaseSQLRejectsMultipleStatements(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	if _, err := guardDatabaseSQL("SELECT 1; SELECT 2", 10, allowed); err == nil {
		t.Fatalf("expected multiple statements to be rejected")
	}
	if _, err := guardDatabaseSQL("SELECT 1;", 10, allowed); err != nil {
		t.Fatalf("expected single trailing semicolon to be accepted: %v", err)
	}
}

func TestGuardDatabaseSQLRejectsSideEffectAndServerAccessFunctions(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	cases := []string{
		"SELECT nextval('demo_seq')",
		"SELECT set_config('application_name', 'agent', false)",
		"SELECT pg_notify('agenttoolgate', 'demo')",
		"SELECT pg_sleep(10)",
		"SELECT lo_import('/tmp/demo')",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT pg_terminate_backend(12345)",
	}
	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := guardDatabaseSQL(input, 10, allowed); err == nil {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestGuardDatabaseSQLRejectsRowLockingClauses(t *testing.T) {
	t.Parallel()

	allowed := mustAllowedTables(t, "public.orders")
	cases := []string{
		"SELECT * FROM public.orders FOR SHARE",
		"SELECT * FROM public.orders FOR KEY SHARE",
	}
	for _, input := range cases {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := guardDatabaseSQL(input, 10, allowed); err == nil {
				t.Fatalf("expected %q to be rejected", input)
			}
		})
	}
}

func TestEffectiveDatabaseQueryDSNFallsBackToDatabaseURL(t *testing.T) {
	t.Parallel()

	if got := effectiveDatabaseQueryDSN(" postgres://query ", "postgres://main"); got != "postgres://query" {
		t.Fatalf("expected DATABASE_QUERY_URL to win, got %q", got)
	}
	if got := effectiveDatabaseQueryDSN("", " postgres://main "); got != "postgres://main" {
		t.Fatalf("expected fallback to DATABASE_URL, got %q", got)
	}
}

func TestMemoryBootstrapRegistersDatabaseQuery(t *testing.T) {
	t.Parallel()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}

	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	tool, err := st.GetToolByKey(context.Background(), workspaces[0].ID, "database.query")
	if err != nil {
		t.Fatalf("get database.query: %v", err)
	}
	if tool.OperationType != "read" || tool.RiskLevel != "medium" || tool.RequiresApproval {
		t.Fatalf("unexpected database.query metadata: %+v", tool)
	}
}

func TestDatabaseQueryCallWritesFailedToolCallWhenWhitelistMissing(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT 1 AS demo"}}`)
	if callResp.Code != http.StatusBadRequest {
		t.Fatalf("expected missing whitelist to return 400 after audit, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audited call, got %d", len(calls))
	}
	if calls[0].ToolKey != "database.query" || calls[0].Status != "failed" || calls[0].PolicyDecision != "allow" {
		t.Fatalf("unexpected audited call: %+v", calls[0])
	}
	if !strings.Contains(calls[0].ErrorMessage, "whitelist") {
		t.Fatalf("expected whitelist error in audit, got %+v", calls[0])
	}
}

func TestDatabaseQueryCallRejectsNonWhitelistedTableWithAudit(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newDatabaseQueryTestApp(t, "", "", []string{"public.orders"}, 2)
	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT * FROM public.users"}}`)
	if callResp.Code != http.StatusBadRequest {
		t.Fatalf("expected non-whitelisted table to return 400 after audit, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audited call, got %d", len(calls))
	}
	if calls[0].Status != "failed" || !strings.Contains(calls[0].ErrorMessage, "not allowed") {
		t.Fatalf("expected failed audit with table error, got %+v", calls[0])
	}
}

func TestDatabaseQueryFailedAuditRedactsSQLLiterals(t *testing.T) {
	t.Parallel()

	const emailLiteral = "alice@example.com"
	const tokenLiteral = "literal-token-secret"
	srv, st, workspace := newDatabaseQueryTestApp(t, "", "", []string{"public.orders"}, 2)
	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT id FROM public.orders WHERE email = '`+emailLiteral+`' AND token = '`+tokenLiteral+`'"}}`)
	if callResp.Code != http.StatusInternalServerError {
		t.Fatalf("expected missing datasource to return 500 after audit, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 audited call, got %d", len(calls))
	}
	if string(calls[0].InputExecutionJSON) != "{}" {
		t.Fatalf("direct database.query must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
	}
	input := string(calls[0].InputRedactedJSON)
	for _, leaked := range []string{emailLiteral, tokenLiteral} {
		if strings.Contains(input, leaked) {
			t.Fatalf("database.query audit input leaked SQL literal %q: %s", leaked, input)
		}
	}
}

func TestDatabaseQueryRejectedDollarQuotedSQLRedactsAuditInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		sql    string
		secret string
	}{
		{
			name:   "untagged",
			sql:    "SELECT $$dollar-secret$$ AS demo",
			secret: "dollar-secret",
		},
		{
			name:   "tagged",
			sql:    "SELECT $tag$tagged-secret$tag$ AS demo",
			secret: "tagged-secret",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv, st, workspace := newDatabaseQueryTestApp(t, "", "", []string{"public.orders"}, 2)
			rawBody, err := json.Marshal(map[string]any{
				"tool": "database.query",
				"arguments": map[string]any{
					"datasource": "local_postgres",
					"sql":        tc.sql,
				},
			})
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			callResp := postJSON(t, srv, "/api/tool-calls", string(rawBody))
			if callResp.Code != http.StatusBadRequest {
				t.Fatalf("expected dollar-quoted SQL to remain rejected, got %d body=%s", callResp.Code, callResp.Body.String())
			}

			calls, err := st.ListToolCalls(context.Background(), workspace.ID)
			if err != nil {
				t.Fatalf("list calls: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("expected 1 audited call, got %d", len(calls))
			}
			if strings.Contains(string(calls[0].InputRedactedJSON), tc.secret) {
				t.Fatalf("database.query audit input leaked dollar-quoted literal %q: %s", tc.secret, calls[0].InputRedactedJSON)
			}
			if string(calls[0].InputExecutionJSON) != "{}" {
				t.Fatalf("rejected database.query must not persist raw execution input, got %s", calls[0].InputExecutionJSON)
			}
		})
	}
}

func TestDatabaseQueryAuditSQLNormalizerMasksLiterals(t *testing.T) {
	t.Parallel()

	got := redactDatabaseSQLLiterals("SELECT id FROM public.orders WHERE email = 'alice@example.com' AND phone = '+15551234567' AND id = 42")
	for _, leaked := range []string{"alice@example.com", "+15551234567", "42"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("normalized audit SQL leaked literal %q: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "?") {
		t.Fatalf("expected normalized audit SQL placeholders, got %s", got)
	}

	for _, input := range []string{"SELECT $$secret$$ AS demo", "SELECT $tag$secret$tag$ AS demo"} {
		if got := redactDatabaseSQLLiterals(input); strings.Contains(got, "secret") {
			t.Fatalf("normalized audit SQL leaked dollar-quoted literal for %q: %s", input, got)
		}
	}
}

func TestDatabaseQueryWithPostgresWritesRedactedOutput(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	tableName := prepareDatabaseQueryFixture(t, dsn)
	allowedTable := "public." + tableName
	srv, st, workspace := newDatabaseQueryTestApp(t, "", dsn, []string{allowedTable}, 2)
	callResp := postJSON(t, srv, "/api/tool-calls", fmt.Sprintf(`{"tool":"database.query","arguments":{"datasource":"local_postgres","sql":"SELECT id, name, email, token FROM %s ORDER BY id"}}`, allowedTable))
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "success" || response.CallID == "" {
		t.Fatalf("unexpected response: %+v", response)
	}

	call, err := st.GetToolCallByID(context.Background(), workspace.ID, response.CallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if call.Status != "success" || call.ErrorMessage != "" {
		t.Fatalf("unexpected stored call: %+v", call)
	}

	var output struct {
		RowCount int              `json:"rowCount"`
		MaxRows  int              `json:"maxRows"`
		Rows     []map[string]any `json:"rows"`
		Tables   []string         `json:"tables"`
	}
	if err := json.Unmarshal(call.OutputRedactedJSON, &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.RowCount != 1 || output.MaxRows != 2 || len(output.Rows) != 1 {
		t.Fatalf("expected query output to honor max rows, got %+v", output)
	}
	if output.Rows[0]["email"] != "[REDACTED]" || output.Rows[0]["token"] != "[REDACTED]" {
		t.Fatalf("expected sensitive fields to be redacted in audit output, got %+v", output.Rows[0])
	}
	if output.Rows[0]["name"] != "Alice" {
		t.Fatalf("expected non-sensitive field to stay visible, got %+v", output.Rows[0])
	}
	if len(output.Tables) != 1 || output.Tables[0] != allowedTable {
		t.Fatalf("expected audited table list, got %+v", output.Tables)
	}
}

func TestDatabaseSchemaIntrospectionReturnsWhitelistedTable(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	tableName := prepareDatabaseQueryFixture(t, dsn)
	allowedTable := "public." + tableName
	srv, _, _ := newDatabaseQueryTestApp(t, dsn, "", []string{allowedTable}, 2)

	resp := getJSON(t, srv, "/api/database/schema?datasource=local_postgres")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var schema databaseSchemaResponse
	decodeBody(t, resp.Body.Bytes(), &schema)
	if schema.Datasource != "local_postgres" || len(schema.Tables) != 1 {
		t.Fatalf("unexpected schema response: %+v", schema)
	}
	if schema.Tables[0].Schema != "public" || schema.Tables[0].Table != tableName {
		t.Fatalf("expected whitelisted table, got %+v", schema.Tables[0])
	}
	foundMaskedEmail := false
	for _, column := range schema.Tables[0].Columns {
		if column.Name == "email" && column.Masked {
			foundMaskedEmail = true
		}
	}
	if !foundMaskedEmail {
		t.Fatalf("expected email column to be marked masked, got %+v", schema.Tables[0].Columns)
	}
}

func newDatabaseQueryTestApp(t *testing.T, databaseQueryURL string, databaseURL string, allowedTables []string, maxRows int) (*App, store.Store, model.Workspace) {
	t.Helper()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}

	cfg := config.Config{
		AuthMode:                   "local",
		DefaultWorkspaceOrgID:      "local-org",
		LocalSubject:               "local-dev",
		LocalEmail:                 "dev@agenttoolgate.local",
		LocalName:                  "Local Developer",
		LocalRole:                  "owner",
		CORSAllowedOrigins:         []string{"*"},
		DatabaseURL:                databaseURL,
		DatabaseQueryURL:           databaseQueryURL,
		DatabaseQueryDatasource:    "local_postgres",
		DatabaseQueryTimeoutMs:     3000,
		DatabaseQueryMaxRows:       maxRows,
		DatabaseQueryAllowedTables: allowedTables,
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, st, workspaces[0]
}

func mustAllowedTables(t *testing.T, tables ...string) databaseAllowedTables {
	t.Helper()

	allowed, err := parseDatabaseAllowedTables(tables)
	if err != nil {
		t.Fatalf("parse allowed tables: %v", err)
	}
	return allowed
}

func prepareDatabaseQueryFixture(t *testing.T, dsn string) string {
	t.Helper()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect fixture database: %v", err)
	}
	tableName := "agt_week4_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	createSQL := fmt.Sprintf(`
		CREATE TABLE public.%s (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			token TEXT NOT NULL
		)
	`, tableName)
	if _, err := pool.Exec(ctx, createSQL); err != nil {
		pool.Close()
		t.Fatalf("create fixture table: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO public.%s (id, name, email, token) VALUES (1, 'Alice', 'alice@example.com', 'secret-token')`, tableName)); err != nil {
		pool.Close()
		t.Fatalf("insert fixture row: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS public.%s`, tableName))
		pool.Close()
	})
	return tableName
}

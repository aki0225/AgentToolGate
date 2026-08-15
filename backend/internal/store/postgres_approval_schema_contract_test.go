package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresApprovalPageQueryMatchesVersionedIndexes(t *testing.T) {
	t.Parallel()

	if got := normalizePostgresContractSQL(postgresApprovalPageWorkspaceFilter); got != "workspace_id = $1" {
		t.Fatalf("workspace filter = %q", got)
	}
	if got := normalizePostgresContractSQL(postgresApprovalPageStatusFilter); got != "status = $2" {
		t.Fatalf("status filter = %q", got)
	}
	if got := normalizePostgresContractSQL(postgresApprovalPageOrder); got != "created_at desc, id desc" {
		t.Fatalf("page order = %q", got)
	}

	assertPostgresIndexContract(
		t,
		postgresApprovalDefaultPageIndexName,
		postgresApprovalDefaultPageIndexSQL,
		"on approval_requests (workspace_id, created_at desc, id desc)",
	)
	assertPostgresIndexContract(
		t,
		postgresApprovalStatusPageIndexName,
		postgresApprovalStatusPageIndexSQL,
		"on approval_requests (workspace_id, status, created_at desc, id desc)",
	)
}

func TestPostgresApprovalMigrationMatchesRuntimeIndexes(t *testing.T) {
	t.Parallel()

	migrationPath := filepath.Join("..", "..", "migrations", "001_init.sql")
	migrationBytes, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read postgres migration: %v", err)
	}
	migrationSQL := normalizePostgresContractSQL(string(migrationBytes))

	for _, contract := range []struct {
		name    string
		columns string
	}{
		{
			name:    postgresApprovalDefaultPageIndexName,
			columns: "(workspace_id, created_at desc, id desc)",
		},
		{
			name:    postgresApprovalStatusPageIndexName,
			columns: "(workspace_id, status, created_at desc, id desc)",
		},
		{
			name:    postgresApprovalCallIndexName,
			columns: "(workspace_id, approval_id, created_at, id) where approval_id <> ''",
		},
	} {
		want := "create index if not exists " + strings.ToLower(contract.name) + " on "
		if contract.name == postgresApprovalCallIndexName {
			want += "tool_calls " + contract.columns
		} else {
			want += "approval_requests " + contract.columns
		}
		if !strings.Contains(migrationSQL, want) {
			t.Fatalf("migration missing index contract %q", want)
		}
	}
}

func TestPostgresApprovalCallIDBackfillOnlyJoinsEmptyApprovals(t *testing.T) {
	t.Parallel()

	assertPostgresIndexContract(
		t,
		postgresApprovalCallIndexName,
		postgresApprovalCallIndexSQL,
		"on tool_calls (workspace_id, approval_id, created_at, id) where approval_id <> ''",
	)

	sql := normalizePostgresContractSQL(postgresApprovalCallIDBackfillSQL)
	for _, want := range []string{
		"from tool_calls as linked_call join approval_requests as empty_approval",
		"empty_approval.workspace_id = linked_call.workspace_id",
		"empty_approval.id = linked_call.approval_id",
		"empty_approval.call_id = ''",
		"where linked_call.approval_id <> ''",
		"order by linked_call.workspace_id, linked_call.approval_id, linked_call.created_at, linked_call.id",
		"update approval_requests as approval set call_id = first_call.id",
		"where approval.call_id = ''",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("approval call_id backfill missing contract %q", want)
		}
	}
}

func assertPostgresIndexContract(t *testing.T, name, statement, columns string) {
	t.Helper()

	if !strings.HasSuffix(name, "_v1_idx") {
		t.Fatalf("postgres index %q must use a versioned suffix", name)
	}
	sql := normalizePostgresContractSQL(statement)
	if want := "create index concurrently if not exists " + strings.ToLower(name); !strings.Contains(sql, want) {
		t.Fatalf("postgres index %q missing runtime declaration %q", name, want)
	}
	if !strings.Contains(sql, columns) {
		t.Fatalf("postgres index %q missing columns %q", name, columns)
	}
}

func normalizePostgresContractSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

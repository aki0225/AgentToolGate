package store

import (
	"context"
	"path/filepath"
	"testing"

	"agenttoolgate/backend/internal/model"
)

func TestSQLiteStoreBootstrapRegistersBuiltinTools(t *testing.T) {
	t.Parallel()

	st := newTestSQLiteStore(t)
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	workspaces, err := st.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("expected one workspace, got %d", len(workspaces))
	}
	tool, err := st.GetToolByKey(context.Background(), workspaces[0].ID, "mock.echo")
	if err != nil {
		t.Fatalf("get mock.echo: %v", err)
	}
	if tool.Key() != "mock.echo" {
		t.Fatalf("unexpected tool key: %+v", tool)
	}
}

func TestSQLiteStoreUpdateToolMetadataPreservesDisabledState(t *testing.T) {
	t.Parallel()
	assertUpdateToolMetadataPreservesDisabledState(t, newTestSQLiteStore(t))
}

func TestSQLiteStoreApprovalTransitionIsCompareAndSwap(t *testing.T) {
	t.Parallel()
	assertApprovalTransitionIsCompareAndSwap(t, newTestSQLiteStore(t))
}

func TestSQLiteStoreSecretCRUDAndValidation(t *testing.T) {
	t.Parallel()
	assertSecretCRUDAndValidation(t, newTestSQLiteStore(t))
}

func TestSQLiteStorePolicyRuleCRUDAndOrdering(t *testing.T) {
	t.Parallel()
	assertPolicyRuleCRUDAndOrdering(t, newTestSQLiteStore(t))
}

func TestSQLiteStoreToolCallExecutionInputClearsOnUpdate(t *testing.T) {
	t.Parallel()
	assertUpdateToolCallClearsExecutionInput(t, newTestSQLiteStore(t))
}

func newTestSQLiteStore(t *testing.T) Store {
	t.Helper()
	st, err := NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "agenttoolgate.db"))
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

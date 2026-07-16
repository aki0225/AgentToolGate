package auth

import (
	"context"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestResolvePrincipalUsesOIDCRoleFromIdentity(t *testing.T) {
	t.Parallel()

	st := newAuthTestStore(t)
	authenticator := &Authenticator{cfg: config.Config{AuthMode: "oidc"}}

	_, user, err := authenticator.ResolvePrincipal(context.Background(), st, Identity{
		Mode:           "oidc",
		Subject:        "oidc-user",
		Email:          "user@example.com",
		Name:           "OIDC User",
		OrganizationID: "local-org",
		Role:           "admin",
	})
	if err != nil {
		t.Fatalf("resolve principal: %v", err)
	}
	if user.Role != "admin" {
		t.Fatalf("expected OIDC role claim to be stored, got %+v", user)
	}
}

func TestResolvePrincipalDoesNotDefaultOIDCUsersToLocalOwner(t *testing.T) {
	t.Parallel()

	st := newAuthTestStore(t)
	authenticator := &Authenticator{cfg: config.Config{
		AuthMode:   "oidc",
		LocalRole:  "owner",
		LocalEmail: "local@example.com",
	}}

	_, user, err := authenticator.ResolvePrincipal(context.Background(), st, Identity{
		Mode:           "oidc",
		Subject:        "oidc-user",
		Email:          "user@example.com",
		Name:           "OIDC User",
		OrganizationID: "local-org",
	})
	if err != nil {
		t.Fatalf("resolve principal: %v", err)
	}
	if user.Role == "owner" {
		t.Fatalf("OIDC user without role claim must not inherit LocalRole owner: %+v", user)
	}
}

func newAuthTestStore(t *testing.T) store.Store {
	t.Helper()

	st := store.NewMemoryStore()
	if err := st.Bootstrap(context.Background(), model.BootstrapInput{
		WorkspaceName:           "Default Workspace",
		WorkspaceSlug:           "default",
		WorkspaceOrganizationID: "local-org",
	}); err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	return st
}

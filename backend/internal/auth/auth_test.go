package auth

import (
	"context"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestNewAuthenticatorRejectsUnsupportedAuthMode(t *testing.T) {
	t.Parallel()

	_, err := NewAuthenticator(context.Background(), config.Config{AuthMode: "odic"})
	if err == nil || !strings.Contains(err.Error(), "unsupported AUTH_MODE") {
		t.Fatalf("expected unsupported auth mode error, got %v", err)
	}
}

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

func TestResolvePrincipalRejectsOIDCIdentityWithoutWorkspaceClaim(t *testing.T) {
	t.Parallel()

	st := newAuthTestStore(t)
	authenticator := &Authenticator{cfg: config.Config{AuthMode: "oidc"}}

	_, _, err := authenticator.ResolvePrincipal(context.Background(), st, Identity{
		Mode:    "oidc",
		Subject: "oidc-user",
		Email:   "user@example.com",
		Name:    "OIDC User",
	})
	if err == nil || !strings.Contains(err.Error(), "missing the configured workspace organization claim") {
		t.Fatalf("expected missing OIDC workspace claim error, got %v", err)
	}
}

func TestResolvePrincipalRejectsUnknownOIDCWorkspace(t *testing.T) {
	t.Parallel()

	st := newAuthTestStore(t)
	authenticator := &Authenticator{cfg: config.Config{AuthMode: "oidc"}}

	_, _, err := authenticator.ResolvePrincipal(context.Background(), st, Identity{
		Mode:           "oidc",
		Subject:        "oidc-user",
		Email:          "user@example.com",
		Name:           "OIDC User",
		OrganizationID: "unmapped-org",
	})
	if err == nil || !strings.Contains(err.Error(), "is not mapped to a workspace") {
		t.Fatalf("expected unmapped OIDC workspace error, got %v", err)
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

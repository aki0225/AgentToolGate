package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/coreos/go-oidc/v3/oidc"
)

type Identity struct {
	Mode           string
	Token          string
	Subject        string
	Email          string
	Name           string
	OrganizationID string
	Role           string
}

type Authenticator struct {
	cfg      config.Config
	verifier *oidc.IDTokenVerifier
}

func NewAuthenticator(ctx context.Context, cfg config.Config) (*Authenticator, error) {
	if !cfg.UsesOIDC() {
		return &Authenticator{cfg: cfg}, nil
	}

	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuerURL)
	if err != nil {
		return nil, fmt.Errorf("load oidc provider: %w", err)
	}

	return &Authenticator{
		cfg: cfg,
		verifier: provider.Verifier(&oidc.Config{
			ClientID: cfg.OIDCClientID,
		}),
	}, nil
}

func (a *Authenticator) Authenticate(ctx context.Context, bearerToken, workspaceOrgID string) (Identity, error) {
	if !a.cfg.UsesOIDC() {
		orgID := strings.TrimSpace(workspaceOrgID)
		if orgID == "" {
			orgID = a.cfg.DefaultWorkspaceOrgID
		}
		return Identity{
			Mode:           "local",
			Subject:        a.cfg.LocalSubject,
			Email:          a.cfg.LocalEmail,
			Name:           a.cfg.LocalName,
			OrganizationID: orgID,
		}, nil
	}

	if strings.TrimSpace(bearerToken) == "" {
		return Identity{}, errors.New("missing bearer token")
	}

	token, err := a.verifier.Verify(ctx, bearerToken)
	if err != nil {
		return Identity{}, fmt.Errorf("verify id token: %w", err)
	}

	claims := map[string]any{}
	if err := token.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("decode claims: %w", err)
	}

	identity := Identity{
		Mode:           "oidc",
		Token:          bearerToken,
		Subject:        claimString(claims, a.cfg.OIDCSubjectClaim),
		Email:          claimString(claims, a.cfg.OIDCEmailClaim),
		Name:           claimString(claims, a.cfg.OIDCNameClaim),
		OrganizationID: claimString(claims, a.cfg.OIDCWorkspaceClaim),
		Role:           claimString(claims, a.cfg.OIDCRoleClaim),
	}

	if identity.OrganizationID == "" {
		identity.OrganizationID = strings.TrimSpace(workspaceOrgID)
	}

	if identity.Subject == "" {
		identity.Subject = token.Subject
	}

	return identity, nil
}

func (a *Authenticator) ResolvePrincipal(ctx context.Context, st store.Store, identity Identity) (model.Workspace, model.User, error) {
	workspace, err := a.resolveWorkspace(ctx, st, identity)
	if err != nil {
		return model.Workspace{}, model.User{}, err
	}

	role := strings.TrimSpace(a.cfg.LocalRole)
	if a.cfg.UsesOIDC() {
		role = strings.TrimSpace(identity.Role)
		if role == "" {
			role = "member"
		}
	}
	if role == "" {
		role = "owner"
	}

	user, err := st.UpsertUser(ctx, model.UpsertUserInput{
		WorkspaceID:   workspace.ID,
		ZitadelUserID: identity.Subject,
		Email:         identity.Email,
		Name:          identity.Name,
		Role:          role,
	})
	if err != nil {
		return model.Workspace{}, model.User{}, err
	}

	return workspace, user, nil
}

func (a *Authenticator) resolveWorkspace(ctx context.Context, st store.Store, identity Identity) (model.Workspace, error) {
	if identity.OrganizationID != "" {
		workspace, err := st.GetWorkspaceByOrganizationID(ctx, identity.OrganizationID)
		if err == nil {
			return workspace, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return model.Workspace{}, err
		}
	}

	workspaces, err := st.ListWorkspaces(ctx)
	if err != nil {
		return model.Workspace{}, err
	}
	if len(workspaces) == 0 {
		return model.Workspace{}, store.ErrNotFound
	}
	return workspaces[0], nil
}

func claimString(claims map[string]any, key string) string {
	if value, ok := claims[key]; ok {
		switch typed := value.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		case []any:
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

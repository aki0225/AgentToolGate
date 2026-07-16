package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestToolCallRateLimitWritesAuditAndReturns429(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newRateLimitTestApp(t)
	srv.cfg.RateLimitPerMinute = 1
	for i := 0; i < srv.cfg.RateLimitPerMinute; i++ {
		resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected success for call %d, got %d body=%s", i+1, resp.Code, resp.Body.String())
		}
	}

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", resp.Code, resp.Body.String())
	}

	var body struct {
		Error string `json:"error"`
	}
	decodeBody(t, resp.Body.Bytes(), &body)
	if body.Error != "rate limit exceeded" {
		t.Fatalf("unexpected rate limit error body: %+v", body)
	}

	calls, err := st.ListToolCalls(context.Background(), workspace.ID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	if len(calls) != srv.cfg.RateLimitPerMinute+1 {
		t.Fatalf("expected %d calls, got %d", srv.cfg.RateLimitPerMinute+1, len(calls))
	}
	var rateLimitedCall *model.ToolCall
	for i := range calls {
		if calls[i].Status == "rate_limited" {
			rateLimitedCall = &calls[i]
			break
		}
	}
	if rateLimitedCall == nil {
		t.Fatalf("expected one rate-limited audit row, got %+v", calls)
	}
	if rateLimitedCall.ErrorMessage != "rate limit exceeded" {
		t.Fatalf("unexpected rate-limited audit row: %+v", rateLimitedCall)
	}
	if string(rateLimitedCall.InputExecutionJSON) != "{}" {
		t.Fatalf("rate-limited calls must not persist execution input, got %s", rateLimitedCall.InputExecutionJSON)
	}
}

func TestWorkspaceLimiterExpiresAndRebuilds(t *testing.T) {
	t.Parallel()

	srv, _, workspace := newRateLimitTestApp(t)

	limiter1 := srv.workspaceRateLimiter(workspace.ID, srv.cfg.RateLimitPerMinute)
	value, ok := srv.rateLimiters.Load(workspace.ID)
	if !ok {
		t.Fatalf("expected limiter entry")
	}
	entry, ok := value.(*workspaceRateLimiterEntry)
	if !ok {
		t.Fatalf("unexpected limiter entry type: %T", value)
	}
	entry.lastUsed.Store(time.Now().Add(-2 * time.Second).UTC().UnixNano())

	removed := srv.reapExpiredWorkspaceRateLimiters(time.Now().UTC())
	if removed != 1 {
		t.Fatalf("expected one expired limiter removed, got %d", removed)
	}
	if _, ok := srv.rateLimiters.Load(workspace.ID); ok {
		t.Fatalf("expired limiter should be removed")
	}

	limiter2 := srv.workspaceRateLimiter(workspace.ID, srv.cfg.RateLimitPerMinute)
	if limiter1 == limiter2 {
		t.Fatalf("expected a rebuilt limiter after cleanup")
	}
}

func newRateLimitTestApp(t *testing.T) (*App, store.Store, model.Workspace) {
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
	if len(workspaces) == 0 {
		t.Fatalf("expected bootstrap workspace")
	}

	cfg := config.Config{
		AuthMode:                  "local",
		DefaultWorkspaceOrgID:     "local-org",
		LocalSubject:              "local-dev",
		LocalEmail:                "dev@agenttoolgate.local",
		LocalName:                 "Local Developer",
		LocalRole:                 "owner",
		RateLimitPerMinute:        60,
		RateLimitEvictIntervalSec: 1,
		RateLimitIdleTimeoutSec:   1,
		CORSAllowedOrigins:        []string{"*"},
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	srv := New(cfg, st, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, st, workspaces[0]
}

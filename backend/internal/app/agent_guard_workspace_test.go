package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"agenttoolgate/backend/internal/config"
)

func TestTrustedAgentGuardWorkspaceRootRejectsClientOverride(t *testing.T) {
	trustedRoot := t.TempDir()
	untrustedRoot := t.TempDir()
	app := &App{cfg: config.Config{ProjectRoot: trustedRoot}}

	if got := app.trustedAgentGuardWorkspaceRoot(trustedRoot); !agentGuardPathsEqual(got, trustedRoot) {
		t.Fatalf("trusted root should be accepted, got %q", got)
	}
	if got := app.trustedAgentGuardWorkspaceRoot(untrustedRoot); got != "" {
		t.Fatalf("client root override must be rejected, got %q", got)
	}
}

func TestAgentGuardRelativeTraversalWithoutTrustedRootIsExternal(t *testing.T) {
	for _, target := range []string{"../outside.txt", "workspace/../../outside.txt", `workspace\..\..\outside.txt`} {
		resolution := (&App{}).resolveAgentGuardTargetWithinContext(target, "", "")
		if got := classifyAgentGuardTargetCategoryWithWorkspace(target, "", resolution); got != "external" {
			t.Fatalf("relative traversal %q must be external without trusted root, got %q", target, got)
		}
	}
}

func TestAgentGuardOrdinaryRelativePathWithoutTrustedRootRemainsWorkspace(t *testing.T) {
	target := "src/main.go"
	resolution := (&App{}).resolveAgentGuardTargetWithinContext(target, "", "")
	if got := classifyAgentGuardTargetCategoryWithWorkspace(target, "", resolution); got != "workspace" {
		t.Fatalf("ordinary relative path should remain workspace-compatible, got %q", got)
	}
}

func TestAgentGuardRejectsClientWorkspaceRootOverride(t *testing.T) {
	trustedRoot := t.TempDir()
	externalRoot := t.TempDir()
	target := filepath.Join(externalRoot, "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write external target: %v", err)
	}

	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	body := fmt.Sprintf(`{
		"adapter":"claude",
		"tool":"Write",
		"actionType":"write",
		"target":"outside.txt",
		"workspaceRoot":%q,
		"workingDirectory":%q,
		"isScript":false,
		"contentEncoding":"plain",
		"content":"changed"
	}`, externalRoot, externalRoot)
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision == "allow" || response.Explanation == nil || response.Explanation.TargetCategory != "external" {
		t.Fatalf("client workspaceRoot override must remain governed as external, got %+v", response)
	}
}

func TestAgentGuardRelativeTraversalWithoutConfiguredRootIsNotAllowed(t *testing.T) {
	srv, _, _ := newGovernanceTestApp(t)
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", `{
		"adapter":"claude",
		"tool":"Write",
		"actionType":"write",
		"target":"workspace/../../outside.txt",
		"isScript":false,
		"contentEncoding":"plain",
		"content":"changed"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision == "allow" || response.Explanation == nil || response.Explanation.TargetCategory != "external" {
		t.Fatalf("relative traversal without trusted root must not be allowed, got %+v", response)
	}
}

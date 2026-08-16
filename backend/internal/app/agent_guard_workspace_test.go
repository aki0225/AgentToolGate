package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestTrustedAgentGuardWorkspaceRootRejectsClientOverride(t *testing.T) {
	trustedRoot := t.TempDir()
	untrustedRoot := t.TempDir()
	app := &App{cfg: config.Config{ProjectRoot: trustedRoot}}
	expectedTrustedRoot := canonicalAgentGuardWorkspaceRoot(trustedRoot)

	if got := app.trustedAgentGuardWorkspaceRoot(trustedRoot); got != expectedTrustedRoot {
		t.Fatalf("trusted root should be accepted, got %q", got)
	}
	if got := app.trustedAgentGuardWorkspaceRoot(untrustedRoot); got != "" {
		t.Fatalf("client root override must be rejected, got %q", got)
	}
}

func TestAgentGuardProjectProtectionCannotBeBypassedByClientAllow(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","read":"require_approval","write":"require_approval","reason":"核心算法目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "Read",
		ActionType:       "read",
		Target:           "src/core/algorithm.go",
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "deny_with_ticket" || response.Explanation == nil || response.Explanation.RiskLevel != "high" || response.Explanation.MatchedRule != "project_protected_path" {
		t.Fatalf("project protection must override a client allow, got %+v", response)
	}
}

func TestAgentGuardProjectProtectionChecksAllPatchTargets(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","write":"deny","reason":"核心算法目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "apply_patch",
		ActionType:       "read",
		Target:           "src/ui.go",
		Targets:          []string{"src/ui.go"},
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: src/core/algorithm.go\n*** End Patch\n",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "deny" || response.Explanation == nil || response.Explanation.MatchedRule != "project_protected_path" {
		t.Fatalf("the second protected patch target must deny the request, got %+v", response)
	}
}

func TestAgentGuardProjectProtectionBindsEveryTargetToFingerprint(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","write":"require_approval","reason":"核心算法目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	evaluate := func(extraTarget string) agentGuardEvaluateResponse {
		t.Helper()
		rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
			Adapter:          "codex",
			Tool:             "Write",
			ActionType:       "write",
			Target:           "src/ui.go",
			Targets:          []string{"src/ui.go", extraTarget},
			WorkspaceRoot:    trustedRoot,
			WorkingDirectory: trustedRoot,
			GuardDecision:    "allow",
			GuardRiskLevel:   "low",
			ContentEncoding:  "plain",
			Content:          "same content",
		}))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
		}
		var response agentGuardEvaluateResponse
		decodeBody(t, rec.Body.Bytes(), &response)
		return response
	}

	first := evaluate("src/core/algorithm.go")
	second := evaluate("src/core/optimizer.go")
	if first.Fingerprint == "" || second.Fingerprint == "" || first.Fingerprint == second.Fingerprint {
		t.Fatalf("changing any patch target must change the approval fingerprint, first=%q second=%q", first.Fingerprint, second.Fingerprint)
	}
}

func TestAgentGuardNetworkURLValidationDoesNotDependOnProjectProtection(t *testing.T) {
	disabledRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, disabledRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":false,
			"protectedPaths":[],
			"egress":{"enabled":false}
		}
	}`)
	defaultServer, defaultStore, defaultWorkspace := newGovernanceTestApp(t)
	disabledServer, disabledStore, disabledWorkspace := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: disabledRoot})
	servers := []struct {
		name      string
		srv       *App
		st        store.Store
		workspace model.Workspace
	}{
		{name: "no project root", srv: defaultServer, st: defaultStore, workspace: defaultWorkspace},
		{name: "protection disabled", srv: disabledServer, st: disabledStore, workspace: disabledWorkspace},
	}
	requests := []struct {
		name       string
		target     string
		networkURL string
	}{
		{name: "invalid network url", target: "https://api.github.com/repos/example/project", networkURL: "ftp://uploads.example.test/data"},
		{name: "conflicting network url", target: "https://api.github.com/repos/example/project", networkURL: "https://uploads.example.test/data"},
	}
	for _, server := range servers {
		for _, request := range requests {
			t.Run(server.name+"/"+request.name, func(t *testing.T) {
				rec := postJSON(t, server.srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
					Adapter:         "codex",
					Tool:            "http.request",
					ActionType:      "network",
					Target:          request.target,
					NetworkMethod:   http.MethodPost,
					NetworkURL:      request.networkURL,
					GuardDecision:   "allow",
					GuardRiskLevel:  "low",
					ContentEncoding: "plain",
				}))
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("invalid network declaration must return 400, got %d body=%s", rec.Code, rec.Body.String())
				}
			})
		}
		approvals, err := server.st.ListApprovalRequests(context.Background(), server.workspace.ID)
		if err != nil {
			t.Fatalf("list approvals: %v", err)
		}
		if len(approvals) != 0 {
			t.Fatalf("invalid network declarations must not create approvals, got %+v", approvals)
		}
	}
}

func TestAgentGuardProjectProtectionRejectsTicketWhenAdditionalTargetChanges(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","write":"require_approval","reason":"核心算法目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, st, workspace := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	baseReq := agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "Write",
		ActionType:       "write",
		Target:           "src/ui.go",
		Targets:          []string{"src/ui.go", "src/core/algorithm.go"},
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          "same content",
	}
	first := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, baseReq))
	if first.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", first.Code, first.Body.String())
	}
	var initial agentGuardEvaluateResponse
	decodeBody(t, first.Body.Bytes(), &initial)
	if initial.Decision != "deny_with_ticket" || initial.ApprovalID == "" {
		t.Fatalf("expected pending approval, got %+v", initial)
	}
	approve := postJSON(t, srv, "/api/approvals/"+initial.ApprovalID+"/approve", "")
	if approve.Code != http.StatusOK {
		t.Fatalf("approve ticket: %d body=%s", approve.Code, approve.Body.String())
	}

	retryReq := baseReq
	retryReq.Targets = []string{"src/ui.go", "src/core/optimizer.go"}
	retryReq.TicketID = initial.ApprovalID
	retry := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, retryReq))
	if retry.Code != http.StatusOK {
		t.Fatalf("expected governed retry, got %d body=%s", retry.Code, retry.Body.String())
	}
	var denied agentGuardEvaluateResponse
	decodeBody(t, retry.Body.Bytes(), &denied)
	if denied.Decision != "deny" || !strings.Contains(denied.Reason, "fingerprint mismatch") {
		t.Fatalf("changed additional target must not consume the old ticket, got %+v", denied)
	}
	approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, initial.ApprovalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if approval.Status != "approved" {
		t.Fatalf("fingerprint mismatch must leave the old approval unconsumed, got %+v", approval)
	}
}

func TestAgentGuardProjectProtectionDoesNotTrustReadHintForWriteTool(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","write":"require_approval","reason":"核心算法目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "Write",
		ActionType:       "read",
		Target:           "src/core/algorithm.go",
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          "changed",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "deny_with_ticket" || response.Explanation == nil || response.Explanation.MatchedRule != "project_protected_path" {
		t.Fatalf("known write tool must override a stale read hint, got %+v", response)
	}
}

func TestAgentGuardEvaluatesEveryDirectAPITargetForBuiltInRisk(t *testing.T) {
	trustedRoot := t.TempDir()
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "apply_patch",
		ActionType:       "write",
		Target:           "src/ui.go",
		Targets:          []string{"src/ui.go", ".ssh/id_rsa"},
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: .ssh/id_rsa\n*** End Patch\n",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision == "allow" || response.Explanation == nil || response.Explanation.RiskLevel == "low" {
		t.Fatalf("every direct API target must contribute to Guard Core risk, got %+v", response)
	}
}

func TestAgentGuardProjectProtectionRecognizesShellDelete(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"deploy/production/**","delete":"deny","reason":"生产配置目录"}
			],
			"egress":{"enabled":false}
		}
	}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "PowerShell",
		ActionType:       "exec",
		Target:           "deploy/production/app.yaml",
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          "Remove-Item deploy/production/app.yaml",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision != "deny" || response.Explanation == nil || response.Explanation.MatchedRule != "project_protected_path" {
		t.Fatalf("shell delete must use the configured delete rule, got %+v", response)
	}
}

func TestAgentGuardTreatsProjectProtectionConfigAsSelfTamper(t *testing.T) {
	trustedRoot := t.TempDir()
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:          "codex",
		Tool:             "Write",
		ActionType:       "write",
		Target:           ".agenttoolgate/protected.json",
		WorkspaceRoot:    trustedRoot,
		WorkingDirectory: trustedRoot,
		GuardDecision:    "allow",
		GuardRiskLevel:   "low",
		ContentEncoding:  "plain",
		Content:          `{"version":1}`,
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected governed response, got %d body=%s", rec.Code, rec.Body.String())
	}
	var response agentGuardEvaluateResponse
	decodeBody(t, rec.Body.Bytes(), &response)
	if response.Decision == "allow" || response.Explanation == nil || response.Explanation.TargetCategory != "self_tamper" {
		t.Fatalf("project protection config must be governed as self-tamper, got %+v", response)
	}
}

func TestAgentGuardInvalidProjectProtectionFailsClosed(t *testing.T) {
	trustedRoot := t.TempDir()
	writeAgentGuardProjectProtection(t, trustedRoot, `{"version":1,"localActionFirewall":{"enabled":true,"unknown":true}}`)
	srv, _, _ := newGovernanceTestAppWithConfig(t, config.Config{ProjectRoot: trustedRoot})
	rec := postJSON(t, srv, "/api/agent-guard/evaluate", mustAgentGuardRequestBody(t, agentGuardEvaluateRequest{
		Adapter:         "codex",
		Tool:            "Read",
		ActionType:      "read",
		Target:          "README.md",
		GuardDecision:   "allow",
		GuardRiskLevel:  "low",
		ContentEncoding: "plain",
	}))
	if rec.Code < http.StatusInternalServerError || strings.Contains(rec.Body.String(), `"decision":"allow"`) {
		t.Fatalf("invalid project protection must fail closed, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func writeAgentGuardProjectProtection(t *testing.T, root, body string) {
	t.Helper()
	configDir := filepath.Join(root, ".agenttoolgate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create project protection directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "protected.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
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

package guard

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadProjectProtectionMissingFileKeepsDefaultBehavior(t *testing.T) {
	root := t.TempDir()
	protection, err := LoadProjectProtection(root)
	if err != nil {
		t.Fatalf("load missing project protection: %v", err)
	}
	if protection.Enabled || len(protection.ProtectedPaths) != 0 || protection.Egress.Enabled {
		t.Fatalf("missing config must produce empty protection, got %+v", protection)
	}
}

func TestLoadProjectProtectionRejectsUnsafeDocuments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"unexpected":true}}`,
		},
		{
			name: "duplicate root field",
			body: `{"version":1,"version":1,"localActionFirewall":{"enabled":true}}`,
		},
		{
			name: "duplicate nested field cannot disable protection",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"enabled":false}}`,
		},
		{
			name: "duplicate rule effect cannot weaken protection",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"src/core/**","read":"deny","read":"require_approval"}]}}`,
		},
		{
			name: "parent traversal",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"../outside/**","read":"deny"}]}}`,
		},
		{
			name: "absolute path",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"C:/secrets/**","read":"deny"}]}}`,
		},
		{
			name: "windows absolute path on every platform",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"D:\\secrets\\**","read":"deny"}]}}`,
		},
		{
			name: "UNC path",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"\\\\server\\share\\**","read":"deny"}]}}`,
		},
		{
			name: "mid-pattern wildcard",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"src/*/secret.go","read":"deny"}]}}`,
		},
		{
			name: "wildcard host",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"egress":{"enabled":true,"allowedHosts":["*"],"unlistedWrite":"deny"}}}`,
		},
		{
			name: "allow cannot weaken guard",
			body: `{"version":1,"localActionFirewall":{"enabled":true,"protectedPaths":[{"pattern":"src/**","read":"allow"}]}}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeProjectProtectionTestFile(t, root, tc.body)
			if _, err := LoadProjectProtection(root); err == nil {
				t.Fatalf("expected invalid project protection for %s", tc.name)
			}
		})
	}
}

func TestLoadProjectProtectionRejectsSymlinkedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires optional privileges")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, ".agenttoolgate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(validProjectProtectionJSON()), 0o600); err != nil {
		t.Fatalf("write source config: %v", err)
	}
	if err := os.Symlink(source, filepath.Join(configDir, "protected.json")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := LoadProjectProtection(root); err == nil {
		t.Fatal("symlinked project protection must be rejected")
	}
}

func TestEvaluateWithProjectProtectionGuardsConfiguredReadsAndWrites(t *testing.T) {
	root := t.TempDir()
	writeProjectProtectionTestFile(t, root, validProjectProtectionJSON())
	protection, err := LoadProjectProtection(root)
	if err != nil {
		t.Fatalf("load project protection: %v", err)
	}

	readDecision := EvaluateWithProjectProtection(ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      "src/core/algorithm.go",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if readDecision.Decision != "ask" || readDecision.Category != "project_protected_path" {
		t.Fatalf("protected read must ask, got %+v", readDecision)
	}
	if readDecision.RiskLevel != "high" {
		t.Fatalf("protected read must require a fresh high-risk approval, got %+v", readDecision)
	}

	writeDecision := EvaluateWithProjectProtection(ActionInput{
		ToolName:    "Write",
		ActionType:  "write",
		Target:      "deploy/production/app.yaml",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if writeDecision.Decision != "deny" || writeDecision.Category != "project_protected_path" {
		t.Fatalf("protected production write must deny, got %+v", writeDecision)
	}

	ordinaryDecision := EvaluateWithProjectProtection(ActionInput{
		ToolName:    "Write",
		ActionType:  "write",
		Target:      "src/ui/button.go",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if ordinaryDecision.Decision != "allow" || !ordinaryDecision.Silent {
		t.Fatalf("ordinary workspace write must remain silent allow, got %+v", ordinaryDecision)
	}
}

func TestEvaluateWithProjectProtectionKeepsMostSevereTarget(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "docs/**", Write: "deny"},
		},
	}
	decision := EvaluateWithProjectProtection(ActionInput{
		ToolName:    "apply_patch",
		ActionType:  "write",
		Targets:     []string{".ssh/id_rsa", "docs/readme.md"},
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if decision.Decision != "deny" || decision.RiskLevel != "critical" {
		t.Fatalf("later project deny must not replace an earlier critical deny, got %+v", decision)
	}
}

func TestEvaluateProjectProtectionDoesNotSplitSemicolonFilenames(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "docs/a;b.md", Write: "require_approval"},
		},
	}
	decision, matched := EvaluateProjectProtection(ActionInput{
		ToolName:    "apply_patch",
		ActionType:  "write",
		Target:      "docs/a;b.md",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if !matched || decision.Decision != "ask" {
		t.Fatalf("semicolon must remain part of one filename, got matched=%v decision=%+v", matched, decision)
	}
}

func TestEvaluateWithProjectProtectionChecksEveryPatchTarget(t *testing.T) {
	root := t.TempDir()
	writeProjectProtectionTestFile(t, root, validProjectProtectionJSON())
	protection, err := LoadProjectProtection(root)
	if err != nil {
		t.Fatalf("load project protection: %v", err)
	}
	decision := EvaluateWithProjectProtection(ActionInput{
		ToolName:       "apply_patch",
		ActionType:     "write",
		Target:         "src/ui.go;src/core/algorithm.go",
		Targets:        []string{"src/ui.go", "src/core/algorithm.go"},
		ContentPreview: "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: src/core/algorithm.go\n*** End Patch\n",
		CWD:            root,
		ProjectRoot:    root,
	}, protection)
	if decision.Decision != "ask" || decision.Category != "project_protected_path" {
		t.Fatalf("any protected patch target must ask, got %+v", decision)
	}
}

func TestEvaluateProjectProtectionMergesCanonicalPatchTargets(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "deny"},
		},
	}
	decision, matched := EvaluateProjectProtection(ActionInput{
		ToolName:       "apply_patch",
		ActionType:     "read",
		Target:         "src/ui.go",
		Targets:        []string{"src/ui.go"},
		ContentPreview: "*** Begin Patch\n*** Update File: src/ui.go\n*** Update File: src/core/algorithm.go\n*** End Patch\n",
		CWD:            root,
		ProjectRoot:    root,
	}, protection)
	if !matched || decision.Decision != "deny" {
		t.Fatalf("canonical patch targets must override a stale read hint and incomplete target list, got matched=%v decision=%+v", matched, decision)
	}
}

func TestEvaluateProjectProtectionClassifiesPatchOperations(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name     string
		line     string
		rule     ProtectedPathRule
		matched  bool
		decision string
	}{
		{
			name:     "add uses write",
			line:     "*** Add File: deploy/production/app.yaml",
			rule:     ProtectedPathRule{Pattern: "deploy/production/**", Write: "require_approval"},
			matched:  true,
			decision: "ask",
		},
		{
			name:    "add does not use delete",
			line:    "*** Add File: deploy/production/app.yaml",
			rule:    ProtectedPathRule{Pattern: "deploy/production/**", Delete: "deny"},
			matched: false,
		},
		{
			name:     "update uses write",
			line:     "*** Update File: deploy/production/app.yaml",
			rule:     ProtectedPathRule{Pattern: "deploy/production/**", Write: "require_approval"},
			matched:  true,
			decision: "ask",
		},
		{
			name:    "update does not use delete",
			line:    "*** Update File: deploy/production/app.yaml",
			rule:    ProtectedPathRule{Pattern: "deploy/production/**", Delete: "deny"},
			matched: false,
		},
		{
			name:     "move uses write",
			line:     "*** Move to: deploy/production/app.yaml",
			rule:     ProtectedPathRule{Pattern: "deploy/production/**", Write: "require_approval"},
			matched:  true,
			decision: "ask",
		},
		{
			name:    "move does not use delete",
			line:    "*** Move to: deploy/production/app.yaml",
			rule:    ProtectedPathRule{Pattern: "deploy/production/**", Delete: "deny"},
			matched: false,
		},
		{
			name:     "delete uses delete",
			line:     "*** Delete File: deploy/production/app.yaml",
			rule:     ProtectedPathRule{Pattern: "deploy/production/**", Delete: "deny"},
			matched:  true,
			decision: "deny",
		},
		{
			name:    "delete does not use write",
			line:    "*** Delete File: deploy/production/app.yaml",
			rule:    ProtectedPathRule{Pattern: "deploy/production/**", Write: "deny"},
			matched: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protection := ProjectProtection{
				Enabled:        true,
				ProtectedPaths: []ProtectedPathRule{tc.rule},
			}
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "apply_patch",
				ActionType:     "write",
				Target:         "deploy/production/app.yaml",
				Targets:        []string{"deploy/production/app.yaml"},
				ContentPreview: "*** Begin Patch\n" + tc.line + "\n*** End Patch\n",
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if matched != tc.matched || decision.Decision != tc.decision {
				t.Fatalf("patch %s must use its matching rule, got matched=%v decision=%+v", tc.name, matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionKeepsStrictestMixedPatchOperation(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "require_approval"},
			{Pattern: "deploy/production/**", Delete: "deny"},
		},
	}
	for _, lines := range []string{
		"*** Update File: src/core/algorithm.go\n*** Delete File: deploy/production/app.yaml",
		"*** Delete File: deploy/production/app.yaml\n*** Update File: src/core/algorithm.go",
	} {
		decision, matched := EvaluateProjectProtection(ActionInput{
			ToolName:       "apply_patch",
			ActionType:     "write",
			Targets:        []string{"src/core/algorithm.go", "deploy/production/app.yaml"},
			ContentPreview: "*** Begin Patch\n" + lines + "\n*** End Patch\n",
			CWD:            root,
			ProjectRoot:    root,
		}, protection)
		if !matched || decision.Decision != "deny" {
			t.Fatalf("mixed patch must keep the strictest operation regardless of order, got matched=%v decision=%+v", matched, decision)
		}
	}
}

func TestEvaluateProjectProtectionRecognizesShellDeleteFromContent(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "deploy/production/**", Delete: "deny"},
		},
	}
	for _, command := range []string{
		"Remove-Item deploy/production/app.yaml",
		"sudo rm deploy/production/app.yaml",
		"cmd /c del deploy/production/app.yaml",
		"powershell -Command Remove-Item deploy/production/app.yaml",
		"echo ok; rm deploy/production/app.yaml",
	} {
		t.Run(command, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "PowerShell",
				ActionType:     "exec",
				ContentPreview: command,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if !matched || decision.Decision != "deny" {
				t.Fatalf("shell delete must use the delete rule, got matched=%v decision=%+v", matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionDoesNotTreatReadPayloadAsPatchOrCommand(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Read: "require_approval", Write: "deny", Delete: "deny"},
		},
	}
	for _, tc := range []struct {
		name    string
		tool    string
		target  string
		content string
	}{
		{
			name:    "grep patch text",
			tool:    "Grep",
			target:  "docs",
			content: "*** Begin Patch\n*** Update File: src/core/algorithm.go\n*** End Patch",
		},
		{
			name:    "read delete prose",
			tool:    "Read",
			target:  "docs/commands.md",
			content: "This guide explains how rm removes a file.",
		},
		{
			name:    "grep delete command text",
			tool:    "Grep",
			target:  "docs",
			content: "rm src/core/algorithm.go",
		},
		{
			name:    "write delete example",
			tool:    "Write",
			target:  "docs/commands.md",
			content: "rm src/core/algorithm.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       tc.tool,
				ActionType:     "read",
				Target:         tc.target,
				ContentPreview: tc.content,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if matched {
				t.Fatalf("read-only payload text must not create a protected target, got %+v", decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionDoesNotTreatDeleteProseAsCommand(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "deploy/production/**", Delete: "deny"},
		},
	}
	decision, matched := EvaluateProjectProtection(ActionInput{
		ToolName:       "shell",
		ActionType:     "exec",
		ContentPreview: "the rm command deletes a file",
		CWD:            root,
		ProjectRoot:    root,
	}, protection)
	if matched {
		t.Fatalf("plain explanatory text must not be treated as a delete command, got %+v", decision)
	}

	decision, matched = EvaluateProjectProtection(ActionInput{
		ToolName:       "shell",
		ActionType:     "exec",
		ContentPreview: `echo "ok; rm deploy/production/app.yaml"`,
		CWD:            root,
		ProjectRoot:    root,
	}, protection)
	if matched {
		t.Fatalf("quoted delete example must not be treated as an invoked command, got %+v", decision)
	}
}

func TestEvaluateProjectProtectionMapsKnownShellFileOperations(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Read: "require_approval", Write: "deny"},
		},
	}
	cases := []struct {
		name     string
		command  string
		decision string
	}{
		{name: "read", command: "Get-Content src/core/algorithm.go", decision: "ask"},
		{name: "cat read", command: "cat src/core/algorithm.go", decision: "ask"},
		{name: "search read", command: "rg TODO src/core", decision: "ask"},
		{name: "write", command: "Set-Content src/core/algorithm.go changed", decision: "deny"},
		{name: "named write path", command: "Set-Content -Path src/core/algorithm.go -Value changed", decision: "deny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "PowerShell",
				ActionType:     "exec",
				ContentPreview: tc.command,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if !matched || decision.Decision != tc.decision {
				t.Fatalf("known shell %s must use the matching project rule, got matched=%v decision=%+v", tc.name, matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionKeepsStrictestCompoundShellTarget(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "require_approval"},
			{Pattern: "deploy/production/**", Write: "deny", Delete: "deny"},
		},
	}
	for _, command := range []string{
		"Set-Content docs/ok.txt ok; Set-Content deploy/production/app.yaml bad",
		"Set-Content src/core/algorithm.go changed; Remove-Item deploy/production/app.yaml",
		"Remove-Item deploy/production/app.yaml; Set-Content src/core/algorithm.go changed",
		`Remove-Item "docs/ok.txt","deploy/production/app.yaml"`,
	} {
		decision, matched := EvaluateProjectProtection(ActionInput{
			ToolName:       "shell",
			ActionType:     "command",
			ContentPreview: command,
			CWD:            root,
			ProjectRoot:    root,
		}, protection)
		if !matched || decision.Decision != "deny" {
			t.Fatalf("compound shell command must keep the strictest target, command=%q matched=%v decision=%+v", command, matched, decision)
		}
	}
}

func TestEvaluateWithProjectProtectionCannotWeakenBuiltInDeny(t *testing.T) {
	root := t.TempDir()
	writeProjectProtectionTestFile(t, root, validProjectProtectionJSON())
	protection, err := LoadProjectProtection(root)
	if err != nil {
		t.Fatalf("load project protection: %v", err)
	}
	decision := EvaluateWithProjectProtection(ActionInput{
		ToolName:    "Write",
		ActionType:  "write",
		Target:      ".ssh/id_rsa",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if decision.Decision != "deny" || decision.RiskLevel != "critical" {
		t.Fatalf("built-in critical deny must remain stricter, got %+v", decision)
	}
}

func TestEvaluateWithProjectProtectionCanDenyUnlistedEgress(t *testing.T) {
	root := t.TempDir()
	writeProjectProtectionTestFile(t, root, validProjectProtectionJSON())
	protection, err := LoadProjectProtection(root)
	if err != nil {
		t.Fatalf("load project protection: %v", err)
	}

	unlisted := EvaluateWithProjectProtection(ActionInput{
		ToolName:      "network.request",
		ActionType:    "network",
		NetworkMethod: "POST",
		NetworkURL:    "https://uploads.example.test/data",
	}, protection)
	if unlisted.Decision != "deny" || unlisted.Category != "project_egress" {
		t.Fatalf("unlisted network write must be denied, got %+v", unlisted)
	}

	listed := EvaluateWithProjectProtection(ActionInput{
		ToolName:      "network.request",
		ActionType:    "network",
		NetworkMethod: "POST",
		NetworkURL:    "https://api.github.com/repos/example/project/issues",
	}, protection)
	if listed.Decision != "ask" || listed.Category == "project_egress" {
		t.Fatalf("listed host must retain the built-in approval decision, got %+v", listed)
	}
}

func TestEvaluateProtectsProjectProtectionFileFromMutation(t *testing.T) {
	root := t.TempDir()
	decision := Evaluate(ActionInput{
		ToolName:    "Write",
		ActionType:  "write",
		Target:      ".agenttoolgate/protected.json",
		CWD:         root,
		ProjectRoot: root,
	})
	if decision.Decision != "ask" || decision.Category != "agent_self_tamper" || !strings.Contains(strings.Join(decision.Signals, " "), "agent_self_tamper") {
		t.Fatalf("project protection config mutation must ask, got %+v", decision)
	}
}

func writeProjectProtectionTestFile(t *testing.T, root, body string) {
	t.Helper()
	configDir := filepath.Join(root, ".agenttoolgate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create project protection dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "protected.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
}

func validProjectProtectionJSON() string {
	return `{
		"version":1,
		"projectRoot":"<repo>",
		"workspace":{"orgId":"local-org"},
		"localActionFirewall":{
			"enabled":true,
			"defaultMode":"live",
			"protectedPaths":[
				{"pattern":"src/core/**","read":"require_approval","write":"require_approval","reason":"核心算法目录"},
				{"pattern":"deploy/production/**","read":"require_approval","write":"deny","delete":"deny","reason":"生产配置目录"}
			],
			"egress":{
				"enabled":true,
				"allowedHosts":["api.github.com"],
				"unlistedWrite":"deny"
			},
			"notes":[]
		}
	}`
}

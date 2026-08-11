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

func TestEvaluateProjectProtectionPreservesLinuxPathCase(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux 文件系统大小写语义由 Ubuntu CI 验证")
	}
	root := t.TempDir()
	upperDir := filepath.Join(root, "src", "Core")
	lowerDir := filepath.Join(root, "src", "core")
	if err := os.MkdirAll(upperDir, 0o700); err != nil {
		t.Fatalf("create upper-case directory: %v", err)
	}
	if err := os.MkdirAll(lowerDir, 0o700); err != nil {
		t.Fatalf("create lower-case directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upperDir, "policy.go"), []byte("package core\n"), 0o600); err != nil {
		t.Fatalf("write upper-case file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lowerDir, "policy.go"), []byte("package core\n"), 0o600); err != nil {
		t.Fatalf("write lower-case file: %v", err)
	}
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/Core/**", Read: "deny"},
		},
	}

	decision, matched := EvaluateProjectProtection(ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      "src/core/policy.go",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if matched {
		t.Fatalf("different-case Linux path must not match protected rule, got %+v", decision)
	}

	decision, matched = EvaluateProjectProtection(ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      "src/Core/policy.go",
		CWD:         root,
		ProjectRoot: root,
	}, protection)
	if !matched || decision.Decision != "deny" {
		t.Fatalf("same-case Linux path must match protected rule, matched=%v decision=%+v", matched, decision)
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

func TestEvaluateProjectProtectionClassifiesPatchMoveSourceAsDelete(t *testing.T) {
	root := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: src/core/algorithm.go\n*** Move to: docs/algorithm.go\n*** End Patch\n"

	deleteDecision, deleteMatched := EvaluateProjectProtection(ActionInput{
		ToolName:       "apply_patch",
		ActionType:     "write",
		ContentPreview: patch,
		CWD:            root,
		ProjectRoot:    root,
	}, ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Delete: "deny"},
		},
	})
	if !deleteMatched || deleteDecision.Decision != "deny" {
		t.Fatalf("patch move source must use delete rule, got matched=%v decision=%+v", deleteMatched, deleteDecision)
	}

	writeDecision, writeMatched := EvaluateProjectProtection(ActionInput{
		ToolName:       "apply_patch",
		ActionType:     "write",
		ContentPreview: patch,
		CWD:            root,
		ProjectRoot:    root,
	}, ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "deny"},
		},
	})
	if writeMatched {
		t.Fatalf("patch move source must not be classified as a write, got %+v", writeDecision)
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
		"echo ok\nrm deploy/production/app.yaml",
		`printf "x\""; rm deploy/production/app.yaml`,
		`printf "x\\"; rm deploy/production/app.yaml`,
		"Write-Output \"x`\"\"; Remove-Item deploy/production/app.yaml",
		"Write-Output \"x``\"; Remove-Item deploy/production/app.yaml",
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

func TestEvaluateProjectProtectionMapsExtendedShellOperations(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Read: "deny", Write: "deny", Delete: "deny", Exec: "deny"},
		},
	}
	cases := []struct {
		name    string
		command string
	}{
		{name: "select string read", command: "Select-String -Pattern TODO -Path src/core/algorithm.go"},
		{name: "select string alias read", command: "sls -Pattern TODO -Path src/core/algorithm.go"},
		{name: "select string positional path", command: "Select-String TODO src/core/algorithm.go"},
		{name: "select string named pattern positional path", command: "Select-String -Pattern TODO src/core/algorithm.go"},
		{name: "redirect write", command: "echo generated > src/core/generated.go"},
		{name: "append redirect write", command: "echo generated >> src/core/generated.go"},
		{name: "clobber redirect write", command: "echo generated >| src/core/generated.go"},
		{name: "tee write", command: "echo generated | tee src/core/generated.go"},
		{name: "powershell tee write", command: "Get-Content input.txt | Tee-Object -FilePath src/core/generated.go"},
		{name: "truncate write", command: "truncate -s 0 src/core/generated.go"},
		{name: "interpreter script exec", command: "python src/core/tool.py"},
		{name: "later interpreter script exec", command: "python bootstrap.py src/core/tool.py"},
		{name: "python launcher script exec", command: "py -3 src/core/tool.py"},
		{name: "versioned interpreter script exec", command: "/usr/bin/python3.12 src/core/tool.py"},
		{name: "direct script exec", command: "./src/core/tool.sh"},
		{name: "move source delete", command: "mv src/core/algorithm.go docs/algorithm.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: tc.command,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if !matched || decision.Decision != "deny" {
				t.Fatalf("extended shell operation must use project rule, command=%q matched=%v decision=%+v", tc.command, matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionIgnoresQuotedRedirection(t *testing.T) {
	root := t.TempDir()
	decision, matched := EvaluateProjectProtection(ActionInput{
		ToolName:       "shell",
		ActionType:     "exec",
		ContentPreview: `echo "> src/core/generated.go"`,
		CWD:            root,
		ProjectRoot:    root,
	}, ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "deny"},
		},
	})
	if matched {
		t.Fatalf("quoted redirection text must not become a write target, got %+v", decision)
	}

	decision, matched = EvaluateProjectProtection(ActionInput{
		ToolName:       "shell",
		ActionType:     "exec",
		ContentPreview: `echo \> src/core/generated.go`,
		CWD:            root,
		ProjectRoot:    root,
	}, ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "deny"},
		},
	})
	if matched {
		t.Fatalf("escaped redirection text must not become a write target, got %+v", decision)
	}
}

func TestEvaluateProjectProtectionClassifiesCopyMoveArguments(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		command   string
		pattern   string
		operation string
		wantMatch bool
	}{
		{name: "copy destination before source", command: "Copy-Item -Destination deploy/production/app.yaml -Path docs/app.yaml", pattern: "deploy/production/**", operation: "write", wantMatch: true},
		{name: "copy source read", command: "Copy-Item -Destination docs/app.yaml -Path src/core/app.yaml", pattern: "src/core/**", operation: "read", wantMatch: true},
		{name: "move source delete", command: "Move-Item -Destination docs/app.yaml -Path src/core/app.yaml", pattern: "src/core/**", operation: "delete", wantMatch: true},
		{name: "move destination write", command: "Move-Item -Path docs/app.yaml -Destination deploy/production/app.yaml", pattern: "deploy/production/**", operation: "write", wantMatch: true},
		{name: "rename destination stays beside source", command: "Rename-Item -NewName replacement.go -Path src/core/algorithm.go", pattern: "src/core/replacement.go", operation: "write", wantMatch: true},
		{name: "truncate reference is read", command: "truncate --reference src/core/reference.go docs/output.go", pattern: "src/core/reference.go", operation: "read", wantMatch: true},
		{name: "truncate reference is not write", command: "truncate --reference src/core/reference.go docs/output.go", pattern: "src/core/reference.go", operation: "write", wantMatch: false},
		{name: "truncate attached reference is read", command: "truncate -rsrc/core/reference.go docs/output.go", pattern: "src/core/reference.go", operation: "read", wantMatch: true},
		{name: "powershell copy alias destination", command: "cp -Destination deploy/production/app.yaml -Path docs/app.yaml", pattern: "deploy/production/**", operation: "write", wantMatch: true},
		{name: "powershell copy alias source", command: "cp -Destination docs/app.yaml -Path src/core/app.yaml", pattern: "src/core/**", operation: "read", wantMatch: true},
		{name: "powershell move alias source", command: "mv -Destination docs/app.yaml -Path src/core/app.yaml", pattern: "src/core/**", operation: "delete", wantMatch: true},
		{name: "posix copy target directory", command: "cp -t deploy/production docs/app.yaml", pattern: "deploy/production", operation: "write", wantMatch: true},
		{name: "posix move target directory source", command: "mv --target-directory docs src/core/app.yaml", pattern: "src/core/**", operation: "delete", wantMatch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule := ProtectedPathRule{Pattern: tc.pattern}
			switch tc.operation {
			case "read":
				rule.Read = "deny"
			case "write":
				rule.Write = "deny"
			case "delete":
				rule.Delete = "deny"
			default:
				t.Fatalf("unsupported test operation %q", tc.operation)
			}
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: tc.command,
				CWD:            root,
				ProjectRoot:    root,
			}, ProjectProtection{Enabled: true, ProtectedPaths: []ProtectedPathRule{rule}})
			if matched != tc.wantMatch {
				t.Fatalf("unexpected copy/move classification, matched=%v decision=%+v", matched, decision)
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

func TestEvaluateProjectProtectionCoversStaticNetworkOutputFiles(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "deploy/production/**", Write: "deny"},
		},
	}
	for _, command := range []string{
		`curl -o deploy/production/download.bin https://downloads.example.test/file`,
		`curl -sodeploy/production/download.bin https://downloads.example.test/file`,
		`curl --output=deploy/production/download.bin https://downloads.example.test/file`,
		`curl -c deploy/production/cookies.txt https://downloads.example.test/file`,
		`curl -Ddeploy/production/headers.txt https://downloads.example.test/file`,
		`Invoke-WebRequest -Uri https://downloads.example.test/file -OutFile deploy/production/download.bin`,
		`iwr -Uri https://downloads.example.test/file -OutF deploy/production/download.bin`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: command,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if !matched || decision.Decision != "deny" || decision.Category != "project_protected_path" {
				t.Fatalf("static network output must use the protected write rule, command=%q matched=%v decision=%+v", command, matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionCoversStaticNetworkInputFiles(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Read: "deny"},
		},
		Egress: EgressRule{
			Enabled:       true,
			AllowedHosts:  []string{"api.github.com"},
			UnlistedWrite: "deny",
		},
	}
	for _, command := range []string{
		`curl -T src/core/archive.bin https://api.github.com/upload`,
		`curl --upload-file=src/core/archive.bin https://api.github.com/upload`,
		`curl -d @src/core/payload.json https://api.github.com/upload`,
		`curl --data-binary @src/core/payload.json https://api.github.com/upload`,
		`curl -F file=@src/core/payload.json https://api.github.com/upload`,
		`Invoke-RestMethod -InFile src/core/payload.json -Method Post -Uri https://api.github.com/upload`,
		`curl -d @- https://api.github.com/upload < src/core/payload.json`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: command,
				CWD:            root,
				ProjectRoot:    root,
			}, protection)
			if !matched || decision.Decision != "deny" || decision.Category != "project_protected_path" {
				t.Fatalf("static network input must use the protected read rule, command=%q matched=%v decision=%+v", command, matched, decision)
			}
		})
	}
}

func TestEvaluateProjectProtectionCoversStaticShellEgressWrites(t *testing.T) {
	protection := ProjectProtection{
		Enabled: true,
		Egress: EgressRule{
			Enabled:       true,
			AllowedHosts:  []string{"api.github.com"},
			UnlistedWrite: "deny",
		},
	}
	for _, command := range []string{
		`curl -X POST https://uploads.example.test/data`,
		`curl -iXPOST https://uploads.example.test/data`,
		`curl -sdsynthetic https://uploads.example.test/data`,
		`curl -sFmessage=synthetic https://uploads.example.test/data`,
		`curl -sTpayload.txt https://uploads.example.test/data`,
		`curl -d synthetic uploads.example.test/data`,
		`curl https://api.github.com/status --next -d synthetic https://uploads.example.test/data`,
		`curl --data synthetic https://uploads.example.test/data`,
		`curl --data-ascii synthetic https://uploads.example.test/data`,
		`curl --form-string message=synthetic https://uploads.example.test/data`,
		`curl --json '{"message":"synthetic"}' https://uploads.example.test/data`,
		`curl -dsynthetic -X GET https://uploads.example.test/data`,
		`curl -i -X POST https://uploads.example.test/data`,
		`Invoke-RestMethod -Method Post -Uri https://uploads.example.test/data`,
		`Invoke-RestMethod -Me Post -Uri https://uploads.example.test/data`,
		`Invoke-WebRequest -Me:Get -B:synthetic -Uri:https://uploads.example.test/data`,
		`Invoke-RestMethod -Method:Get -Body:synthetic -Uri:https://uploads.example.test/data`,
		`Invoke-RestMethod https://uploads.example.test/data -Body synthetic`,
		`powershell -Command Invoke-RestMethod -Method Post -Uri https://uploads.example.test/data`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: command,
			}, protection)
			if !matched || decision.Decision != "deny" || decision.Category != "project_egress" {
				t.Fatalf("static shell egress write must be denied, command=%q matched=%v decision=%+v", command, matched, decision)
			}
		})
	}

	for _, command := range []string{
		`curl https://uploads.example.test/data`,
		`curl -I https://uploads.example.test/data`,
		`curl -sI https://uploads.example.test/data`,
		`curl -i https://uploads.example.test/data`,
		`curl -iXGET https://uploads.example.test/data`,
		`curl -f https://uploads.example.test/data`,
		`curl -sf https://uploads.example.test/data`,
		`curl -stsynthetic https://uploads.example.test/data`,
		`curl -sxhttps://proxy.example.test https://uploads.example.test/data`,
		`curl -x https://proxy.example.test https://uploads.example.test/data`,
		`curl -m 5 https://uploads.example.test/data`,
		`curl --connect-timeout 5 https://uploads.example.test/data`,
		`curl -H "Referer: https://docs.example.test" https://uploads.example.test/data`,
		`Invoke-RestMethod -Method Get -Uri https://uploads.example.test/data`,
		`Invoke-RestMethod -Me Get -Uri https://uploads.example.test/data`,
		`curl -d synthetic https://api.github.com/upload --next https://uploads.example.test/data`,
		`curl -X POST https://api.github.com/repos/example/project/issues`,
		`curl -m 5 -d synthetic https://api.github.com/upload`,
		`curl --connect-timeout 5 -d synthetic https://api.github.com/upload`,
	} {
		t.Run("allowed "+command, func(t *testing.T) {
			decision, matched := EvaluateProjectProtection(ActionInput{
				ToolName:       "shell",
				ActionType:     "exec",
				ContentPreview: command,
			}, protection)
			if matched {
				t.Fatalf("read-only or allowlisted shell network request must not add a project floor, command=%q decision=%+v", command, decision)
			}
		})
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
	if decision.Decision != "deny" || decision.RiskLevel != "critical" ||
		decision.Category != "agent_self_tamper" ||
		!strings.Contains(strings.Join(decision.Signals, " "), "agent_self_tamper") {
		t.Fatalf("project protection config mutation must fail closed, got %+v", decision)
	}
}

func TestEvaluateAllowsReadingGeneratedProjectClientConfig(t *testing.T) {
	root := t.TempDir()
	decision := Evaluate(ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      ".agenttoolgate/clients/codex.config.snippet.toml",
		CWD:         root,
		ProjectRoot: root,
	})
	if decision.Decision != "allow" || !decision.Silent {
		t.Fatalf("reading generated client config must remain low friction, got %+v", decision)
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

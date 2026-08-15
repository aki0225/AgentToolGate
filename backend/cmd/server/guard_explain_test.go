package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agenttoolgate/backend/internal/guard"
)

func TestRunGuardExplainActionReportsDecisionsWithoutSensitiveContent(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeClaude); err != nil {
		t.Fatalf("init project: %v", err)
	}
	protected := `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[{"pattern":"src/core/**","write":"deny","reason":"synthetic-rule-secret"}]
		}
	}`
	if err := os.WriteFile(projectProtectedPath(project), []byte(protected), 0o600); err != nil {
		t.Fatalf("write project protection: %v", err)
	}
	outside := t.TempDir()
	secret := "synthetic-explain-secret"
	inputPath := filepath.Join(outside, "action.json")
	input := map[string]any{
		"toolName":       "Write",
		"actionType":     "write",
		"target":         "src/core/algorithm.go",
		"cwd":            outside,
		"projectRoot":    outside,
		"contentPreview": secret,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	if err := os.WriteFile(inputPath, raw, 0o600); err != nil {
		t.Fatalf("write action: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "explain", "action", "--input", inputPath, "--dir", project, "--format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("explain returned %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if strings.Contains(output, secret) || strings.Contains(output, "synthetic-rule-secret") || strings.Contains(output, outside) {
		t.Fatalf("explain output leaked content or an outside absolute path: %s", output)
	}
	var report struct {
		BuiltIn struct {
			Decision string `json:"decision"`
		} `json:"builtIn"`
		Floor *struct {
			Decision string `json:"decision"`
		} `json:"floor"`
		Final struct {
			Decision string `json:"decision"`
		} `json:"final"`
		MatchedRules []struct {
			Pattern string `json:"pattern"`
		} `json:"matchedRules"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode explain report: %v output=%s", err, output)
	}
	if report.BuiltIn.Decision != "allow" || report.Floor == nil || report.Floor.Decision != "deny" ||
		report.Final.Decision != "deny" || len(report.MatchedRules) != 1 {
		t.Fatalf("unexpected explain report: %+v", report)
	}
	if _, err := os.Stat(projectHookControlPath(project)); !os.IsNotExist(err) {
		t.Fatalf("read-only explain must not create hook control, got %v", err)
	}
}

func TestRunGuardExplainSupportsCodexAndClaudePayloads(t *testing.T) {
	project := t.TempDir()
	if _, err := writeProjectInitFiles(project, projectInitModeClaude); err != nil {
		t.Fatalf("init project: %v", err)
	}
	for _, client := range []string{"codex", "claude"} {
		t.Run(client, func(t *testing.T) {
			payload := `{"tool_name":"Read","cwd":"` + strings.ReplaceAll(project, `\`, `\\`) + `","tool_input":{"path":"README.md"}}`
			inputPath := filepath.Join(t.TempDir(), client+".json")
			if err := os.WriteFile(inputPath, []byte(payload), 0o600); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"guard", "explain", client, "--input", inputPath, "--dir", project, "--format", "text"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("explain returned %d stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "最终决策: allow") ||
				!strings.Contains(strings.ToLower(stdout.String()), "readme.md") {
				t.Fatalf("unexpected text explanation:\n%s", stdout.String())
			}
		})
	}
}

func TestRunGuardExplainRejectsUnknownAction(t *testing.T) {
	project := t.TempDir()
	inputPath := filepath.Join(t.TempDir(), "action.json")
	if err := os.WriteFile(inputPath, []byte(`{"toolName":"custom","actionType":"mystery"}`), 0o600); err != nil {
		t.Fatalf("write action: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"guard", "explain", "action", "--input", inputPath, "--dir", project, "--format", "json"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "actionType") {
		t.Fatalf("unknown action must fail before evaluation, code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunGuardExplainRejectsNonStrictJSONForEveryInputType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		client string
		body   string
	}{
		{
			name:   "action wrong case",
			client: "action",
			body:   `{"ToolName":"Read","actionType":"read"}`,
		},
		{
			name:   "codex trailing json",
			client: "codex",
			body:   `{"tool_name":"Read","tool_input":{"path":"README.md"}} {}`,
		},
		{
			name:   "claude null field",
			client: "claude",
			body:   `{"tool_name":"Read","tool_input":{"path":null}}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := t.TempDir()
			inputPath := filepath.Join(t.TempDir(), "payload.json")
			if err := os.WriteFile(inputPath, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run([]string{"guard", "explain", tc.client, "--input", inputPath, "--dir", project, "--format", "json"}, &stdout, &stderr)
			if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("non-strict %s input must fail before explanation, code=%d stdout=%s stderr=%s", tc.client, code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWriteGuardExplanationEscapesTextControlCharacters(t *testing.T) {
	explanation := guard.ProjectExplanation{
		NormalizedTargets: []guard.ProjectExplanationTarget{
			{Kind: "path", Value: "target\nnext\x1b\\file", Operation: "write"},
		},
		BuiltIn: guard.ProjectDecisionExplanation{
			Decision: "allow", RiskLevel: "low", Category: "test",
		},
		MatchedRules: []guard.ProjectRuleMatch{
			{
				Kind:      "protected_path",
				Target:    "target\nnext\x1b\\file",
				Operation: "write",
				Pattern:   "target\n**",
				Effect:    "deny",
			},
		},
		Floor: &guard.ProjectDecisionExplanation{
			Decision: "deny", RiskLevel: "high", Category: "project_protected_path",
		},
		Final: guard.ProjectDecisionExplanation{
			Decision: "deny", RiskLevel: "high", Category: "project_protected_path",
		},
	}
	var output bytes.Buffer
	if err := writeGuardExplanation(&output, explanation, "text"); err != nil {
		t.Fatalf("write explanation: %v", err)
	}
	text := output.String()
	if strings.ContainsRune(text, '\x1b') || strings.Contains(text, "target\nnext") {
		t.Fatalf("text explanation contains raw control characters: %q", text)
	}
	for _, escaped := range []string{`target\nnext\u001B\\file`, `target\n**`} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("text explanation missing escaped value %q: %q", escaped, text)
		}
	}
}

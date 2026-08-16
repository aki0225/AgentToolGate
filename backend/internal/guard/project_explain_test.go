package guard

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeActionInputRejectsNonStrictJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"toolName":"Read","actionType":"read","unexpected":true}`,
		},
		{
			name: "wrong case field",
			body: `{"ToolName":"Read","actionType":"read"}`,
		},
		{
			name: "duplicate field",
			body: `{"toolName":"Read","toolName":"Write","actionType":"read"}`,
		},
		{
			name: "case alias duplicate",
			body: `{"toolName":"Read","ToolName":"Write","actionType":"read"}`,
		},
		{
			name: "trailing json",
			body: `{"toolName":"Read","actionType":"read"} {}`,
		},
		{
			name: "null optional field",
			body: `{"toolName":"Read","actionType":"read","contentPreview":null}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeActionInput([]byte(tc.body)); err == nil {
				t.Fatalf("expected strict action input rejection for %s", tc.name)
			}
		})
	}
}

func TestDecodeActionInputRejectsUnknownAction(t *testing.T) {
	_, err := DecodeActionInput([]byte(`{"toolName":"custom","actionType":"mystery"}`))
	if err == nil || !strings.Contains(err.Error(), "actionType") {
		t.Fatalf("unknown action must be rejected, got %v", err)
	}
}

func TestLoadProjectProtectionRejectsConflictingDuplicateEffects(t *testing.T) {
	root := t.TempDir()
	writeProjectProtectionTestFile(t, root, `{
		"version":1,
		"localActionFirewall":{
			"enabled":true,
			"protectedPaths":[
				{"pattern":"src/core/**","write":"require_approval"},
				{"pattern":"src\\core\\**","write":"deny"}
			]
		}
	}`)

	if _, err := LoadProjectProtection(root); err == nil {
		t.Fatal("conflicting effects for the same normalized pattern and action must be rejected")
	}
}

func TestExplainWithProjectProtectionUsesSharedMatchingCore(t *testing.T) {
	root := t.TempDir()
	protection := ProjectProtection{
		Enabled: true,
		ProtectedPaths: []ProtectedPathRule{
			{Pattern: "src/core/**", Write: "deny"},
		},
	}
	explanation := ExplainWithProjectProtection(ActionInput{
		ToolName:       "Write",
		ActionType:     "write",
		Target:         "src/core/algorithm.go",
		ContentPreview: "synthetic-secret-value",
		CWD:            root,
		ProjectRoot:    root,
	}, protection)

	if explanation.BuiltIn.Decision != "allow" {
		t.Fatalf("ordinary workspace write should be allowed by the built-in guard, got %+v", explanation.BuiltIn)
	}
	if explanation.Floor == nil || explanation.Floor.Decision != "deny" {
		t.Fatalf("project floor must deny, got %+v", explanation.Floor)
	}
	if explanation.Final.Decision != "deny" {
		t.Fatalf("final decision must keep the project floor, got %+v", explanation.Final)
	}
	if len(explanation.MatchedRules) != 1 ||
		explanation.MatchedRules[0].Pattern != "src/core/**" ||
		explanation.MatchedRules[0].Operation != "write" {
		t.Fatalf("expected one protected path match, got %+v", explanation.MatchedRules)
	}
	if len(explanation.NormalizedTargets) != 1 ||
		explanation.NormalizedTargets[0].Value != "src/core/algorithm.go" {
		t.Fatalf("expected repo-relative normalized target, got %+v", explanation.NormalizedTargets)
	}
}

func TestExplainWithProjectProtectionRedactsOutsideRepositoryPaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	explanation := ExplainWithProjectProtection(ActionInput{
		ToolName:    "Read",
		ActionType:  "read",
		Target:      outside,
		CWD:         root,
		ProjectRoot: root,
	}, ProjectProtection{})

	if len(explanation.NormalizedTargets) != 1 ||
		explanation.NormalizedTargets[0].Value != "<outside-repo>" {
		t.Fatalf("outside absolute path must be redacted, got %+v", explanation.NormalizedTargets)
	}
}

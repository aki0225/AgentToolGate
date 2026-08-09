package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

const (
	approvalResponseContentSecret   = "approval-content-secret-should-not-leak"
	approvalResponseQuerySecret     = "approval-query-secret-should-not-leak"
	approvalResponseSignatureSecret = "approval-signature-secret-should-not-leak"
	approvalResponseCLISecret       = "approval-cli-secret-should-not-leak"
	approvalResponseJSONSecret      = "approval-json-secret-should-not-leak"
	approvalResponseUser            = "approval-url-user"
	approvalResponsePassword        = "approval-url-password"
	approvalResponseFragment        = "approval-private-fragment"
)

func TestApprovalListResponseRedactsAgentGuardPayload(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	seedApprovalResponsePrivacyFixture(t, st, workspace)

	resp := getJSON(t, srv, "/api/approvals")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	assertApprovalResponseBodyDoesNotLeak(t, resp.Body.String())

	var payload struct {
		Items []approvalResponse `json:"items"`
	}
	decodeBody(t, resp.Body.Bytes(), &payload)
	if len(payload.Items) != 1 {
		t.Fatalf("expected one approval, got %+v", payload.Items)
	}
	assertSafeApprovalResponse(t, payload.Items[0])
}

func TestApprovalActionResponseRedactsAgentGuardPayload(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	approval := seedApprovalResponsePrivacyFixture(t, st, workspace)

	resp := postJSON(t, srv, "/api/approvals/"+approval.ID+"/approve", `{"reason":"reviewed"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	assertApprovalResponseBodyDoesNotLeak(t, resp.Body.String())

	var payload approvalActionResponse
	decodeBody(t, resp.Body.Bytes(), &payload)
	assertSafeApprovalResponse(t, payload.Approval)
	if string(payload.ToolCall.InputRedactedJSON) != "{}" {
		t.Fatalf("agent guard action response must not return the stored decision payload, got %s", payload.ToolCall.InputRedactedJSON)
	}
}

func TestApprovalActionResponseUsesStableFailureMessage(t *testing.T) {
	t.Parallel()

	response := newApprovalActionResponse(
		model.ApprovalRequest{},
		model.ToolCall{
			Status:       "failed",
			ErrorMessage: "request failed for https://user:password@example.test/?token=secret#fragment",
		},
	)
	if response.ToolCall.ErrorMessage != approvalExecutionFailedMessage {
		t.Fatalf("expected stable failure message, got %q", response.ToolCall.ErrorMessage)
	}
	if response.Result != nil {
		t.Fatalf("failed approval action must not return a partial result, got %+v", response.Result)
	}
}

func TestApprovalActionResponsePreservesStableRevalidationFailure(t *testing.T) {
	t.Parallel()

	response := newApprovalActionResponse(
		model.ApprovalRequest{},
		model.ToolCall{
			Status:       "failed",
			ErrorMessage: approvalRevalidationFailedMessage,
		},
	)
	if response.ToolCall.ErrorMessage != approvalRevalidationFailedMessage {
		t.Fatalf("expected stable revalidation failure, got %q", response.ToolCall.ErrorMessage)
	}
}

func TestApprovalActionResponseRedactsExplanation(t *testing.T) {
	t.Parallel()

	const secret = "explanation-secret-should-not-leak"
	response := newApprovalActionResponse(
		model.ApprovalRequest{},
		model.ToolCall{
			Status: "approval_required",
			Explanation: &model.ToolCallExplanation{
				TargetCategory: "target token=" + secret,
				RiskLevel:      "high",
				MatchedRule:    "rule https://user:password@example.test/path?api_key=" + secret,
				Signals:        []string{"Bearer " + secret, "safe signal"},
			},
		},
	)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "user:password") {
		t.Fatalf("approval explanation leaked sensitive data: %s", raw)
	}
	if response.ToolCall.Explanation == nil || response.ToolCall.Explanation.Signals[1] != "safe signal" {
		t.Fatalf("approval explanation lost safe data: %+v", response.ToolCall.Explanation)
	}
}

func TestApprovalActionResponseBuildsResultFromRedactedOutput(t *testing.T) {
	t.Parallel()

	const secret = "approval-result-secret-should-not-leak"
	response := newApprovalActionResponse(
		model.ApprovalRequest{},
		model.ToolCall{
			Status: "success",
			InputRedactedJSON: json.RawMessage(
				`{"url":"https://user:password@example.test/input?token=` + secret + `#private"}`,
			),
			OutputRedactedJSON: json.RawMessage(
				`{"url":"https://user:password@example.test/output?api_key=` + secret + `#private","token":"` + secret + `","message":"safe"}`,
			),
		},
	)

	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(raw), secret) ||
		strings.Contains(string(raw), "user:password") ||
		strings.Contains(string(raw), "#private") {
		t.Fatalf("approval action leaked sensitive result data: %s", raw)
	}

	result, ok := response.Result.(map[string]any)
	if !ok || result["message"] != "safe" || result["token"] != "[REDACTED]" {
		t.Fatalf("approval action must return the safe output view, got %+v", response.Result)
	}
}

func TestRedactApprovalTextSanitizesEmbeddedNonHTTPURL(t *testing.T) {
	t.Parallel()

	redacted := redactApprovalText(
		"connect postgres://db-user:db-password@example.test/app?password=db-query-secret&sslmode=require#private-fragment",
	)
	for _, secret := range []string{"db-user", "db-password", "db-query-secret", "private-fragment"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("embedded URL leaked %q: %s", secret, redacted)
		}
	}
	parsed, err := url.Parse(strings.TrimPrefix(redacted, "connect "))
	if err != nil {
		t.Fatalf("parse redacted embedded URL: %v", err)
	}
	if parsed.Query().Get("sslmode") != "require" || parsed.Query().Get("password") != "[REDACTED]" {
		t.Fatalf("embedded URL lost safe query data or failed to redact password: %s", redacted)
	}
}

func TestApprovalResponsesRedactCLIAndQuotedJSONSecrets(t *testing.T) {
	t.Parallel()

	const (
		passwordSecret = "cli-password-secret"
		tokenSecret    = "cli-token-secret"
		jsonSecret     = "json-token-secret"
	)
	raw := `run --password ` + passwordSecret + ` --token "` + tokenSecret + `" payload={"token":"` + jsonSecret + `","mode":"safe"}`

	approval := newApprovalResponse(model.ApprovalRequest{
		Target:          raw,
		CanonicalTarget: raw,
		Reason:          raw,
	})
	action := newApprovalActionResponse(model.ApprovalRequest{
		Target:          raw,
		CanonicalTarget: raw,
		Reason:          raw,
	}, model.ToolCall{Status: "approval_required"})

	encoded, err := json.Marshal([]any{approval, action})
	if err != nil {
		t.Fatalf("marshal approval responses: %v", err)
	}
	for _, secret := range []string{passwordSecret, tokenSecret, jsonSecret} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("approval response leaked %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"mode\":\"safe\"`) {
		t.Fatalf("approval redaction must preserve non-sensitive JSON fields: %s", encoded)
	}
}

func TestRedactApprovalTextPreservesOrdinarySecurityTerms(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "password policy", input: "password policy", want: "password policy"},
		{name: "token budget", input: "token budget", want: "token budget"},
		{name: "tokenize flag", input: "run --tokenize input.txt", want: "run --tokenize input.txt"},
		{name: "tokenizer field", input: `payload={"tokenizer":"safe"}`, want: `payload={"tokenizer":"safe"}`},
		{
			name:  "quoted token field",
			input: `payload={"token":"synthetic-secret","mode":"safe"}`,
			want:  `payload={"token":"[REDACTED]","mode":"safe"}`,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := redactApprovalText(tc.input); got != tc.want {
				t.Fatalf("redactApprovalText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func seedApprovalResponsePrivacyFixture(t *testing.T, st store.Store, workspace model.Workspace) model.ApprovalRequest {
	t.Helper()

	tool, err := st.GetToolByKey(context.Background(), workspace.ID, agentGuardEvaluateToolKey)
	if err != nil {
		t.Fatalf("get agent guard tool: %v", err)
	}
	rawTarget := approvalResponseRawTarget()
	decisionPayload, err := json.Marshal(map[string]any{
		"adapter":              "codex",
		"tool":                 "Write",
		"actionType":           "write",
		"target":               rawTarget,
		"isScript":             false,
		"contentEncoding":      "plain",
		"content":              approvalResponseContentSecret,
		"contentHash":          "content-hash-visible",
		"scriptHash":           "script-hash-visible",
		"targetCategory":       "external",
		"contentSensitive":     true,
		"canonicalTarget":      rawTarget,
		"resolvedPath":         `C:\Users\private\secret.txt`,
		"resolvedParentPath":   `C:\Users\private`,
		"resolvedFileIdentity": "private-file-identity",
		"parentIdentity":       "private-parent-identity",
		"riskLevel":            "high",
		"fingerprint":          "fingerprint-visible",
	})
	if err != nil {
		t.Fatalf("marshal decision payload: %v", err)
	}

	approval, err := st.CreateApprovalRequest(context.Background(), model.CreateApprovalRequestInput{
		WorkspaceID:     workspace.ID,
		ToolKey:         tool.Key(),
		ToolDisplayName: tool.DisplayName,
		RequestedBy:     "independent-requester",
		Reason: "approval required for " + rawTarget +
			" run --token " + approvalResponseCLISecret +
			` payload={"token":"` + approvalResponseJSONSecret + `","mode":"safe"}`,
		Fingerprint:         "fingerprint-visible",
		Adapter:             "codex",
		ActionType:          "write",
		Target:              rawTarget,
		CanonicalTarget:     rawTarget,
		ContentEncoding:     "plain",
		ContentHash:         "content-hash-visible",
		ScriptHash:          "script-hash-visible",
		DecisionPayloadJSON: decisionPayload,
		TTL:                 time.Hour,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	if _, err := st.CreateToolCall(context.Background(), model.CreateToolCallInput{
		WorkspaceID:        workspace.ID,
		RequestID:          "request-approval-response-privacy",
		ToolID:             tool.ID,
		ToolKey:            tool.Key(),
		Status:             "approval_required",
		RiskLevel:          "high",
		PolicyDecision:     policyRequireApproval,
		ApprovalID:         approval.ID,
		InputRedactedJSON:  decisionPayload,
		InputExecutionJSON: decisionPayload,
		OutputRedactedJSON: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("create tool call: %v", err)
	}
	return approval
}

func approvalResponseRawTarget() string {
	return "https://" + approvalResponseUser + ":" + approvalResponsePassword +
		"@example.test/upload?mode=safe&token=" + approvalResponseQuerySecret +
		"&X-Amz-Signature=" + approvalResponseSignatureSecret + "#" + approvalResponseFragment
}

func assertApprovalResponseBodyDoesNotLeak(t *testing.T, body string) {
	t.Helper()

	for _, secret := range []string{
		approvalResponseContentSecret,
		approvalResponseQuerySecret,
		approvalResponseSignatureSecret,
		approvalResponseCLISecret,
		approvalResponseJSONSecret,
		approvalResponseUser,
		approvalResponsePassword,
		approvalResponseFragment,
		`C:\Users\private`,
		"private-file-identity",
		"private-parent-identity",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("approval response leaked %q: %s", secret, body)
		}
	}
}

func assertSafeApprovalResponse(t *testing.T, approval approvalResponse) {
	t.Helper()

	for _, target := range []string{approval.Target, approval.CanonicalTarget} {
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatalf("parse redacted target %q: %v", target, err)
		}
		if parsed.User != nil {
			t.Fatalf("redacted target must remove userinfo: %q", target)
		}
		if parsed.Fragment != "" {
			t.Fatalf("redacted target must clear fragment: %q", target)
		}
		if parsed.Query().Get("token") != "[REDACTED]" || parsed.Query().Get("X-Amz-Signature") != "[REDACTED]" {
			t.Fatalf("redacted target must mask sensitive query values: %q", target)
		}
		if parsed.Query().Get("mode") != "safe" {
			t.Fatalf("redacted target must preserve non-sensitive query values: %q", target)
		}
	}

	var summary map[string]any
	if err := json.Unmarshal(approval.DecisionPayloadJSON, &summary); err != nil {
		t.Fatalf("decode decision summary: %v", err)
	}
	for _, forbidden := range []string{
		"content",
		"target",
		"canonicalTarget",
		"resolvedPath",
		"resolvedParentPath",
		"resolvedFileIdentity",
		"parentIdentity",
	} {
		if _, exists := summary[forbidden]; exists {
			t.Fatalf("decision summary must omit %q: %+v", forbidden, summary)
		}
	}
	if summary["contentHash"] != "content-hash-visible" || summary["targetCategory"] != "external" || summary["riskLevel"] != "high" {
		t.Fatalf("decision summary lost required hash or classification fields: %+v", summary)
	}
}

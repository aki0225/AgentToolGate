package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agenttoolgate/backend/internal/auth"
	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"
)

func TestMemoryBootstrapRegistersHTTPRequest(t *testing.T) {
	t.Parallel()

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
	tool, err := st.GetToolByKey(context.Background(), workspaces[0].ID, "http.request")
	if err != nil {
		t.Fatalf("get http.request: %v", err)
	}
	if tool.OperationType != "read" || tool.RiskLevel != "medium" || tool.RequiresApproval {
		t.Fatalf("unexpected http.request metadata: %+v", tool)
	}
}

func TestHTTPRequestGETAllowlistSuccessWritesRedactedAudit(t *testing.T) {
	t.Parallel()

	const requestSecret = "request-api-key-secret"
	const responseHeaderSecret = "response-token-secret"
	const responseBodySecret = "response-body-token"
	const responseDescriptionEmail = "alice@example.com"
	const responseDescriptionPhone = "+15551234567"
	const responseDescriptionToken = "sk_test_1234567890abcdef"
	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("X-Demo") != "hello" || r.Header.Get("X-Api-Key") != requestSecret {
			t.Fatalf("unexpected request headers: %+v", r.Header)
		}
		w.Header().Set("X-Token", responseHeaderSecret)
		w.Header().Set("Set-Cookie", "session=secret")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"description": "contact " + responseDescriptionEmail + " or " + responseDescriptionPhone + " with " + responseDescriptionToken,
			"token":       responseBodySecret,
			"password":    "pw",
			"nested":      map[string]any{"secret": "nested-secret"},
		})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	body := `{"tool":"http.request","arguments":{"method":"GET","url":"` + mockHTTP.URL + `/status","headers":{"X-Demo":"hello","X-Api-Key":"` + requestSecret + `"}}}`
	resp := postJSON(t, srv, "/api/tool-calls", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected sensitive request header to require approval, got %+v", response)
	}
	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("approve request: expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	var action approvalActionResponse
	decodeBody(t, approveResp.Body.Bytes(), &action)
	if action.ToolCall.Status != "success" {
		t.Fatalf("expected approved request to succeed, got %+v", action.ToolCall)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("expected one request, got %d", requestCount)
	}

	calls := listHTTPTestCalls(t, st, workspace.ID)
	if len(calls) != 1 {
		t.Fatalf("expected 1 audit call, got %d", len(calls))
	}
	call := calls[0]
	if call.Status != "success" || call.PolicyDecision != policyRequireApproval || call.ApprovalStatus != "approved" || call.RiskLevel != "high" || call.ToolKey != "http.request" {
		t.Fatalf("unexpected audit call: %+v", call)
	}
	if strings.Contains(string(call.InputRedactedJSON), requestSecret) {
		t.Fatalf("request secret leaked into audit input: %s", call.InputRedactedJSON)
	}
	if string(call.InputExecutionJSON) != "{}" || strings.Contains(string(call.InputExecutionJSON), requestSecret) {
		t.Fatalf("direct HTTP execution must not persist raw execution input: %s", call.InputExecutionJSON)
	}
	output := string(call.OutputRedactedJSON)
	for _, secret := range []string{responseHeaderSecret, responseBodySecret, "nested-secret", "session=secret", responseDescriptionEmail, responseDescriptionPhone, responseDescriptionToken} {
		if strings.Contains(output, secret) {
			t.Fatalf("response secret %q leaked into audit output: %s", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("expected redacted markers in output: %s", output)
	}
}

func TestHTTPRequestRedactsResolvedHeaderSecretsFromResponseAndAudit(t *testing.T) {
	const (
		shortSecret    = "xy"
		alphabetSecret = "orchid"
	)
	t.Setenv("HTTP_SHORT_SECRET_ENV", shortSecret)
	t.Setenv("HTTP_ALPHABET_SECRET_ENV", alphabetSecret)

	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Short-Auth") != shortSecret || r.Header.Get("X-Alphabet-Auth") != alphabetSecret {
			t.Fatalf("expected resolved header Secrets, got %+v", r.Header)
		}
		w.Header().Set("X-Reflected-Value", "header "+shortSecret+" "+alphabetSecret)
		writeJSON(w, http.StatusOK, map[string]any{
			"message":                  "plain " + shortSecret + " and " + alphabetSecret,
			shortSecret:                "short Secret reflected as a key",
			alphabetSecret:             "alphabet Secret reflected as a key",
			"prefix-" + alphabetSecret: "alphabet Secret reflected inside a key",
			"details": []any{
				map[string]any{"value": shortSecret},
				"nested " + alphabetSecret,
			},
		})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})
	for _, secret := range []struct {
		name     string
		valueRef string
	}{
		{name: "http_short_secret", valueRef: "HTTP_SHORT_SECRET_ENV"},
		{name: "http_alphabet_secret", valueRef: "HTTP_ALPHABET_SECRET_ENV"},
	} {
		if _, err := st.CreateSecret(context.Background(), model.CreateSecretInput{
			WorkspaceID:    workspace.ID,
			WorkspaceOrgID: workspace.ZitadelOrganizationID,
			Name:           secret.name,
			Description:    "HTTP reflected header Secret",
			Enabled:        true,
			SecretType:     "token",
			ValueSource:    "env",
			ValueRef:       secret.valueRef,
			Metadata:       json.RawMessage(`{"scope":"http"}`),
		}); err != nil {
			t.Fatalf("create Secret %s: %v", secret.name, err)
		}
	}

	body := fmt.Sprintf(`{
		"tool":"http.request",
		"arguments":{
			"method":"GET",
			"url":%q,
			"headerSecretRefs":{
				"X-Short-Auth":"http_short_secret",
				"X-Alphabet-Auth":"http_alphabet_secret"
			}
		}
	}`, mockHTTP.URL+"/reflect")
	resp := postJSON(t, srv, "/api/tool-calls", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "success" {
		t.Fatalf("expected direct HTTP success, got %+v", response)
	}
	apiOutput, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("marshal HTTP result: %v", err)
	}
	for _, secret := range []string{shortSecret, alphabetSecret} {
		if strings.Contains(string(apiOutput), secret) {
			t.Fatalf("HTTP API result leaked resolved Secret %q: %s", secret, apiOutput)
		}
	}
	if !strings.Contains(string(apiOutput), "[REDACTED]") {
		t.Fatalf("expected exact Secret redaction in HTTP API result: %s", apiOutput)
	}
	if !strings.Contains(string(apiOutput), `"[REDACTED]#2"`) {
		t.Fatalf("expected colliding redacted HTTP keys to be preserved: %s", apiOutput)
	}

	call := singleHTTPTestCall(t, st, workspace.ID)
	for _, secret := range []string{shortSecret, alphabetSecret} {
		if strings.Contains(string(call.OutputRedactedJSON), secret) {
			t.Fatalf("HTTP audit leaked resolved Secret %q: %s", secret, call.OutputRedactedJSON)
		}
	}
}

func TestHTTPRequestGETHonorsConfiguredApprovalPolicy(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	policyPath := writeAppPolicyFile(t, `
rules:
  - name: http-get-requires-approval
    priority: 1000
    match:
      tool_namespace: http
      tool_name: request
      operation_type: read
      user_role: owner
    effect: require_approval
    reason: configured policy requires HTTP review
`)
	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})
	srv.cfg.PolicyConfigPath = policyPath
	srv.reloadPolicyEngine()

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+mockHTTP.URL+`/status"}}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, resp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" || response.Reason != "configured policy requires HTTP review" {
		t.Fatalf("expected configured policy to require approval, got %+v", response)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("configured approval policy must prevent direct HTTP execution")
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "approval_required" || call.PolicyDecision != "require_approval" {
		t.Fatalf("unexpected approval audit: %+v", call)
	}
}

func TestHTTPRequestRejectsNonAllowlistHostWithAudit(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{"api.internal.local"},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+mockHTTP.URL+`/status"}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("non-allowlisted host should not be called")
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "failed" || !strings.Contains(call.ErrorMessage, "not allowed") {
		t.Fatalf("unexpected failed audit: %+v", call)
	}
}

func TestHTTPRequestRejectsSSRFAndNonHTTPScheme(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		allowedHosts []string
		requestURL   string
	}{
		{
			name:         "metadata",
			allowedHosts: []string{"169.254.169.254"},
			requestURL:   "http://169.254.169.254/latest/meta-data",
		},
		{
			name:         "non http scheme",
			allowedHosts: []string{"localhost"},
			requestURL:   "file:///etc/passwd",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st, workspace := newHTTPTestApp(t, httpTestConfig{allowedHosts: tc.allowedHosts})
			resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+tc.requestURL+`"}}`)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
			}
			call := singleHTTPTestCall(t, st, workspace.ID)
			if call.Status != "failed" || call.ErrorMessage == "" {
				t.Fatalf("expected failed audit, got %+v", call)
			}
		})
	}
}

func TestHTTPRequestRejectsForbiddenHeaders(t *testing.T) {
	t.Parallel()

	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("forbidden header request should not reach mock server")
	}))
	t.Cleanup(mockHTTP.Close)

	for _, header := range []string{"Authorization", "Cookie", "Host", "Proxy-Authorization", "Set-Cookie"} {
		t.Run(header, func(t *testing.T) {
			srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
				allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
			})
			body := `{"tool":"http.request","arguments":{"method":"GET","url":"` + mockHTTP.URL + `","headers":{"` + header + `":"secret"}}}`
			resp := postJSON(t, srv, "/api/tool-calls", body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
			}
			call := singleHTTPTestCall(t, st, workspace.ID)
			if call.Status != "failed" || !strings.Contains(call.ErrorMessage, "not allowed") {
				t.Fatalf("unexpected failed audit: %+v", call)
			}
			if strings.Contains(string(call.InputRedactedJSON), "secret") {
				t.Fatalf("forbidden header secret should be redacted in audit input: %s", call.InputRedactedJSON)
			}
		})
	}
}

func TestHTTPRequestRejectsInvalidHeaders(t *testing.T) {
	t.Parallel()

	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("invalid header request should not reach mock server")
	}))
	t.Cleanup(mockHTTP.Close)

	cases := []struct {
		name    string
		headers string
	}{
		{name: "invalid name", headers: `"Bad Header":"value"`},
		{name: "invalid value", headers: `"X-Demo":"hello\r\nX-Injected: yes"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
				allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
			})
			body := `{"tool":"http.request","arguments":{"method":"GET","url":"` + mockHTTP.URL + `","headers":{` + tc.headers + `}}}`
			resp := postJSON(t, srv, "/api/tool-calls", body)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
			}
			call := singleHTTPTestCall(t, st, workspace.ID)
			if call.Status != "failed" || !strings.Contains(call.ErrorMessage, "invalid") {
				t.Fatalf("unexpected failed audit: %+v", call)
			}
		})
	}
}

func TestHTTPRequestRejectsRedirectToNonAllowlistHost(t *testing.T) {
	t.Parallel()

	var targetCount int32
	targetHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(targetHTTP.Close)

	var redirectCount int32
	redirectHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectCount, 1)
		http.Redirect(w, r, targetHTTP.URL+"/secret", http.StatusFound)
	}))
	t.Cleanup(redirectHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, redirectHTTP.URL)},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+redirectHTTP.URL+`/start"}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt32(&redirectCount) != 1 {
		t.Fatalf("expected initial allowlisted redirect endpoint to be called once, got %d", redirectCount)
	}
	if atomic.LoadInt32(&targetCount) != 0 {
		t.Fatalf("redirect target outside allowlist must not be called")
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "failed" || call.ErrorMessage != "bad request: http redirect target is not allowed" {
		t.Fatalf("unexpected failed audit: %+v", call)
	}
	if strings.Contains(call.ErrorMessage, mustURLHost(t, targetHTTP.URL)) {
		t.Fatalf("redirect target leaked into audit error: %s", call.ErrorMessage)
	}
}

func TestHTTPRequestRejectsRedirectToMetadataAddress(t *testing.T) {
	t.Parallel()

	var redirectCount int32
	redirectHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectCount, 1)
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(redirectHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, redirectHTTP.URL)},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+redirectHTTP.URL+`/start"}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt32(&redirectCount) != 1 {
		t.Fatalf("expected initial redirect endpoint to be called once, got %d", redirectCount)
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "failed" || call.ErrorMessage != "bad request: http redirect target is not allowed" {
		t.Fatalf("unexpected failed audit: %+v", call)
	}
	if strings.Contains(call.ErrorMessage, "169.254.169.254") {
		t.Fatalf("metadata redirect target leaked into audit error: %s", call.ErrorMessage)
	}
}

func TestGuardedHTTPClientDisablesEnvironmentProxy(t *testing.T) {
	t.Parallel()

	client := newGuardedHTTPClient(0, []string{"example.test"})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("expected HTTP adapter transport to disable proxy")
	}
}

func TestHTTPRequestNon2xxWritesFailedAuditWithoutBodyLeak(t *testing.T) {
	t.Parallel()

	const secretBody = "internal-error-secret"
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(secretBody))
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"GET","url":"`+mockHTTP.URL+`/fail"}}`)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), secretBody) {
		t.Fatalf("non-2xx body leaked into response: %s", resp.Body.String())
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "failed" || call.ErrorMessage != "http request failed with status 418" {
		t.Fatalf("unexpected failed audit: %+v", call)
	}
	if strings.Contains(string(call.OutputRedactedJSON), secretBody) || strings.Contains(call.ErrorMessage, secretBody) {
		t.Fatalf("non-2xx body leaked into audit: %+v", call)
	}
}

func TestHTTPRequestPOSTRequiresApprovalThenExecutesAfterApprove(t *testing.T) {
	t.Parallel()

	const bodySecret = "approval-body-token"
	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["message"] != "hello" || body["token"] != bodySecret {
			t.Fatalf("approve must execute original body, got %+v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{"created": true})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello","token":"`+bodySecret+`"}}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("POST must not call target before approval")
	}
	pendingCall, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get pending call: %v", err)
	}
	if !strings.Contains(string(pendingCall.InputExecutionJSON), bodySecret) {
		t.Fatalf("approval_required call must keep raw execution input until review, got %s", pendingCall.InputExecutionJSON)
	}

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}
	if strings.Contains(approveResp.Body.String(), bodySecret) {
		t.Fatalf("approval response leaked original body secret: %s", approveResp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("expected one request after approve, got %d", requestCount)
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "success" || call.ApprovalStatus != "approved" || call.PolicyDecision != "require_approval" {
		t.Fatalf("unexpected approved audit: %+v", call)
	}
	if string(call.InputExecutionJSON) != "{}" {
		t.Fatalf("approved call must clear raw execution input, got %s", call.InputExecutionJSON)
	}
	if strings.Contains(string(call.InputRedactedJSON), bodySecret) || strings.Contains(string(call.OutputRedactedJSON), bodySecret) {
		t.Fatalf("approval audit must not expose original body secret: %+v", call)
	}
}

func TestHTTPRequestConcurrentApproveExecutesOnce(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		writeJSON(w, http.StatusOK, map[string]any{"created": true})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})
	gated := newGatedApprovalStore(st, 2)
	srv.store = gated

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello"}}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	responses := make(chan int, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
			responses <- resp.Code
		}()
	}
	gated.waitForTransitions(t)
	wg.Wait()
	close(responses)

	successCount := 0
	conflictCount := 0
	for code := range responses {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Fatalf("expected 200 or 409, got %d", code)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("expected one success and one conflict, got success=%d conflict=%d", successCount, conflictCount)
	}
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("concurrent approve must execute target once, got %d requests", requestCount)
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "success" || call.ApprovalStatus != "approved" {
		t.Fatalf("unexpected approved call after concurrent approve: %+v", call)
	}
}

func TestHTTPRequestApprovalRevalidatesAfterTransition(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		writeJSON(w, http.StatusOK, map[string]any{"created": true})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello"}}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	gated := newPostCASTransitionStore(st)
	srv.store = gated
	approveResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		approveResponse <- postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	}()

	gated.waitForTransition(t)
	t.Cleanup(gated.releaseTransition)
	tool, err := st.GetToolByKey(context.Background(), workspace.ID, "http.request")
	if err != nil {
		t.Fatalf("get http.request tool: %v", err)
	}
	disabled := false
	if _, err := st.UpdateTool(context.Background(), workspace.ID, tool.ID, model.UpdateToolInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable http.request after approval transition: %v", err)
	}
	gated.releaseTransition()

	select {
	case approveResp := <-approveResponse:
		if approveResp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
		}
		var action approvalActionResponse
		decodeBody(t, approveResp.Body.Bytes(), &action)
		if action.Approval.Status != "approved" {
			t.Fatalf("approval transition already succeeded, got %+v", action.Approval)
		}
		if action.ToolCall.Status != "failed" || action.ToolCall.ErrorMessage != approvalRevalidationFailedMessage {
			t.Fatalf("expected stable failed execution result, got %+v", action.ToolCall)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}

	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("revalidation failure must not call target, got %d requests", requestCount)
	}
	approval, err := st.GetApprovalRequestByID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get transitioned approval: %v", err)
	}
	if approval.Status != "approved" {
		t.Fatalf("approval review must remain approved, got %+v", approval)
	}
	call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get failed call: %v", err)
	}
	if call.Status != "failed" || call.ApprovalStatus != "approved" {
		t.Fatalf("unexpected call after revalidation failure: %+v", call)
	}
	if string(call.InputExecutionJSON) != "{}" || string(call.OutputRedactedJSON) != "{}" {
		t.Fatalf("revalidation failure must clear frozen input and output: %+v", call)
	}
	if call.ErrorMessage != approvalRevalidationFailedMessage {
		t.Fatalf("expected stable audit error, got %q", call.ErrorMessage)
	}

	retryResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if retryResp.Code != http.StatusConflict {
		t.Fatalf("expected retry conflict, got %d body=%s", retryResp.Code, retryResp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("retry must not call target, got %d requests", requestCount)
	}
}

func TestHTTPRequestApprovalRevalidatesAfterFrozenInputClear(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		writeJSON(w, http.StatusOK, map[string]any{"created": true})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items","body":{"message":"hello"}}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.Status != "approval_required" || response.ApprovalID == "" {
		t.Fatalf("expected approval_required, got %+v", response)
	}

	gated := newPostClearToolCallStore(st)
	srv.store = gated
	approveResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		approveResponse <- postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	}()

	gated.waitForClear(t)
	t.Cleanup(gated.releaseClear)
	tool, err := st.GetToolByKey(context.Background(), workspace.ID, "http.request")
	if err != nil {
		t.Fatalf("get http.request tool: %v", err)
	}
	disabled := false
	if _, err := st.UpdateTool(context.Background(), workspace.ID, tool.ID, model.UpdateToolInput{Enabled: &disabled}); err != nil {
		t.Fatalf("disable http.request after frozen input clear: %v", err)
	}
	gated.releaseClear()

	select {
	case approveResp := <-approveResponse:
		if approveResp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
		}
		var action approvalActionResponse
		decodeBody(t, approveResp.Body.Bytes(), &action)
		if action.Approval.Status != "approved" {
			t.Fatalf("approval transition already succeeded, got %+v", action.Approval)
		}
		if action.ToolCall.Status != "failed" || action.ToolCall.ErrorMessage != approvalRevalidationFailedMessage {
			t.Fatalf("expected final revalidation failure, got %+v", action.ToolCall)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval response")
	}

	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("final revalidation failure must not call target, got %d requests", requestCount)
	}
	call, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get failed call: %v", err)
	}
	if call.Status != "failed" || call.ApprovalStatus != "approved" {
		t.Fatalf("unexpected call after final revalidation failure: %+v", call)
	}
	if string(call.InputExecutionJSON) != "{}" || string(call.OutputRedactedJSON) != "{}" {
		t.Fatalf("final revalidation failure must keep execution data cleared: %+v", call)
	}
}

func TestHTTPRequestRejectSkipsTarget(t *testing.T) {
	t.Parallel()

	const bodySecret = "reject-body-token"
	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"DELETE","url":"`+mockHTTP.URL+`/items/1","body":{"token":"`+bodySecret+`"}}}`)
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	pendingCall, err := st.GetToolCallByApprovalID(context.Background(), workspace.ID, response.ApprovalID)
	if err != nil {
		t.Fatalf("get pending call: %v", err)
	}
	if !strings.Contains(string(pendingCall.InputExecutionJSON), bodySecret) {
		t.Fatalf("approval_required reject path must keep raw execution input until review, got %s", pendingCall.InputExecutionJSON)
	}
	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", "")
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}
	if strings.Contains(rejectResp.Body.String(), bodySecret) {
		t.Fatalf("reject response leaked original body secret: %s", rejectResp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("reject must not call target")
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "rejected" || call.ApprovalStatus != "rejected" {
		t.Fatalf("unexpected rejected audit: %+v", call)
	}
	if string(call.InputExecutionJSON) != "{}" {
		t.Fatalf("rejected call must clear raw execution input, got %s", call.InputExecutionJSON)
	}
	if strings.Contains(string(call.InputRedactedJSON), bodySecret) || strings.Contains(string(call.OutputRedactedJSON), bodySecret) {
		t.Fatalf("reject audit must not expose original body secret: %+v", call)
	}
}

func TestHTTPRequestRejectsMethodNotConfigured(t *testing.T) {
	t.Parallel()

	var requestCount int32
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts:   []string{mustURLHost(t, mockHTTP.URL)},
		allowedMethods: []string{http.MethodGet},
	})

	resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"http.request","arguments":{"method":"POST","url":"`+mockHTTP.URL+`/items"}}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", resp.Code, resp.Body.String())
	}
	if atomic.LoadInt32(&requestCount) != 0 {
		t.Fatalf("method validation should happen before target request")
	}
	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.Status != "failed" || !strings.Contains(call.ErrorMessage, "not allowed") {
		t.Fatalf("unexpected failed audit: %+v", call)
	}
}

func TestHTTPRequestRedactsSensitiveURLQueryFromResponseAndAudit(t *testing.T) {
	const (
		tokenValue     = "synthetic-query-token"
		signatureValue = "synthetic-query-signature"
	)
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	t.Cleanup(mockHTTP.Close)

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{mustURLHost(t, mockHTTP.URL)},
	})
	target := mockHTTP.URL + "/status?token=" + tokenValue + "&page=1&signature=" + signatureValue
	resp := postJSON(t, srv, "/api/tool-calls", fmt.Sprintf(
		`{"tool":"http.request","arguments":{"method":"GET","url":%q}}`,
		target,
	))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	for _, secret := range []string{tokenValue, signatureValue} {
		if strings.Contains(resp.Body.String(), secret) {
			t.Fatalf("HTTP response leaked URL query secret %q: %s", secret, resp.Body.String())
		}
	}
	if !strings.Contains(resp.Body.String(), "page=1") || !strings.Contains(resp.Body.String(), "[REDACTED]") {
		t.Fatalf("expected non-sensitive query and redaction marker, got %s", resp.Body.String())
	}

	call := singleHTTPTestCall(t, st, workspace.ID)
	for _, field := range []string{string(call.InputRedactedJSON), string(call.OutputRedactedJSON), call.ErrorMessage} {
		if strings.Contains(field, tokenValue) || strings.Contains(field, signatureValue) {
			t.Fatalf("HTTP audit leaked URL query secret: %+v", call)
		}
	}
}

func TestHTTPRequestNetworkFailureDoesNotPersistSensitiveURL(t *testing.T) {
	const tokenValue = "synthetic-failed-query-token"
	mockHTTP := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	targetHost := mustURLHost(t, mockHTTP.URL)
	target := mockHTTP.URL + "/status?access_token=" + tokenValue
	mockHTTP.Close()

	srv, st, workspace := newHTTPTestApp(t, httpTestConfig{
		allowedHosts: []string{targetHost},
	})
	resp := postJSON(t, srv, "/api/tool-calls", fmt.Sprintf(
		`{"tool":"http.request","arguments":{"method":"GET","url":%q}}`,
		target,
	))
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), tokenValue) {
		t.Fatalf("HTTP failure response leaked URL query secret: %s", resp.Body.String())
	}

	call := singleHTTPTestCall(t, st, workspace.ID)
	if call.ErrorMessage != "http request failed" {
		t.Fatalf("expected stable network failure message, got %q", call.ErrorMessage)
	}
	for _, field := range []string{string(call.InputRedactedJSON), string(call.OutputRedactedJSON), call.ErrorMessage} {
		if strings.Contains(field, tokenValue) {
			t.Fatalf("HTTP failure audit leaked URL query secret: %+v", call)
		}
	}
}

type httpTestConfig struct {
	allowedHosts   []string
	allowedMethods []string
}

func newHTTPTestApp(t *testing.T, overrides httpTestConfig) (*App, store.Store, model.Workspace) {
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

	allowedHosts := overrides.allowedHosts
	if len(allowedHosts) == 0 {
		allowedHosts = []string{"127.0.0.1:18080"}
	}
	cfg := config.Config{
		AuthMode:              "local",
		DefaultWorkspaceOrgID: "local-org",
		LocalSubject:          "local-dev",
		LocalEmail:            "dev@agenttoolgate.local",
		LocalName:             "Local Developer",
		LocalRole:             "owner",
		CORSAllowedOrigins:    []string{"*"},
		HTTPAllowedHosts:      allowedHosts,
		HTTPAllowedMethods:    overrides.allowedMethods,
		HTTPTimeoutMs:         3000,
		HTTPMaxResponseBytes:  65536,
	}
	authenticator, err := auth.NewAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	appStore := approvalReviewableTestStore(st)
	srv := New(cfg, appStore, authenticator, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	return srv, appStore, workspaces[0]
}

func mustURLHost(t *testing.T, rawURL string) string {
	t.Helper()

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return parsed.Host
}

func listHTTPTestCalls(t *testing.T, st store.Store, workspaceID string) []model.ToolCall {
	t.Helper()

	calls, err := st.ListToolCalls(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("list tool calls: %v", err)
	}
	return calls
}

func singleHTTPTestCall(t *testing.T, st store.Store, workspaceID string) model.ToolCall {
	t.Helper()

	calls := listHTTPTestCalls(t, st, workspaceID)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	return calls[0]
}

type gatedApprovalStore struct {
	store.Store
	expected int
	entered  chan struct{}
	release  chan struct{}
}

func newGatedApprovalStore(st store.Store, expected int) *gatedApprovalStore {
	return &gatedApprovalStore{
		Store:    st,
		expected: expected,
		entered:  make(chan struct{}, expected),
		release:  make(chan struct{}),
	}
}

func (s *gatedApprovalStore) UpdateApprovalRequest(ctx context.Context, workspaceID, approvalID string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	s.enterTransition()
	return s.Store.UpdateApprovalRequest(ctx, workspaceID, approvalID, input)
}

func (s *gatedApprovalStore) TransitionApprovalRequest(ctx context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	s.enterTransition()
	return s.Store.TransitionApprovalRequest(ctx, workspaceID, approvalID, fromStatus, input)
}

func (s *gatedApprovalStore) enterTransition() {
	s.entered <- struct{}{}
	<-s.release
}

func (s *gatedApprovalStore) waitForTransitions(t *testing.T) {
	t.Helper()

	for i := 0; i < s.expected; i++ {
		select {
		case <-s.entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for concurrent approval transition %d/%d", i+1, s.expected)
		}
	}
	close(s.release)
}

type postCASTransitionStore struct {
	store.Store
	transitioned     chan struct{}
	release          chan struct{}
	transitionedOnce sync.Once
	releaseOnce      sync.Once
}

func newPostCASTransitionStore(st store.Store) *postCASTransitionStore {
	return &postCASTransitionStore{
		Store:        st,
		transitioned: make(chan struct{}),
		release:      make(chan struct{}),
	}
}

func (s *postCASTransitionStore) TransitionApprovalRequest(ctx context.Context, workspaceID, approvalID, fromStatus string, input model.UpdateApprovalRequestInput) (model.ApprovalRequest, error) {
	approval, err := s.Store.TransitionApprovalRequest(ctx, workspaceID, approvalID, fromStatus, input)
	if err != nil {
		return approval, err
	}
	if strings.EqualFold(strings.TrimSpace(fromStatus), "pending") &&
		strings.EqualFold(strings.TrimSpace(input.Status), "approved") {
		s.transitionedOnce.Do(func() {
			close(s.transitioned)
			<-s.release
		})
	}
	return approval, nil
}

func (s *postCASTransitionStore) waitForTransition(t *testing.T) {
	t.Helper()

	select {
	case <-s.transitioned:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for approval transition")
	}
}

func (s *postCASTransitionStore) releaseTransition() {
	s.releaseOnce.Do(func() {
		close(s.release)
	})
}

type postClearToolCallStore struct {
	store.Store
	cleared     chan struct{}
	release     chan struct{}
	clearOnce   sync.Once
	releaseOnce sync.Once
}

func newPostClearToolCallStore(st store.Store) *postClearToolCallStore {
	return &postClearToolCallStore{
		Store:   st,
		cleared: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *postClearToolCallStore) TransitionToolCall(ctx context.Context, workspaceID, callID, fromStatus string, input model.UpdateToolCallInput) (model.ToolCall, error) {
	call, err := s.Store.TransitionToolCall(ctx, workspaceID, callID, fromStatus, input)
	if err != nil {
		return call, err
	}
	if strings.EqualFold(strings.TrimSpace(fromStatus), "approval_required") &&
		strings.EqualFold(strings.TrimSpace(input.Status), "approval_required") &&
		strings.TrimSpace(string(input.InputExecutionJSON)) == "{}" {
		s.clearOnce.Do(func() {
			close(s.cleared)
			<-s.release
		})
	}
	return call, nil
}

func (s *postClearToolCallStore) waitForClear(t *testing.T) {
	t.Helper()

	select {
	case <-s.cleared:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for frozen input clear")
	}
}

func (s *postClearToolCallStore) releaseClear() {
	s.releaseOnce.Do(func() {
		close(s.release)
	})
}

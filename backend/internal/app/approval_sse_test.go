package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agenttoolgate/backend/internal/config"
	"agenttoolgate/backend/internal/model"
	"agenttoolgate/backend/internal/store"

	"github.com/google/uuid"
)

func TestApprovalSSEBroadcastsPendingAndApprovedEvents(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", true)

	httpServer := httptest.NewServer(srv.Router())
	t.Cleanup(httpServer.Close)

	resp, reader := openApprovalStream(t, httpServer.URL, workspace.ZitadelOrganizationID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.ApprovalID == "" {
		t.Fatalf("approval id missing: %+v", response)
	}

	eventName, eventData := readNextSSEEvent(t, reader)
	if eventName != "approval" {
		t.Fatalf("expected approval event, got %q data=%s", eventName, eventData)
	}
	assertSSEApprovalPayload(t, eventData, response.ApprovalID, "pending", "mock.write")

	approveResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/approve", "")
	if approveResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", approveResp.Code, approveResp.Body.String())
	}

	eventName, eventData = readNextSSEEvent(t, reader)
	if eventName != "approval" {
		t.Fatalf("expected approval event, got %q data=%s", eventName, eventData)
	}
	assertSSEApprovalPayload(t, eventData, response.ApprovalID, "approved", "mock.write")
}

func TestApprovalSSEBroadcastsRejectedEvent(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	createMockTool(t, st, workspace.ID, "mock", "reject", "Mock Reject", "write", "low", true)

	httpServer := httptest.NewServer(srv.Router())
	t.Cleanup(httpServer.Close)

	resp, reader := openApprovalStream(t, httpServer.URL, workspace.ZitadelOrganizationID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.reject","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}

	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)
	if response.ApprovalID == "" {
		t.Fatalf("approval id missing: %+v", response)
	}

	eventName, eventData := readNextSSEEvent(t, reader)
	if eventName != "approval" {
		t.Fatalf("expected approval event, got %q data=%s", eventName, eventData)
	}
	assertSSEApprovalPayload(t, eventData, response.ApprovalID, "pending", "mock.reject")

	rejectResp := postJSON(t, srv, "/api/approvals/"+response.ApprovalID+"/reject", "")
	if rejectResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rejectResp.Code, rejectResp.Body.String())
	}

	eventName, eventData = readNextSSEEvent(t, reader)
	if eventName != "approval" {
		t.Fatalf("expected approval event, got %q data=%s", eventName, eventData)
	}
	assertSSEApprovalPayload(t, eventData, response.ApprovalID, "rejected", "mock.reject")
}

func TestApprovalSSEAcceptsWorkspaceHeaderAndIsolatesWorkspaces(t *testing.T) {
	t.Parallel()

	srv, st, workspace := newGovernanceTestApp(t)
	otherWorkspace, err := st.CreateWorkspace(context.Background(), model.CreateWorkspaceInput{
		Name:                  "Other Workspace",
		Slug:                  "other-" + uuid.NewString(),
		ZitadelOrganizationID: "other-org-" + uuid.NewString(),
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatalf("create other workspace: %v", err)
	}
	createMockTool(t, st, workspace.ID, "mock", "write", "Mock Write", "write", "low", true)
	createMockTool(t, st, otherWorkspace.ID, "mock", "otherwrite", "Other Mock Write", "write", "low", true)

	httpServer := httptest.NewServer(srv.Router())
	t.Cleanup(httpServer.Close)

	resp, reader := openApprovalStreamWithHeader(t, httpServer.URL, workspace.ZitadelOrganizationID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	otherCallResp := postJSONWithWorkspace(t, srv, otherWorkspace.ZitadelOrganizationID, "/api/tool-calls", `{"tool":"mock.otherwrite","arguments":{"message":"other"}}`)
	if otherCallResp.Code != http.StatusOK {
		t.Fatalf("expected other workspace call 200, got %d body=%s", otherCallResp.Code, otherCallResp.Body.String())
	}

	callResp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.write","arguments":{"message":"hello"}}`)
	if callResp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", callResp.Code, callResp.Body.String())
	}
	var response toolCallResponse
	decodeBody(t, callResp.Body.Bytes(), &response)

	eventName, eventData := readNextSSEEvent(t, reader)
	if eventName != "approval" {
		t.Fatalf("expected approval event, got %q data=%s", eventName, eventData)
	}
	assertSSEApprovalPayload(t, eventData, response.ApprovalID, "pending", "mock.write")
}

func TestOIDCApprovalStreamWorkspaceSelectionRequiresHeader(t *testing.T) {
	t.Parallel()

	app := &App{cfg: config.Config{AuthMode: "oidc"}}
	req := httptest.NewRequest(http.MethodGet, "/api/approvals/stream?workspaceOrgId=query-org", nil)
	if got := app.requestedWorkspaceOrgID(req); got != "" {
		t.Fatalf("oidc approval stream must not accept workspace query fallback, got %q", got)
	}

	headerReq := httptest.NewRequest(http.MethodGet, "/api/approvals/stream?workspaceOrgId=query-org", nil)
	headerReq.Header.Set("X-Workspace-Org-Id", "header-org")
	if got := app.requestedWorkspaceOrgID(headerReq); got != "header-org" {
		t.Fatalf("oidc approval stream must select workspace from header, got %q", got)
	}
}

func postJSONWithWorkspace(t *testing.T, srv *App, workspaceOrgID, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Workspace-Org-Id", workspaceOrgID)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func openApprovalStream(t *testing.T, baseURL, workspaceOrgID string) (*http.Response, *bufio.Reader) {
	t.Helper()

	streamURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse stream base url: %v", err)
	}
	streamURL.Path = "/api/approvals/stream"
	query := streamURL.Query()
	query.Set("workspaceOrgId", workspaceOrgID)
	streamURL.RawQuery = query.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, streamURL.String(), nil)
	if err != nil {
		t.Fatalf("new sse request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open approval stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body)
}

func openApprovalStreamWithHeader(t *testing.T, baseURL, workspaceOrgID string) (*http.Response, *bufio.Reader) {
	t.Helper()

	streamURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse stream base url: %v", err)
	}
	streamURL.Path = "/api/approvals/stream"

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, streamURL.String(), nil)
	if err != nil {
		t.Fatalf("new sse request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Workspace-Org-Id", workspaceOrgID)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open approval stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	return resp, bufio.NewReader(resp.Body)
}

func readNextSSEEvent(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()

	var eventName string
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read sse event: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if eventName == "" && len(dataLines) == 0 {
				continue
			}
			return eventName, strings.Join(dataLines, "\n")
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func assertSSEApprovalPayload(t *testing.T, rawData, wantID, wantStatus, wantToolKey string) {
	t.Helper()

	var payload approvalSSEPayload
	if err := json.Unmarshal([]byte(rawData), &payload); err != nil {
		t.Fatalf("decode approval sse payload: %v data=%s", err, rawData)
	}
	if payload.ID != wantID || payload.Status != wantStatus || payload.ToolKey != wantToolKey {
		t.Fatalf("unexpected approval payload: %+v", payload)
	}
	if payload.CreatedAt.IsZero() {
		t.Fatalf("approval event must carry createdAt: %+v", payload)
	}
}

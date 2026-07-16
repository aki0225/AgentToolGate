package app

import (
	"agenttoolgate/backend/internal/model"
	"net/http"
	"testing"
)

func TestToolCallAuditPaginationAndFiltering(t *testing.T) {
	t.Parallel()

	srv, _, _ := newGovernanceTestApp(t)
	for i := 0; i < 15; i++ {
		resp := postJSON(t, srv, "/api/tool-calls", `{"tool":"mock.echo","arguments":{"message":"hello"}}`)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected success for call %d, got %d body=%s", i+1, resp.Code, resp.Body.String())
		}
	}

	resp := getJSON(t, srv, "/api/tool-calls?tool=mock.echo&status=success&page=2&pageSize=10")
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var page model.ToolCallPage
	decodeBody(t, resp.Body.Bytes(), &page)
	if page.Total != 15 || page.Page != 2 || page.PageSize != 10 {
		t.Fatalf("unexpected pagination metadata: %+v", page)
	}
	if len(page.Items) != 5 {
		t.Fatalf("expected 5 tool calls on page 2, got %d", len(page.Items))
	}
	for _, call := range page.Items {
		if call.ToolKey != "mock.echo" || call.Status != "success" {
			t.Fatalf("unexpected filtered call: %+v", call)
		}
	}
}

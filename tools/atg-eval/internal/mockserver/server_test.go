package mockserver

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"agenttoolgate/evaluation/internal/redact"
)

func TestServerRecordsOnlyRedactedLoopbackEvidence(t *testing.T) {
	secret := "synthetic-loopback-secret"
	server, err := New(Options{
		Redactor: redact.New(redact.Options{Secrets: []string{secret}}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	request, err := http.NewRequest(
		http.MethodPost,
		server.URL()+"/collect/"+secret+"?token="+secret,
		strings.NewReader(`{"value":"`+secret+`"}`),
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := server.Requests()
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	record := records[0]
	if !record.SensitiveDetected {
		t.Fatal("应识别 synthetic secret")
	}
	serialized := record.Path + record.Query + record.Body + record.Headers["Authorization"]
	if strings.Contains(serialized, secret) {
		t.Fatalf("证据泄露 synthetic secret：%+v", record)
	}
	if record.Headers["Authorization"] != redact.RedactedValue {
		t.Fatalf("Authorization 未完整脱敏：%+v", record.Headers)
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	server, err := New(Options{MaxBodyBytes: 8})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	response, err := http.Post(server.URL()+"/upload", "text/plain", strings.NewReader("123456789"))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", response.StatusCode)
	}
	records := server.Requests()
	if len(records) != 1 || !records[0].Truncated || records[0].BodyBytes != 8 {
		t.Fatalf("unexpected record: %+v", records)
	}
}

func TestValidateLoopbackURL(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:18080/path",
		"http://[::1]:18080/path",
	} {
		if err := ValidateLoopbackURL(rawURL); err != nil {
			t.Fatalf("%s should be allowed: %v", rawURL, err)
		}
	}
	for _, rawURL := range []string{
		"https://example.com:443/path",
		"http://localhost:18080/path",
		"http://127.0.0.1/path",
		"file:///tmp/data",
		"http://user:password@127.0.0.1:18080/path",
	} {
		if err := ValidateLoopbackURL(rawURL); err == nil {
			t.Fatalf("%s should be rejected", rawURL)
		}
	}
}
